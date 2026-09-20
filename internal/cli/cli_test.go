package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
)

type fakeBackend struct {
	mu                      sync.Mutex
	reads, previews, writes int
	request                 core.Request
	connection              core.Connection
	failedConnection        string
	snapshotOverride        *core.Snapshot
}

func (f *fakeBackend) Snapshot(ctx context.Context, c core.Connection) (core.Snapshot, error) {
	f.mu.Lock()
	f.reads++
	f.mu.Unlock()
	if c.ID == f.failedConnection {
		return core.Snapshot{}, errors.New("offline")
	}
	if f.snapshotOverride != nil {
		return *f.snapshotOverride, nil
	}
	return core.Snapshot{ConnectionID: c.ID, Tasks: []core.Task{{ID: 3, Status: "Done", Result: "Success", Command: "echo done", Group: "default"}}, Groups: []core.Group{{Name: "default", Status: "Running", Parallel: 0}}}, nil
}
func (f *fakeBackend) Log(context.Context, core.Connection, int, int) (string, error) {
	f.reads++
	return "job output\n", nil
}
func (f *fakeBackend) Follow(context.Context, core.Connection, int, int, func(string)) error {
	f.reads++
	return nil
}
func (f *fakeBackend) Preview(c core.Connection, r core.Request) (core.Plan, error) {
	f.previews++
	f.request = r
	f.connection = c
	return core.Plan{ConnectionID: c.ID, Transport: c.Kind, Display: "pueue " + r.Operation}, nil
}
func (f *fakeBackend) Execute(ctx context.Context, c core.Connection, r core.Request) (core.Result, error) {
	f.writes++
	f.request = r
	f.connection = c
	id := 5
	return core.Result{ConnectionID: c.ID, TaskID: &id, Message: "done"}, nil
}

func setup(t *testing.T) *fakeBackend {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("LAZYPUEUE_CONFIG", "")
	return &fakeBackend{}
}
func invoke(t *testing.T, backend *fakeBackend, tty bool, args ...string) (string, string, error) {
	t.Helper()
	var out, errout bytes.Buffer
	root := NewRoot(Options{Backend: backend, Input: strings.NewReader(""), Output: &out, ErrorOutput: &errout, IsTerminal: func() bool { return tty }, Version: "test"})
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return out.String(), errout.String(), err
}

func TestEntryPolicy(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		tty      bool
		wantCode int
		contains string
	}{
		{"root pipe", nil, false, 0, "Available Commands"},
		{"json root", []string{"--json"}, false, 2, "requires a data command"},
		{"partial add", []string{"add", "--label", "provided"}, true, 2, "command is required"},
		{"bare add pipe", []string{"add"}, false, 2, "command is required"},
		{"interactive pipe", []string{"add", "--interactive"}, false, 2, "requires an input and output terminal"},
		{"interactive json", []string{"add", "--interactive", "--json"}, true, 2, "cannot be combined"},
		{"invalid flag", []string{"add", "--interactive", "--direktory", "x"}, true, 2, "unknown flag"},
		{"invalid mode", []string{"add", "--interactive", "--mode", "broken"}, true, 2, "start mode"},
		{"invalid dependency", []string{"add", "--interactive", "--after", "wrong"}, true, 2, "invalid dependency"},
		{"invalid connection kind", []string{"connections", "add", "--interactive", "--kind", "http"}, true, 2, "--kind"},
		{"invalid connection ID", []string{"connections", "add", "invalid!", "--interactive"}, true, 2, "ID must"},
		{"invalid native host", []string{"connections", "add", "--kind", "native", "--host", "not/a/host", "--interactive"}, true, 2, "--host"},
		{"business connection", []string{"connections", "add", "--name", "Lab"}, true, 2, "ID must"},
		{"json group", []string{"group", "--json"}, false, 2, "choose a subcommand"},
		{"unknown command", []string{"unrecognized"}, false, 2, "does not accept arguments"},
		{"json follow", []string{"follow", "3", "--json"}, false, 2, "cannot be combined"},
		{"read dry run", []string{"status", "--dry-run"}, false, 2, "read-only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			out, _, err := invoke(t, f, tc.tty, tc.args...)
			if ExitCode(err) != tc.wantCode {
				t.Fatalf("code=%d err=%v output=%s", ExitCode(err), err, out)
			}
			text := out
			if err != nil {
				text += err.Error()
			}
			if !strings.Contains(text, tc.contains) {
				t.Errorf("missing %q: %s", tc.contains, text)
			}
			if f.reads+f.writes+f.previews != 0 {
				t.Errorf("entry validation called backend: %+v", f)
			}
		})
	}
}

