package pueue

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

var _ core.Backend = (*Client)(nil)

func (c *Client) Snapshot(ctx context.Context, conn core.Connection) (core.Snapshot, error) {
	data, err := c.run(ctx, conn, []string{"status", "--json"}, false, false)
	if err != nil {
		return core.Snapshot{}, err
	}
	return ParseSnapshot(data, conn.ID, time.Now())
}

func (c *Client) Log(ctx context.Context, conn core.Connection, id, lines int) (string, error) {
	entries, err := c.Logs(ctx, conn, []int{id}, lines)
	if err != nil {
		return "", err
	}
	result := entries[id]
	if result.Error != "" {
		return "", &Error{Kind: "log-unavailable", Detail: result.Error}
	}
	return result.Output, nil
}

type ProbeResult struct {
	Version   string `json:"version"`
	Major     int    `json:"major"`
	Minor     int    `json:"minor"`
	Patch     int    `json:"patch"`
	Reachable bool   `json:"reachable"`
}

var versionRE = regexp.MustCompile(`(?i)pueue\s+(\d+)\.(\d+)\.(\d+)`)

func (c *Client) version(ctx context.Context, conn core.Connection) (ProbeResult, error) {
	data, err := c.run(ctx, conn, []string{"--version"}, false, false)
	if err != nil {
		return ProbeResult{}, err
	}
	match := versionRE.FindStringSubmatch(string(data))
	if match == nil {
		return ProbeResult{}, fmt.Errorf("could not determine the Pueue client version")
	}
	p := ProbeResult{Version: strings.TrimSpace(string(data))}
	p.Major, _ = strconv.Atoi(match[1])
	p.Minor, _ = strconv.Atoi(match[2])
	p.Patch, _ = strconv.Atoi(match[3])
	if p.Major != 4 {
		return p, fmt.Errorf("Pueue 4.x is required; found %s", p.Version)
	}
	return p, nil
}

func (c *Client) Probe(ctx context.Context, conn core.Connection) (ProbeResult, error) {
	p, err := c.version(ctx, conn)
	if err != nil {
		return p, err
	}
	_, err = c.Snapshot(ctx, conn)
	p.Reachable = err == nil
	return p, err
}

// ArgsFor represents mutation intent as argv. The command is a single argument
// after --, allowing Pueue's configured shell to interpret it exactly once.
func ArgsFor(req core.Request) ([]string, error) {
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	ids := make([]string, len(req.IDs))
	for i, id := range req.IDs {
		ids[i] = strconv.Itoa(id)
	}
	switch req.Operation {
	case "add":
		a := req.Add
		args := []string{"add", "--print-task-id", "--working-directory=" + a.Directory}
		if a.Group != "" {
			args = append(args, "--group="+a.Group)
		}
		if a.Label != "" {
			args = append(args, "--label="+a.Label)
		}
		if a.Priority != 0 {
			args = append(args, "--priority="+strconv.Itoa(a.Priority))
		}
		for _, id := range a.After {
			args = append(args, "--after", strconv.Itoa(id))
		}
		if a.Delay != "" {
			args = append(args, "--delay="+a.Delay)
		}
		switch a.Mode {
		case "stashed":
			args = append(args, "--stashed")
		case "immediate":
			args = append(args, "--immediate")
		}
		return append(args, "--", a.Command), nil
	case "edit":
		return append([]string{"edit"}, ids...), nil
	case "edit-restart", "stop-edit-restart":
		mode := "--not-in-place"
		if req.InPlace {
			mode = "--in-place"
		}
		return append([]string{"restart", mode, "--edit", "--stashed"}, ids...), nil
	case "recover-stash":
		return append([]string{"stash"}, ids...), nil
	case "clear-group":
		return []string{"pause", "--wait", "--group=" + req.Group}, nil
	case "restart", "restart-failed":
		args := []string{"restart", "--not-in-place"}
		if req.InPlace {
			args[1] = "--in-place"
		}
		if len(ids) == 0 && req.Operation == "restart-failed" {
			if req.Group != "" {
				return append(args, "--failed-in-group="+req.Group), nil
			}
			return append(args, "--all-failed"), nil
		}
		return append(args, ids...), nil
	case "pause", "start", "stash", "enqueue", "kill", "remove":
		return append([]string{req.Operation}, ids...), nil
	case "clean":
		if len(ids) > 0 {
			return append([]string{"remove"}, ids...), nil
		}
		args := []string{"clean"}
		if req.SuccessfulOnly {
			args = append(args, "--successful-only")
		}
		if req.Group != "" {
			args = append(args, "--group="+req.Group)
		}
		return args, nil
	case "group-add":
		return []string{"group", "add", "--parallel", strconv.Itoa(req.Parallel), "--", req.Group}, nil
	case "group-remove":
		return []string{"group", "remove", "--", req.Group}, nil
	case "group-pause":
		return []string{"pause", "--group=" + req.Group}, nil
	case "group-start":
		return []string{"start", "--group=" + req.Group}, nil
	case "parallel":
		return []string{"parallel", strconv.Itoa(req.Parallel), "--group=" + req.Group}, nil
	}
	return nil, fmt.Errorf("unsupported operation %q", req.Operation)
}

