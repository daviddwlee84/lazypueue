package pueue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/pelletier/go-toml/v2"
)

func taskAt(s core.Snapshot, id int) (core.Task, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return core.Task{}, false
}
func original(t core.Task) string {
	if t.OriginalCommand != "" {
		return t.OriginalCommand
	}
	return t.Command
}
func markError(result core.Result, err error) (core.Result, error) {
	var e *Error
	if errors.As(err, &e) {
		result.Unknown = e.Unknown
	}
	return result, err
}

func (c *Client) waitTask(ctx context.Context, conn core.Connection, id int, created time.Time, accept func(core.Task) bool) (core.Task, error) {
	deadline := time.NewTimer(6 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		snap, err := c.Snapshot(ctx, conn)
		if err != nil {
			return core.Task{}, err
		}
		task, ok := taskAt(snap, id)
		if !ok || !task.CreatedAt.Equal(created) {
			return core.Task{}, fmt.Errorf("task #%d disappeared or its ID was reused", id)
		}
		if accept(task) {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return task, ctx.Err()
		case <-deadline.C:
			return task, fmt.Errorf("task #%d did not reach the expected state; inspect it before retrying", id)
		case <-tick.C:
		}
	}
}

func editorScript(id int, e core.EditRequest) (string, error) {
	type editable struct {
		ID       int    `toml:"id"`
		Command  string `toml:"command"`
		Path     string `toml:"path"`
		Label    string `toml:"label,omitempty"`
		Priority int    `toml:"priority"`
	}
	data, err := toml.Marshal(map[string]editable{strconv.Itoa(id): {id, e.Command, e.Directory, e.Label, e.Priority}})
	if err != nil {
		return "", err
	}
	script := "#!/bin/sh\nset -eu\np=$1\n[ -n \"$p\" ] || exit 72\n"
	script += "if [ -f \"$p\" ]; then\n  printf '%s' " + shellQuote(string(data)) + " > \"$p\"\n"
	script += "elif [ -d \"$p/" + strconv.Itoa(id) + "\" ]; then\n"
	for _, field := range []struct{ name, value string }{{"command", e.Command}, {"path", e.Directory}, {"label", e.Label}, {"priority", strconv.Itoa(e.Priority)}} {
		script += "  printf '%s' " + shellQuote(field.value) + " > \"$p/" + strconv.Itoa(id) + "/" + field.name + "\"\n"
	}
	script += "else\n  printf '%s\\n' 'Unexpected Pueue editor path; no edit applied' >&2\n  exit 73\nfi\n"
	return script, nil
}

func (c *Client) controlledEdit(parent context.Context, conn core.Connection, args []string, id int, e core.EditRequest) ([]byte, error) {
	script, err := editorScript(id, e)
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.operationContext(parent, writeTimeout)
	defer cancel()
	cmd, err := c.command(ctx, conn, args, false)
	if err != nil {
		return nil, err
	}
	if conn.Kind == "ssh" {
		remote := cmd.Args[len(cmd.Args)-1]
		cmd.Args[len(cmd.Args)-1] = "sh -s"
		wrapper := "set -eu\numask 077\nwork=$(mktemp -d /tmp/lazypueue-edit.XXXXXX)\ntrap 'rm -rf \"$work\"' 0\ntrap 'exit 130' 1 2 15\n"
		wrapper += "printf '%s' " + shellQuote(script) + " > \"$work/editor\"\nchmod 700 \"$work/editor\"\n"
		wrapper += "EDITOR=\"sh $work/editor\" " + remote + " </dev/null\n"
		cmd.Stdin = strings.NewReader(wrapper)
	} else {
		dir, err := c.tempDir()
		if err != nil {
			return nil, err
		}
		file, err := os.CreateTemp(dir, "editor-*.sh")
		if err != nil {
			return nil, err
		}
		path := file.Name()
		defer os.Remove(path)
		if _, err = file.WriteString(script); err != nil {
			file.Close()
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
		cmd.Env = []string{}
		for _, env := range os.Environ() {
			if !strings.HasPrefix(env, "EDITOR=") {
				cmd.Env = append(cmd.Env, env)
			}
		}
		cmd.Env = append(cmd.Env, "EDITOR=sh "+shellQuote(path))
	}
	output := &boundedBuffer{limit: maxOutput}
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return nil, classify(err, stderr.String(), ctx, false, true)
	}
	if err = cmd.Wait(); err != nil {
		return nil, classify(err, stderr.String(), ctx, true, true)
	}
	if output.overflow {
		return nil, &Error{Kind: "output-too-large", Detail: "Edit response exceeded limit; inspect the task", Unknown: true}
	}
	return output.Bytes(), nil
}