func TestHelpDoesNotLoadBrokenConfiguration(t *testing.T) {
	f := setup(t)
	missing := filepath.Join(t.TempDir(), "missing.toml")
	out, _, err := invoke(t, f, false, "--config", missing, "add", "--help")
	if err != nil || !strings.Contains(out, "Start mode") {
		t.Fatalf("help failed: %v %s", err, out)
	}
	_, _, err = invoke(t, f, false, "--config", missing, "status")
	if ExitCode(err) != 2 {
		t.Fatalf("explicit missing config: %v", err)
	}
}

func TestDryRunIsPure(t *testing.T) {
	f := setup(t)
	out, _, err := invoke(t, f, false, "add", "--json", "--dry-run", "--", "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	var plan core.Plan
	if json.Unmarshal([]byte(out), &plan) != nil || plan.ConnectionID != "local" {
		t.Fatalf("not plan JSON: %s", out)
	}
	if f.previews != 1 || f.reads != 0 || f.writes != 0 {
		t.Fatalf("backend calls: %+v", f)
	}
	path, _ := config.ResolvePath("")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run created config")
	}
	state, _ := config.StatePath()
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run created state")
	}
}

func TestBatchDryRunsAndGroupListing(t *testing.T) {
	for _, args := range [][]string{{"clean", "--dry-run", "--json"}, {"restart-failed", "--group", "ml", "--dry-run", "--json"}, {"group", "remove", "ml", "--dry-run", "--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := setup(t)
			out, _, err := invoke(t, f, false, args...)
			if err != nil || !json.Valid([]byte(out)) {
				t.Fatalf("plan: %v %s", err, out)
			}
			if f.reads != 0 || f.writes != 0 || f.previews != 1 {
				t.Fatalf("dry-run backend calls: %+v", f)
			}
		})
	}
	t.Run("all groups", func(t *testing.T) {
		f := setup(t)
		out, _, err := invoke(t, f, false, "group", "list", "--connection", "all", "--json")
		if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"connections"`) {
			t.Fatalf("groups: %v %s", err, out)
		}
	})
	t.Run("group list dry-run", func(t *testing.T) {
		f := setup(t)
		_, _, err := invoke(t, f, false, "group", "list", "--dry-run")
		if ExitCode(err) != 2 || f.reads != 0 {
			t.Fatalf("dry-run read: %v %+v", err, f)
		}
	})
}

func TestMutationConfirmationAndRestartSemantics(t *testing.T) {
	for _, args := range [][]string{{"kill", "3"}, {"remove", "3"}, {"start", "3"}, {"restart", "3", "--in-place"}, {"clean"}, {"group", "remove", "ml"}, {"parallel", "0"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := setup(t)
			out, _, err := invoke(t, f, false, args...)
			if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--yes") || f.writes != 0 || out != "" {
				t.Fatalf("unguarded mutation: %v %s %+v", err, out, f)
			}
			_, _, err = invoke(t, f, false, append(args, "--yes")...)
			if err != nil || f.writes != 1 {
				t.Fatalf("approved mutation: %v %+v", err, f)
			}
		})
	}
	t.Run("new ID restart", func(t *testing.T) {
		f := setup(t)
		_, _, err := invoke(t, f, false, "restart", "3", "--yes")
		if err != nil || f.request.InPlace || f.writes != 1 {
			t.Fatalf("restart: %v %+v", err, f)
		}
	})
	t.Run("all blocks writes", func(t *testing.T) {
		f := setup(t)
		_, _, err := invoke(t, f, false, "--connection", "all", "kill", "3", "--yes")
		if ExitCode(err) != 2 || f.writes != 0 {
			t.Fatalf("all mutation: %v", err)
		}
	})
}

func TestStructuredStatusAndLog(t *testing.T) {
	f := setup(t)
	out, stderr, err := invoke(t, f, false, "status", "--connection", "local", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("status: %v %s", err, stderr)
	}
	var s core.Snapshot
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	if s.Groups[0].Parallel != 0 || s.Tasks[0].ID != 3 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	out, _, err = invoke(t, f, false, "log", "3", "--json")
	if err != nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"output": "job output\n"`) {
		t.Fatalf("log: %v %s", err, out)
	}
}