func validateRequest(req core.Request) error {
	if strings.ContainsRune(req.Group, 0) {
		return fmt.Errorf("group name cannot contain NUL bytes")
	}
	for _, id := range req.IDs {
		if id < 0 {
			return fmt.Errorf("task IDs cannot be negative")
		}
	}
	switch req.Operation {
	case "edit", "edit-restart", "stop-edit-restart":
		if len(req.IDs) != 1 || req.Edit == nil {
			return fmt.Errorf("edit requires one task and an edit draft")
		}
		return core.ValidateEdit(*req.Edit)
	case "recover-stash":
		if len(req.IDs) != 1 {
			return fmt.Errorf("recover-stash requires one task")
		}
	case "clear-group":
		if strings.TrimSpace(req.Group) == "" {
			return fmt.Errorf("clear-group requires a group")
		}
	case "add":
		if req.Add == nil {
			return fmt.Errorf("add request is missing")
		}
		return core.ValidateAdd(*req.Add)
	case "pause", "start", "restart", "stash", "enqueue", "kill", "remove":
		if len(req.IDs) == 0 {
			return fmt.Errorf("%s requires at least one task ID", req.Operation)
		}
	case "clean", "restart-failed":
	case "group-add", "group-remove", "group-pause", "group-start", "parallel":
		if strings.TrimSpace(req.Group) == "" {
			return fmt.Errorf("a group name is required")
		}
		if req.Parallel < 0 {
			return fmt.Errorf("parallelism cannot be negative")
		}
		if req.Operation == "group-remove" && req.Group == "default" {
			return fmt.Errorf("the default group cannot be removed")
		}
	default:
		return fmt.Errorf("unsupported operation %q", req.Operation)
	}
	return nil
}