func (c *Client) editTask(ctx context.Context, conn core.Connection, req core.Request, before core.Snapshot) (core.Result, error) {
	id := req.IDs[0]
	result := core.Result{ConnectionID: conn.ID, Outcomes: []core.Outcome{}}
	task, exists := taskAt(before, id)
	if !exists || !core.Eligible(req.Operation, task) {
		return result, fmt.Errorf("task #%d is not eligible for %s", id, req.Operation)
	}
	if req.Operation == "recover-stash" {
		if _, err := c.run(ctx, conn, []string{"stash", strconv.Itoa(id)}, true, false); err != nil {
			return markError(result, err)
		}
		if _, err := c.waitTask(ctx, conn, id, task.CreatedAt, func(t core.Task) bool { return !t.Locked && t.State() == "stashed" }); err != nil {
			return markError(result, err)
		}
		result.Message = fmt.Sprintf("Recovered task #%d to stash; no task was started", id)
		result.TaskID = &id
		return result, nil
	}
	e := *req.Edit
	if (conn.Kind == "native" || conn.Kind == "ssh") && !filepath.IsAbs(e.Directory) {
		return result, fmt.Errorf("editing a remote task requires an absolute directory on the daemon host")
	}
	if req.Operation == "edit" && task.ScheduledAt != nil && e.Stashed {
		return result, fmt.Errorf("scheduled task retains its schedule; choose enqueue after edit or duplicate as an unscheduled stashed task")
	}
	if conn.Kind == "" || conn.Kind == "local" {
		e.Directory = expandLocal(e.Directory)
	}
	if req.Operation == "stop-edit-restart" {
		if _, err := c.run(ctx, conn, []string{"kill", strconv.Itoa(id)}, true, false); err != nil {
			return markError(result, err)
		}
		var err error
		task, err = c.waitTask(ctx, conn, id, task.CreatedAt, func(t core.Task) bool { return t.Terminal() })
		if err != nil {
			return result, fmt.Errorf("stop requested, but completion could not be confirmed: %w", err)
		}
		result.Message = fmt.Sprintf("Stopped task #%d. ", id)
	}
	if req.Operation == "edit" && task.State() == "queued" {
		if _, err := c.run(ctx, conn, []string{"stash", strconv.Itoa(id)}, true, false); err != nil {
			return markError(result, err)
		}
		var err error
		task, err = c.waitTask(ctx, conn, id, task.CreatedAt, func(t core.Task) bool { return t.State() == "stashed" || t.State() == "running" || t.Terminal() })
		if err != nil {
			return result, fmt.Errorf("stash requested; inspect the task before retrying: %w", err)
		}
		if task.State() != "stashed" {
			return result, fmt.Errorf("task started before it could be stashed; nothing was edited")
		}
		result.Message = fmt.Sprintf("Stashed task #%d. ", id)
	}
	fresh, err := c.Snapshot(ctx, conn)
	if err != nil {
		return result, fmt.Errorf("%sUnable to revalidate before edit: %w", result.Message, err)
	}
	current, ok := taskAt(fresh, id)
	if !ok || !current.CreatedAt.Equal(task.CreatedAt) || current.Locked {
		return result, fmt.Errorf("%sTask changed before edit; no edit submitted", result.Message)
	}
	isRetry := req.Operation != "edit"
	if (isRetry && !current.Terminal()) || (!isRetry && current.State() != "stashed") {
		return result, fmt.Errorf("%sTask state changed before edit", result.Message)
	}
	args := []string{"edit", strconv.Itoa(id)}
	if isRetry {
		mode := "--not-in-place"
		if req.InPlace {
			mode = "--in-place"
		}
		args = []string{"restart", mode, "--edit", "--stashed", strconv.Itoa(id)}
	}
	if _, err = c.controlledEdit(ctx, conn, args, id, e); err != nil {
		result, err = markError(result, err)
		detail := result.Message + "Edit did not complete; the task may be Locked or remain stashed. Refresh before using Recover to stash. " + err.Error()
		return result, &Error{Kind: "edit-failed", Detail: detail, Unknown: result.Unknown, Cause: err}
	}
	targetID := id
	targetCreated := task.CreatedAt
	if isRetry && !req.InPlace {
		known := map[string]bool{}
		for _, t := range fresh.Tasks {
			known[t.Identity()] = true
		}
		deadline := time.Now().Add(4 * time.Second)
		for {
			after, readErr := c.Snapshot(ctx, conn)
			if readErr != nil {
				return core.Result{ConnectionID: conn.ID, Unknown: true}, &Error{Kind: "unknown-outcome", Detail: "Retry submitted; could not identify the new task. Inspect the queue before retrying", Unknown: true, Cause: readErr}
			}
			candidates := []core.Task{}
			for _, t := range after.Tasks {
				if !known[t.Identity()] && original(t) == e.Command && t.Path == e.Directory && t.Label == e.Label && t.Priority == e.Priority && t.Group == task.Group {
					candidates = append(candidates, t)
				}
			}
			if len(candidates) == 1 {
				targetID = candidates[0].ID
				targetCreated = candidates[0].CreatedAt
				break
			}
			if len(candidates) > 1 || time.Now().After(deadline) {
				return core.Result{ConnectionID: conn.ID, Unknown: true}, &Error{Kind: "unknown-outcome", Detail: "Retry submitted but its new task ID is ambiguous; inspect the queue before retrying", Unknown: true}
			}
			select {
			case <-ctx.Done():
				return core.Result{ConnectionID: conn.ID, Unknown: true}, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	_, err = c.waitTask(ctx, conn, targetID, targetCreated, func(t core.Task) bool {
		return !t.Locked && t.State() == "stashed" && original(t) == e.Command && t.Path == e.Directory && t.Label == e.Label && t.Priority == e.Priority
	})
	if err != nil {
		return core.Result{ConnectionID: conn.ID, Unknown: true}, &Error{Kind: "unknown-outcome", Detail: "Edit was submitted but its result could not be confirmed; inspect the task before retrying", Unknown: true, Cause: err}
	}
	if !e.Stashed {
		if _, err = c.run(ctx, conn, []string{"enqueue", strconv.Itoa(targetID)}, true, false); err != nil {
			result, err = markError(result, err)
			result.TaskID = &targetID
			return result, fmt.Errorf("edited task #%d, but enqueue failed: %w", targetID, err)
		}
	}
	result.TaskID = &targetID
	result.Outcomes = []core.Outcome{{ID: targetID}}
	state := "queued"
	if e.Stashed {
		state = "stashed"
	}
	result.Message += fmt.Sprintf("Saved task #%d; %s", targetID, state)
	return result, nil
}