func TestVersionFlagHonorsJSON(t *testing.T) {
	f := setup(t)
	out, stderr, err := invoke(t, f, false, "--json", "--version")
	if err != nil || stderr != "" || !json.Valid([]byte(out)) {
		t.Fatalf("version: %v %s %s", err, out, stderr)
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(out), &value); err != nil || value["version"] != "test" {
		t.Fatalf("version payload: %v %s", err, out)
	}
}

func TestAllStatusIsolatesFailure(t *testing.T) {
	f := setup(t)
	cfg := config.Default()
	cfg.Connections = append(cfg.Connections, core.Connection{ID: "lab", Kind: "ssh", SSHHost: "lab"})
	if err := config.Save("", cfg); err != nil {
		t.Fatal(err)
	}
	f.failedConnection = "lab"
	out, _, err := invoke(t, f, false, "status", "--connection", "all", "--json")
	if ExitCode(err) != 1 || !json.Valid([]byte(out)) {
		t.Fatalf("status all: %v %s", err, out)
	}
	var result struct {
		Connections []connectionStatus `json:"connections"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Connections) != 2 || result.Connections[0].Snapshot == nil || result.Connections[1].Error != "offline" {
		t.Fatalf("missing per-connection result: %s", out)
	}
}

func TestAddJSONAndRecentDefaults(t *testing.T) {
	f := setup(t)
	out, _, err := invoke(t, f, false, "add", "--json", "--directory", "/tmp", "--group", "ml", "--label", "中文", "--after", "1,2", "--", "printf '%s' 'j q /'")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("add: %v %s", err, out)
	}
	if f.request.Add == nil || f.request.Add.Command != "printf '%s' 'j q /'" || len(f.request.Add.After) != 2 {
		t.Fatalf("command changed: %+v", f.request.Add)
	}
	state, err := config.LoadState()
	if err != nil || state.LastUsed["local"].Group != "ml" {
		t.Fatalf("missing recent defaults: %v %+v", err, state)
	}
}

func TestConnectionsAddUsesExplicitConfigAndNoProbe(t *testing.T) {
	f := setup(t)
	path := filepath.Join(t.TempDir(), "settings.toml")
	if err := os.WriteFile(path, []byte("# keep my comment\ndefault_connection = 'all'\n[tui]\nrefresh_seconds=3\nbackground_seconds=5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, _, err := invoke(t, f, false, "--config", path, "connections", "add", "lab", "--kind", "ssh", "--ssh-host", "lab-alias", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("connections: %v %s", err, out)
	}
	if f.reads+f.writes+f.previews != 0 {
		t.Fatal("saving connection made network probes")
	}
	data, _ := os.ReadFile(path)
	if !bytes.Contains(data, []byte("# keep my comment")) {
		t.Fatal("comment lost")
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cfg.Connection("lab")
	if err != nil || c.SSHHost != "lab-alias" {
		t.Fatalf("connection not saved: %v %+v", err, c)
	}
}

func TestConnectionsDryRunDoesNotSave(t *testing.T) {
	f := setup(t)
	out, _, err := invoke(t, f, false, "connections", "add", "lab", "--kind", "ssh", "--ssh-host", "lab", "--dry-run", "--json")
	if err != nil || !json.Valid([]byte(out)) {
		t.Fatalf("connection plan: %v %s", err, out)
	}
	path, _ := config.ResolvePath("")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created config in dry run")
	}
}

func TestEditorArgumentsAreNotShellEvaluated(t *testing.T) {
	args, err := parseEditor(`code --wait 'a b' "$(touch should-not-exist)"`)
	if err != nil || len(args) != 4 || args[2] != "a b" || args[3] != "$(touch should-not-exist)" {
		t.Fatalf("args=%q err=%v", args, err)
	}
	if _, err := parseEditor(`code "unclosed`); err == nil {
		t.Fatal("accepted invalid quotes")
	}
}