func (c *Client) Preview(conn core.Connection, req core.Request) (core.Plan, error) {
	if err := validateConnection(conn); err != nil {
		return core.Plan{}, err
	}
	if conn.Kind == "native" && req.Operation == "add" && conn.SSHHost == "" {
		return core.Plan{}, fmt.Errorf("configure an SSH submission companion for this native connection before adding tasks")
	}
	args, err := ArgsFor(req)
	if err != nil {
		return core.Plan{}, err
	}
	plan := core.Plan{ConnectionID: conn.ID, Transport: conn.Kind}
	if plan.Transport == "" {
		plan.Transport = "local"
	}
	if conn.Kind == "ssh" || (conn.Kind == "native" && req.Operation == "add" && conn.SSHHost != "") {
		binary := conn.SSHBinary
		if binary == "" && conn.Kind == "ssh" {
			binary = conn.Binary
		}
		if binary == "" {
			binary = "pueue"
		}
		remote := remotePath(binary) + " --color never"
		config, profile := remoteConfig(conn)
		if config != "" {
			remote += " --config " + remotePath(config)
		}
		if profile != "" {
			remote += " --profile " + shellQuote(profile)
		}
		remote += " " + remoteJoin(args)
		plan.Display = "ssh " + shellQuote(conn.SSHHost) + " " + shellQuote(remote)
		plan.Transport = "ssh"
	} else {
		binary := conn.Binary
		if binary == "" {
			binary = "pueue"
		}
		display := []string{binary, "--color", "never"}
		if conn.ConfigPath != "" {
			display = append(display, "--config", conn.ConfigPath)
		}
		if conn.Profile != "" {
			display = append(display, "--profile", conn.Profile)
		}
		plan.Display = shellJoin(append(display, args...))
	}
	switch req.Operation {
	case "edit", "edit-restart", "stop-edit-restart":
		e := req.Edit
		display := []string{"lazypueue", "--connection", conn.ID, "edit", strconv.Itoa(req.IDs[0]), "--command=" + e.Command, "--directory=" + e.Directory, "--label=" + e.Label, "--priority=" + strconv.Itoa(e.Priority), "--stashed=" + strconv.FormatBool(e.Stashed)}
		if req.InPlace {
			display = append(display, "--in-place")
		}
		plan.Display = shellJoin(display)
		plan.Consequences = append(plan.Consequences, "Applies the reviewed command, directory, label and priority; preserves the existing environment and group.")
		if req.Operation == "stop-edit-restart" {
			plan.Consequences = append(plan.Consequences, "Stops the current process before editing and retrying; cancellation cannot undo that stop.")
		}
		if req.Operation != "edit" {
			if req.InPlace {
				plan.Consequences = append(plan.Consequences, "Reuses the task ID and overwrites its log on the next run.")
			} else {
				plan.Consequences = append(plan.Consequences, "Creates a new task, keeps the original log and clears dependencies.")
			}
		}
		if req.Edit.Stashed {
			plan.Consequences = append(plan.Consequences, "Leaves the edited task stashed; it will not run automatically.")
		} else {
			plan.Consequences = append(plan.Consequences, "Enqueues the edited task after saving; it may start when its group has capacity.")
		}
	case "recover-stash":
		plan.Consequences = append(plan.Consequences, "Releases this edit lock to stash; the original edit session will no longer be able to save.")
	case "clear-group":
		plan.Display = "lazypueue --connection " + shellQuote(conn.ID) + " group clear " + shellQuote(req.Group)
		plan.Consequences = append(plan.Consequences, "Stops and removes only the reviewed tasks and their logs; no reset or dependency cascade.", "Temporarily pauses scheduling; restores its previous state on success. Failure leaves it paused.")
	case "restart", "restart-failed":
		if req.InPlace {
			plan.Consequences = append(plan.Consequences, "Reuses task IDs and overwrites their existing logs.")
		} else {
			plan.Consequences = append(plan.Consequences, "Creates new tasks; keeps the original tasks and logs.")
		}
	case "remove", "clean":
		plan.Consequences = append(plan.Consequences, "Removes the selected task records and their logs.")
	case "kill":
		plan.Consequences = append(plan.Consequences, "Stops the selected task processes.")
	case "group-remove":
		plan.Consequences = append(plan.Consequences, "Removes an empty group. Existing tasks must be handled separately.")
	case "add":
		if req.Add.CreateGroup {
			plan.Consequences = append(plan.Consequences, fmt.Sprintf("Creates group %q first; it remains if task submission fails.", req.Add.Group))
		}
		if plan.Transport == "ssh" {
			plan.Consequences = append(plan.Consequences, "Uses the SSH host's noninteractive environment and working directory.")
		}
		if conn.Kind == "native" && plan.Transport != "ssh" {
			plan.Consequences = append(plan.Consequences, "Uses the local Pueue client's environment. Working directory must exist on the daemon host.")
		}
		if req.Add.Mode == "immediate" {
			plan.Consequences = append(plan.Consequences, "Starts immediately, ignoring the group's parallelism limit.")
		}
	}
	return plan, nil
}

