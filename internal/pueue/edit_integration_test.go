package pueue

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("LAZYPUEUE_INTEGRATION") != "1" {
		t.Skip("set LAZYPUEUE_INTEGRATION=1 for isolated daemons")
	}
	for _, name := range []string{"pueue", "pueued"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
}

func TestIsolatedEditClearAndBatch(t *testing.T) {
	requireIntegration(t)
	for _, transport := range []string{"unix", "tls"} {
		t.Run(transport, func(t *testing.T) {
			c, local, native, dir := isolatedDaemon(t, transport)
			ctx := context.Background()
			run := func(conn core.Connection, r core.Request) core.Result {
				t.Helper()
				v, err := c.Execute(ctx, conn, r)
				if err != nil {
					t.Fatalf("%s: %v (%+v)", r.Operation, err, v)
				}
				return v
			}
			add := func(command, group, mode string, after ...int) int {
				t.Helper()
				v := run(local, core.Request{Operation: "add", Add: &core.AddRequest{Command: command, Directory: dir, Group: group, Mode: mode, After: after}})
				return *v.TaskID
			}
			get := func(id int) core.Task {
				t.Helper()
				s, err := c.Snapshot(ctx, native)
				if err != nil {
					t.Fatal(err)
				}
				v, ok := taskAt(s, id)
				if !ok {
					t.Fatalf("missing task %d", id)
				}
				return v
			}
			await := func(id int, state string) core.Task {
				t.Helper()
				v := get(id)
				out, err := c.waitTask(ctx, native, id, v.CreatedAt, func(t core.Task) bool { return t.State() == state })
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			run(local, core.Request{Operation: "group-add", Group: "edit", Parallel: 1})
			id := add("echo wrong", "edit", "stashed")
			initial := get(id)
			text := "printf '%s\\n' 'edited 中文 \"quotes\"' # $HOME $(printf not-executed)"
			e := core.EditRequest{Command: text, Directory: dir, Label: "edited label", Priority: -3, Stashed: true}
			run(native, core.Request{Operation: "edit", IDs: []int{id}, Guards: map[int]time.Time{id: initial.CreatedAt}, Edit: &e})
			saved := get(id)
			if saved.Command != text || saved.Label != e.Label || saved.Priority != -3 || saved.State() != "stashed" {
				t.Fatalf("bad edit: %+v", saved)
			}
			e.Stashed = false
			run(native, core.Request{Operation: "edit", IDs: []int{id}, Guards: map[int]time.Time{id: initial.CreatedAt}, Edit: &e})
			await(id, "succeeded")
			oldLog, err := c.Log(ctx, native, id, 30)
			if err != nil {
				t.Fatal(err)
			}
			e.Command = "printf 'retry\\n'"
			copy := run(native, core.Request{Operation: "edit-restart", IDs: []int{id}, Guards: map[int]time.Time{id: initial.CreatedAt}, Edit: &e})
			if *copy.TaskID == id {
				t.Fatal("copy retry reused ID")
			}
			await(*copy.TaskID, "succeeded")
			if log, _ := c.Log(ctx, native, id, 30); log != oldLog {
				t.Fatal("copy retry changed original log")
			}
			e.Command = "printf 'in-place\\n'"
			run(native, core.Request{Operation: "edit-restart", IDs: []int{id}, InPlace: true, Guards: map[int]time.Time{id: initial.CreatedAt}, Edit: &e})
			await(id, "succeeded")
			if log, _ := c.Log(ctx, native, id, 30); !strings.Contains(log, "in-place") {
				t.Fatalf("retry log %q", log)
			}
			live := add("sleep 10", "edit", "queued")
			liveTask := await(live, "running")
			e.Command = "printf 'after-stop\\n'"
			e.Stashed = true
			stopped := run(native, core.Request{Operation: "stop-edit-restart", IDs: []int{live}, InPlace: true, Guards: map[int]time.Time{live: liveTask.CreatedAt}, Edit: &e})
			if *stopped.TaskID != live || get(live).State() != "stashed" {
				t.Fatal("stop/edit did not stage task")
			}
			values, err := c.Logs(ctx, native, []int{id, *copy.TaskID, live, 999999}, 30)
			if err != nil {
				t.Fatal(err)
			}
			if values[id].Task == nil || !strings.Contains(values[id].Output, "in-place") || values[999999].Error == "" {
				t.Fatalf("bad batch: %+v", values)
			}
			// A never-run member must not hide good logs in native batches.
			never := add("echo no", "edit", "stashed")
			values, err = c.Logs(ctx, native, []int{id, never}, 30)
			if err != nil || values[id].Error != "" || values[never].Error == "" {
				t.Fatalf("batch isolation: %+v %v", values, err)
			}

			// A cross-group dependent protects the parent; no cascade occurs.
			run(local, core.Request{Operation: "group-add", Group: "clear", Parallel: 1})
			parent := add("echo parent", "clear", "stashed")
			child := add("echo child", "default", "stashed", parent)
			p := get(parent)
			result, err := c.Execute(ctx, native, core.Request{Operation: "clear-group", Group: "clear", IDs: []int{parent}, Guards: map[int]time.Time{parent: p.CreatedAt}})
			if err == nil || len(result.Outcomes) != 1 || result.Outcomes[0].Error == "" {
				t.Fatalf("clear ignored external dependency: %+v %v", result, err)
			}
			if get(child).ID != child || get(parent).ID != parent {
				t.Fatal("clear cascaded")
			}
			run(native, core.Request{Operation: "remove", IDs: []int{child}})
			run(native, core.Request{Operation: "group-start", Group: "clear"})
			run(native, core.Request{Operation: "enqueue", IDs: []int{parent}})
			await(parent, "succeeded")
			running := add("sleep 10", "clear", "queued")
			await(running, "running")
			descendant := add("echo descendant", "clear", "queued", running)
			before, err := c.Snapshot(ctx, native)
			if err != nil {
				t.Fatal(err)
			}
			clearReq := core.Request{Operation: "clear-group", Group: "clear", Guards: map[int]time.Time{}}
			for _, task := range before.Tasks {
				if task.Group == "clear" {
					clearReq.IDs = append(clearReq.IDs, task.ID)
					clearReq.Guards[task.ID] = task.CreatedAt
				}
			}
			newcomer := add("echo keep-me", "clear", "stashed")
			run(native, clearReq)
			remaining, err := c.Snapshot(ctx, native)
			if err != nil {
				t.Fatal(err)
			}
			for _, old := range []int{parent, running, descendant} {
				if _, exists := taskAt(remaining, old); exists {
					t.Fatalf("captured task %d remained", old)
				}
			}
			if get(newcomer).State() != "stashed" {
				t.Fatal("new task was removed or started")
			}
			for _, group := range remaining.Groups {
				if group.Name == "clear" && group.Status != "running" {
					t.Fatal("group status not restored")
				}
			}
		})
	}
}

func TestIsolatedSSHControlledEditorAndRecovery(t *testing.T) {
	requireIntegration(t)
	c, local, native, dir := isolatedDaemon(t, "unix")
	ctx := context.Background()
	pueueBinary, err := exec.LookPath("pueue")
	if err != nil {
		t.Fatal(err)
	}
	fakeDir := t.TempDir()
	// A transport fixture executes the remote command in a real POSIX shell.
	// It never contacts SSH, credentials, or a user-owned daemon.
	ssh := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ] && [ \"$1\" != -- ]; do shift; done\n[ \"$#\" -gt 0 ] || exit 0\nshift\n[ \"$#\" -gt 0 ] || exit 0\nshift\n[ \"$#\" -gt 0 ] || exit 0\nexec sh -c \"$1\"\n"
	if err = os.WriteFile(filepath.Join(fakeDir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LAZYPUEUE_EDIT_FIXTURE_ENV", "original-environment")
	added, err := c.Execute(ctx, local, core.Request{Operation: "add", Add: &core.AddRequest{Command: "printf 'original\\n'", Directory: dir, Mode: "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	id := *added.TaskID
	snap, _ := c.Snapshot(ctx, native)
	task, _ := taskAt(snap, id)
	if _, err = c.waitTask(ctx, native, id, task.CreatedAt, func(t core.Task) bool { return t.Terminal() }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LAZYPUEUE_EDIT_FIXTURE_ENV", "changed-client-environment")
	remote := core.Connection{ID: "ssh-fixture", Kind: "ssh", SSHHost: "fixture", Binary: pueueBinary, ConfigPath: local.ConfigPath}
	e := core.EditRequest{Command: "printf '%s\\n' \"$LAZYPUEUE_EDIT_FIXTURE_ENV\"", Directory: dir}
	retried, err := c.Execute(ctx, remote, core.Request{Operation: "edit-restart", IDs: []int{id}, InPlace: true, Guards: map[int]time.Time{id: task.CreatedAt}, Edit: &e})
	if err != nil {
		t.Fatal(err)
	}
	if *retried.TaskID != id {
		t.Fatal("wrong retry ID")
	}
	if _, err = c.waitTask(ctx, native, id, task.CreatedAt, func(t core.Task) bool { return t.Terminal() }); err != nil {
		t.Fatal(err)
	}
	output, err := c.Log(ctx, native, id, 30)
	if err != nil || !strings.Contains(output, "original-environment") {
		t.Fatalf("environment not preserved: %q %v", output, err)
	}
	staged, err := c.Execute(ctx, local, core.Request{Operation: "add", Add: &core.AddRequest{Command: "echo staged", Directory: dir, Mode: "stashed"}})
	if err != nil {
		t.Fatal(err)
	}
	lockedID := *staged.TaskID
	before, _ := c.Snapshot(ctx, native)
	lockedTask, _ := taskAt(before, lockedID)
	reader, writer := io.Pipe()
	editor := exec.Command(pueueBinary, "--config", local.ConfigPath, "edit", strconv.Itoa(lockedID))
	editor.Env = append(os.Environ(), "EDITOR=sh -c 'cat >/dev/null' --")
	editor.Stdin = reader
	if err = editor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		writer.Close()
		reader.Close()
		if editor.Process != nil {
			_ = editor.Process.Kill()
		}
		_ = editor.Wait()
	}()
	if _, err = c.waitTask(ctx, native, lockedID, lockedTask.CreatedAt, func(t core.Task) bool { return t.Locked }); err != nil {
		t.Fatal(err)
	}
	result, err := c.Execute(ctx, native, core.Request{Operation: "recover-stash", IDs: []int{lockedID}, Guards: map[int]time.Time{lockedID: lockedTask.CreatedAt}})
	if err != nil {
		t.Fatalf("recover: %+v %v", result, err)
	}
	writer.Close()
	if _, err = c.waitTask(ctx, native, lockedID, lockedTask.CreatedAt, func(t core.Task) bool { return !t.Locked && t.State() == "stashed" }); err != nil {
		t.Fatal(err)
	}
	t.Log(fmt.Sprintf("SSH fixture edit kept original env; recovered locked task #%d without starting it", lockedID))
}