func TestExecuteStructuredErrorsUseParsedJSONFlag(t *testing.T) {
	cases := []struct {
		args []string
		json bool
	}{
		{[]string{"add", "--json", "--mode", "invalid"}, true},
		{[]string{"--json", "add", "--unknown"}, true},
		{[]string{"--json=false", "kill", "bad"}, false},
		{[]string{"kill", "--", "--json"}, false},
		{[]string{"add", "--connection", "all", "--command", "--json"}, false},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			f := setup(t)
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), Options{Backend: f, Input: strings.NewReader(""), Output: &stdout, ErrorOutput: &stderr, IsTerminal: func() bool { return false }}, tc.args)
			if code != 2 || stdout.Len() != 0 {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			if tc.json {
				var result struct {
					Error    string `json:"error"`
					ExitCode int    `json:"exit_code"`
				}
				if err := json.Unmarshal(stderr.Bytes(), &result); err != nil || result.ExitCode != 2 || result.Error == "" {
					t.Fatalf("invalid structured error: %v %s", err, &stderr)
				}
			} else if !strings.HasPrefix(stderr.String(), "lazypueue: ") {
				t.Fatalf("unexpected error format: %s", &stderr)
			}
			if strings.Count(stderr.String(), "\n") != 1 {
				t.Fatalf("error printed more than once: %s", &stderr)
			}
		})
	}
}

func TestInteractiveMutationReviewFreezesIDs(t *testing.T) {
	created := time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)
	for _, args := range [][]string{{"remove", "3"}, {"clean"}, {"restart-failed"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := setup(t)
			result := "Success"
			if args[0] == "restart-failed" {
				result = "Failed"
			}
			f.snapshotOverride = &core.Snapshot{Tasks: []core.Task{{ID: 3, CreatedAt: created, Status: "Done", Result: result, Command: "reviewed command", Group: "default"}}}
			var out, errout bytes.Buffer
			root := NewRoot(Options{Backend: f, Input: strings.NewReader("yes\n"), Output: &out, ErrorOutput: &errout, IsTerminal: func() bool { return true }})
			root.SetArgs(args)
			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if f.reads != 1 || f.writes != 1 || len(f.request.IDs) != 1 || f.request.IDs[0] != 3 || !f.request.Guards[3].Equal(created) {
				t.Fatalf("missing exact review guards: %+v %+v", f, f.request)
			}
			if !strings.Contains(errout.String(), "#3") || !strings.Contains(errout.String(), "reviewed command") {
				t.Fatalf("review missing target: %s", &errout)
			}
		})
	}
	t.Run("empty clean cannot expand after review", func(t *testing.T) {
		f := setup(t)
		f.snapshotOverride = &core.Snapshot{Tasks: []core.Task{}}
		var out bytes.Buffer
		root := NewRoot(Options{Backend: f, Input: strings.NewReader("yes\n"), Output: &out, ErrorOutput: &bytes.Buffer{}, IsTerminal: func() bool { return true }})
		root.SetArgs([]string{"clean"})
		if err := root.ExecuteContext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.writes != 0 || !strings.Contains(out.String(), "No matching tasks") {
			t.Fatalf("empty clean submitted: writes=%d output=%s", f.writes, &out)
		}
	})
}

func TestWizardRetainsPartialEffectFailure(t *testing.T) {
	f := setup(t)
	model := form.NewTask(config.Default().Connections, core.AddRequest{Command: "echo retained", Directory: "/tmp", Group: "jobs", CreateGroup: true}, "local", f)
	model.Done = true
	host := &wizardHost{task: model, pending: true}
	host.Update(wizardResultMsg{result: core.Result{Message: "Created group jobs; task was not submitted"}, err: errors.New("submission failed")})
	if model.Done || model.Err == nil || !strings.Contains(model.Err.Error(), "Created group jobs") || !strings.Contains(model.Err.Error(), "submission failed") || model.Request.Command != "echo retained" {
		t.Fatalf("partial effect lost: %+v", model)
	}
	if host.pending {
		t.Fatal("form stayed pending after failure")
	}
}