func (c *Client) Execute(ctx context.Context, conn core.Connection, req core.Request) (core.Result, error) {
	result := core.Result{ConnectionID: conn.ID, Outcomes: []core.Outcome{}}
	if err := validateRequest(req); err != nil {
		return result, err
	}
	if conn.Kind == "native" && req.Operation == "add" && conn.SSHHost == "" {
		return result, fmt.Errorf("configure an SSH submission companion for this native connection before adding tasks")
	}
	snapshot, err := c.Snapshot(ctx, conn)
	if err != nil {
		return result, err
	}
	// Dependency IDs and selected task IDs share the same daemon ID namespace.
	// Revalidate every captured identity before any write, including group creation.
	for id, expected := range req.Guards {
		found := false
		for _, task := range snapshot.Tasks {
			if task.ID == id && task.CreatedAt.Equal(expected) {
				found = true
				break
			}
		}
		if !found {
			return result, &Error{Kind: "stale-selection", Detail: fmt.Sprintf("task #%d disappeared or its ID was reused; review the selection again", id)}
		}
	}
	if req.Operation == "add" {
		return c.add(ctx, conn, req, snapshot)
	}
	switch req.Operation {
	case "edit", "edit-restart", "stop-edit-restart", "recover-stash":
		return c.editTask(ctx, conn, req, snapshot)
	case "clear-group":
		return c.clearGroup(ctx, conn, req, snapshot)
	}
	groups := map[string]bool{}
	for _, g := range snapshot.Groups {
		groups[g.Name] = true
	}
	if req.Operation == "group-add" && groups[req.Group] {
		return result, fmt.Errorf("group %q already exists", req.Group)
	}
	if req.Operation != "group-add" && req.Group != "" && !groups[req.Group] {
		return result, fmt.Errorf("group %q no longer exists", req.Group)
	}
	tasks := map[int]core.Task{}
	for _, t := range snapshot.Tasks {
		tasks[t.ID] = t
		if req.Operation == "group-remove" && t.Group == req.Group {
			return result, fmt.Errorf("group %q still contains tasks; remove or clean them explicitly before removing the group", req.Group)
		}
	}
	if (req.Operation == "clean" || req.Operation == "restart-failed") && len(req.IDs) == 0 {
		for _, t := range snapshot.Tasks {
			if req.Group != "" && t.Group != req.Group {
				continue
			}
			if req.Operation == "clean" && t.Terminal() && (!req.SuccessfulOnly || t.State() == "succeeded") {
				req.IDs = append(req.IDs, t.ID)
			}
			if req.Operation == "restart-failed" && t.Failed() {
				req.IDs = append(req.IDs, t.ID)
			}
		}
		if len(req.IDs) == 0 {
			result.Message = "No matching tasks"
			return result, nil
		}
	}
	seen := map[int]bool{}
	valid := make([]int, 0, len(req.IDs))
	for _, id := range req.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		t, ok := tasks[id]
		reason := ""
		if !ok {
			reason = "task no longer exists"
		} else if guard, ok := req.Guards[id]; ok && !t.CreatedAt.Equal(guard) {
			reason = "task ID was reused; select it again"
		} else if !core.Eligible(req.Operation, t) {
			reason = "task is no longer eligible for " + req.Operation
		} else if req.Operation == "start" && req.ResumeOnly && t.State() != "paused" {
			reason = "task is no longer paused; Resume will not force-start queued work"
		} else if req.Operation == "restart-failed" && !t.Failed() {
			reason = "task is no longer failed"
		} else if req.Operation == "clean" && req.SuccessfulOnly && t.State() != "succeeded" {
			reason = "task is not successful"
		}
		if reason != "" {
			result.Outcomes = append(result.Outcomes, core.Outcome{ID: id, Error: reason})
		} else {
			valid = append(valid, id)
		}
	}
	if len(result.Outcomes) > 0 {
		result.Message = "Tasks changed since review; nothing was submitted"
		return result, &Error{Kind: "stale-selection", Detail: result.Message}
	}
	req.IDs = valid
	sort.Ints(req.IDs)
	args, err := ArgsFor(req)
	if err != nil {
		return result, err
	}
	_, err = c.run(ctx, conn, args, true, false)
	if err != nil {
		var transportErr *Error
		if errors.As(err, &transportErr) {
			result.Unknown = transportErr.Unknown
		}
		return result, err
	}
	if req.Operation == "remove" || req.Operation == "clean" {
		guards := map[int]time.Time{}
		for _, id := range req.IDs {
			guards[id] = tasks[id].CreatedAt
		}
		result.Outcomes, err = c.reconcileRemoval(ctx, conn, req.IDs, guards)
		if err != nil {
			result.Unknown = true
			return result, &Error{Kind: "unknown-outcome", Detail: "Removal submitted but could not be reconciled; inspect the queue", Unknown: true, Cause: err}
		}
		if err = outcomeError(result.Outcomes); err != nil {
			result.Message = err.Error()
			return result, err
		}
	} else {
		for _, id := range req.IDs {
			result.Outcomes = append(result.Outcomes, core.Outcome{ID: id})
		}
	}
	result.Message = "Request accepted; refreshing daemon state"
	return result, nil
}

func (c *Client) add(ctx context.Context, conn core.Connection, req core.Request, snapshot core.Snapshot) (core.Result, error) {
	result := core.Result{ConnectionID: conn.ID, Outcomes: []core.Outcome{}}
	a := *req.Add
	if a.Group == "" {
		a.Group = "default"
	}
	if conn.Kind != "ssh" && conn.Kind != "native" {
		a.Directory = expandLocal(a.Directory)
	}
	if conn.Kind == "native" {
		remote := conn
		remote.Kind = "ssh"
		remote.Binary = conn.SSHBinary
		remote.ConfigPath = conn.SSHConfig
		remote.Profile = conn.SSHProfile
		other, err := c.Snapshot(ctx, remote)
		if err != nil {
			return result, fmt.Errorf("check SSH submission companion: %w", err)
		}
		if err = matchingSubmissionTargets(snapshot, other, a); err != nil {
			return result, err
		}
	}

	groupExists := false
	for _, g := range snapshot.Groups {
		if g.Name == a.Group {
			groupExists = true
		}
	}
	if !groupExists && !a.CreateGroup {
		return result, fmt.Errorf("group %q does not exist; choose Create Group", a.Group)
	}
	tasks := map[int]core.Task{}
	for _, t := range snapshot.Tasks {
		tasks[t.ID] = t
	}
	for _, id := range a.After {
		t, ok := tasks[id]
		if !ok {
			return result, fmt.Errorf("dependency #%d no longer exists", id)
		}
		if t.Failed() {
			return result, fmt.Errorf("dependency #%d has failed; this task would never run", id)
		}
	}
	if !groupExists {
		_, err := c.run(ctx, conn, []string{"group", "add", "--parallel", strconv.Itoa(a.GroupParallel), "--", a.Group}, true, false)
		if err != nil {
			var te *Error
			if errors.As(err, &te) {
				result.Unknown = te.Unknown
			}
			return result, err
		}
		result.Message = fmt.Sprintf("Created group %q. ", a.Group)
		if conn.Kind == "native" {
			remote := conn
			remote.Kind = "ssh"
			remote.Binary = conn.SSHBinary
			remote.ConfigPath = conn.SSHConfig
			remote.Profile = conn.SSHProfile
			other, err := c.Snapshot(ctx, remote)
			if err != nil {
				return result, fmt.Errorf("%sCannot verify the new group through SSH: %w", result.Message, err)
			}
			found := false
			for _, g := range other.Groups {
				if g.Name == a.Group {
					found = true
				}
			}
			if !found {
				return result, fmt.Errorf("%sThe SSH companion cannot see it; check that both connections reach the same daemon", result.Message)
			}
		}
	}
	req.Add = &a
	args, err := ArgsFor(req)
	if err != nil {
		return result, err
	}
	output, err := c.run(ctx, conn, args, true, true)
	if err != nil {
		var te *Error
		if errors.As(err, &te) {
			result.Unknown = te.Unknown
		}
		return result, err
	}
	id, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || id < 0 {
		result.Unknown = true
		return result, &Error{Kind: "unknown-outcome", Detail: "Pueue accepted submission but returned no task ID; refresh before retrying", Unknown: true}
	}
	result.TaskID = &id
	result.Message += fmt.Sprintf("Queued task #%d", id)
	return result, nil
}

// EditCommand hands Pueue's native lock/edit/restore flow to the user's editor.
// The caller owns terminal suspension and must run the returned command once.
func (c *Client) EditCommand(ctx context.Context, conn core.Connection, ids []int) (*exec.Cmd, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("select a queued or stashed task")
	}
	snapshot, err := c.Snapshot(ctx, conn)
	if err != nil {
		return nil, err
	}
	tasks := map[int]core.Task{}
	for _, t := range snapshot.Tasks {
		tasks[t.ID] = t
	}
	args := []string{"edit"}
	for _, id := range ids {
		t, ok := tasks[id]
		if !ok || !core.Eligible("edit", t) {
			return nil, fmt.Errorf("task #%d is not editable", id)
		}
		args = append(args, strconv.Itoa(id))
	}
	cmd, err := c.command(ctx, conn, args, false)
	if err == nil && conn.Kind == "ssh" {
		// The TUI has released terminal ownership before running this command.
		cmd.Args = append(cmd.Args[:1], append([]string{"-t"}, cmd.Args[1:]...)...)
	}
	return cmd, err
}

// Compare the referenced scope before an add workflow can modify either daemon.
func matchingSubmissionTargets(native, ssh core.Snapshot, a core.AddRequest) error {
	nativeGroups, sshGroups := map[string]bool{}, map[string]bool{}
	for _, g := range native.Groups {
		nativeGroups[g.Name] = true
	}
	for _, g := range ssh.Groups {
		sshGroups[g.Name] = true
	}
	if nativeGroups[a.Group] != sshGroups[a.Group] {
		return fmt.Errorf("native and SSH submission connections disagree about group %q; check companion config/profile", a.Group)
	}
	for _, id := range a.After {
		var left, right *core.Task
		for i := range native.Tasks {
			if native.Tasks[i].ID == id {
				left = &native.Tasks[i]
			}
		}
		for i := range ssh.Tasks {
			if ssh.Tasks[i].ID == id {
				right = &ssh.Tasks[i]
			}
		}
		if left == nil || right == nil || !left.CreatedAt.Equal(right.CreatedAt) || left.Command != right.Command {
			return fmt.Errorf("native and SSH submission connections disagree about dependency #%d; check companion config/profile", id)
		}
	}
	return nil
}
