package pueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"go.yaml.in/yaml/v3"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const created = "2026-09-01T01:02:03.123456+08:00"

func wireFixture(statuses ...string) []byte {
	tasks := map[string]any{}
	for id, status := range statuses {
		var value any
		if err := json.Unmarshal([]byte(status), &value); err != nil {
			panic(err)
		}
		tasks[fmt.Sprint(id)] = map[string]any{"id": id, "created_at": created, "command": "printf '中文 é 👩‍💻'", "original_command": "alias foo", "path": "/remote/project", "group": "default", "priority": 0, "envs": map[string]string{"TEST_SECRET": "never-retain-this"}, "status": value}
	}
	data, _ := json.Marshal(map[string]any{"tasks": tasks, "groups": map[string]any{"default": map[string]any{"status": "Running", "parallel_tasks": 1}}})
	return data
}

func TestParseV4VariantsDropsEnvironment(t *testing.T) {
	fixture := wireFixture(
		`{"Stashed":{"enqueue_at":null}}`,
		`{"Queued":{"enqueued_at":"2026-09-01T01:02:03+08:00"}}`,
		`{"Locked":{"previous_status":{"Locked":{"previous_status":{"Running":{"enqueued_at":"2026-09-01T01:02:03+08:00","start":"2026-09-01T01:03:03+08:00"}}}}}}`,
		`{"Done":{"result":"Success"}}`,
		`{"Done":{"result":{"Failed":127}}}`,
		`{"Done":{"result":{"FailedToSpawn":"missing remote cwd"}}}`,
		`{"Done":{"result":"DependencyFailed"}}`,
		`{"Future":{"anything":true}}`,
	)
	s, err := ParseSnapshot(fixture, "remote", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if s.ConnectionID != "remote" || len(s.Tasks) != 8 {
		t.Fatalf("bad snapshot: %+v", s)
	}
	if !s.Tasks[2].Locked || s.Tasks[2].State() != "running" {
		t.Fatalf("lost underlying lock state: %+v", s.Tasks[2])
	}
	if s.Tasks[0].ScheduledAt != nil {
		t.Fatal("null schedule converted to epoch")
	}
	if !s.Tasks[3].Terminal() || s.Tasks[3].Failed() {
		t.Fatal("success not recognized")
	}
	if *s.Tasks[4].ExitCode != 127 || !s.Tasks[5].Failed() || s.Tasks[5].Error == "" || !s.Tasks[6].Failed() {
		t.Fatal("failed results lost")
	}
	if s.Tasks[7].Status != "unknown" {
		t.Fatal("future status not preserved")
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), "envs") || strings.Contains(string(data), "never-retain") {
		t.Fatal("captured environment escaped parser boundary")
	}
	if _, err = ParseSnapshot([]byte(`{"default":{"status":"Running"}}`), "x", time.Now()); err == nil {
		t.Fatal("group JSON accepted as status")
	}
}

func TestRemoteQuotingSurvivesShell(t *testing.T) {
	args := []string{"add", "--working-directory", "~/directory with 'quotes' $(printf OOPS)", "--", "printf '%s' \"$HOME\"; echo `printf data`\nline"}
	cmd := exec.Command("/bin/sh", "-c", "set -- "+remoteJoin(args)+"; printf '%s\\000' \"$@\"")
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	home, _ := os.UserHomeDir()
	want := append([]string{}, args...)
	want[2] = home + "/directory with 'quotes' $(printf OOPS)"
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv changed:\nwant %#v\ngot %#v", want, got)
	}
	path := "~/bin/pueue\"$(printf OOPS)"
	out, err := exec.Command("/bin/sh", "-c", "printf '%s' "+remotePath(path)).Output()
	if err != nil || string(out) != home+"/bin/pueue\"$(printf OOPS)" {
		t.Fatalf("unsafe binary quoting: %q %v", out, err)
	}
}

func TestNativeOverlayProfileAndRouting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "original.yml")
	original := []byte("client:\n  read_local_logs: true\nshared:\n  host: wrong\nprofiles:\n  server:\n    client:\n      read_local_logs: true\n      show_confirmation_questions: true\n    shared:\n      use_unix_socket: false\n      host: correct\n      port: '6924'\n      daemon_cert: /private/cert\n      shared_secret_path: /private/secret\ndaemon:\n  env_vars:\n    SECRET: do-not-copy\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	client := New()
	defer client.Close()
	conn := core.Connection{ID: "remote", Kind: "native", Binary: "pueue", ConfigPath: path, Profile: "server", SSHHost: "test-host"}
	effective, err := client.effectiveConfig(conn)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(effective)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err = yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if object(parsed["shared"])["host"] != "correct" || object(parsed["client"])["read_local_logs"] != false || object(parsed["client"])["show_confirmation_questions"] != false {
		t.Fatalf("bad overlay: %s", data)
	}
	if strings.Contains(string(data), "do-not-copy") || strings.Contains(string(data), "profiles:") {
		t.Fatalf("unneeded config copied: %s", data)
	}
	info, _ := os.Stat(effective)
	if info.Mode().Perm() != 0600 {
		t.Fatal("temporary config is not private")
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(original) {
		t.Fatal("original config was changed")
	}
	for _, submission := range []bool{false, true} {
		cmd, err := client.command(context.Background(), conn, []string{"status", "--json"}, submission)
		if err != nil {
			t.Fatal(err)
		}
		isSSH := filepath.Base(cmd.Path) == "ssh"
		if isSSH != submission {
			t.Fatalf("native control routed to wrong transport: %v", cmd.Args)
		}
	}
}

func fakeClient(t *testing.T, fixture []byte, mutation string) (*Client, core.Connection, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	calls := filepath.Join(dir, "calls")
	if err := os.WriteFile(state, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "pueue")
	mutation = strings.ReplaceAll(mutation, "__STATE__", shellQuote(state))
	script := "#!/bin/sh\nshift 4\ncase \"$1\" in\nstatus) cat " + shellQuote(state) + " ;;\n--version) printf 'pueue 4.0.2\\n' ;;\n*) printf '%s\\n' \"$@\" >> " + shellQuote(calls) + "\n" + mutation + "\n;;\nesac\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.yml")
	if err := os.WriteFile(config, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := New()
	t.Cleanup(func() { _ = client.Close() })
	return client, core.Connection{ID: "isolated", Kind: "local", Binary: binary, ConfigPath: config}, calls
}

func TestCleanUsesReviewedIDsAndRechecksIdentity(t *testing.T) {
	fixture := wireFixture(`{"Done":{"result":"Success"}}`, `{"Done":{"result":"Success"}}`)
	var changed map[string]any
	if err := json.Unmarshal(fixture, &changed); err != nil {
		t.Fatal(err)
	}
	delete(changed["tasks"].(map[string]any), "0")
	remaining, _ := json.Marshal(changed)
	client, conn, calls := fakeClient(t, fixture, "printf '%s' "+shellQuote(string(remaining))+" > __STATE__\nexit 0")
	stamp, _ := time.Parse(time.RFC3339Nano, created)
	result, err := client.Execute(context.Background(), conn, core.Request{Operation: "clean", IDs: []int{0}, Guards: map[int]time.Time{0: stamp}, SuccessfulOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "remove\n0\n" || len(result.Outcomes) != 1 {
		t.Fatalf("clean reached unreviewed tasks: %q %+v", data, result)
	}
	_, err = client.Execute(context.Background(), conn, core.Request{Operation: "clean", IDs: []int{1}, Guards: map[int]time.Time{1: stamp.Add(-time.Hour)}})
	if err == nil {
		t.Fatal("reused task ID accepted")
	}
	data, _ = os.ReadFile(calls)
	if string(data) != "remove\n0\n" {
		t.Fatalf("stale identity was mutated: %q", data)
	}
}

func TestInterruptedWriteHasUnknownOutcomeWithoutRetry(t *testing.T) {
	client, conn, calls := fakeClient(t, wireFixture(`{"Done":{"result":"Success"}}`), "exec sleep 10")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type completion struct {
		result core.Result
		err    error
	}
	done := make(chan completion, 1)
	go func() {
		result, err := client.Execute(ctx, conn, core.Request{Operation: "remove", IDs: []int{0}})
		done <- completion{result, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(calls); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mutation did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	completed := <-done
	result, err := completed.result, completed.err
	var transportErr *Error
	if !errors.As(err, &transportErr) || !result.Unknown || !transportErr.Unknown {
		t.Fatalf("interrupted mutation was presented as definite failure: %+v %v", result, err)
	}
	data, _ := os.ReadFile(calls)
	if string(data) != "remove\n0\n" {
		t.Fatalf("mutation retried: %q", data)
	}
}

func TestMutationArguments(t *testing.T) {
	command := "echo 'single' && echo \"$HOME\" | wc -c"
	args, err := ArgsFor(core.Request{Operation: "add", Add: &core.AddRequest{Command: command, Directory: "/remote", After: []int{0, 2}, Mode: "queued"}})
	if err != nil {
		t.Fatal(err)
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != command {
		t.Fatal("command was shell-expanded or split")
	}
	args, err = ArgsFor(core.Request{Operation: "restart", IDs: []int{1}})
	if err != nil || !reflect.DeepEqual(args, []string{"restart", "--not-in-place", "1"}) {
		t.Fatalf("restart depends on user default: %v %v", args, err)
	}
}

func TestNativeSubmissionRequiresCompanion(t *testing.T) {
	client := New()
	defer client.Close()
	conn := core.Connection{ID: "native", Kind: "native"}
	req := core.Request{Operation: "add", Add: &core.AddRequest{Command: "echo hi", Directory: "/remote"}}
	if _, err := client.Preview(conn, req); err == nil || !strings.Contains(err.Error(), "SSH submission companion") {
		t.Fatalf("native add missing setup guidance: %v", err)
	}
	if _, err := client.Execute(context.Background(), conn, req); err == nil || !strings.Contains(err.Error(), "SSH submission companion") {
		t.Fatalf("native add attempted before setup: %v", err)
	}
}

func TestResumeCannotForceStartAChangedTask(t *testing.T) {
	client, conn, calls := fakeClient(t, wireFixture(`{"Queued":{}}`), "printf accepted")
	_, err := client.Execute(context.Background(), conn, core.Request{Operation: "start", IDs: []int{0}, ResumeOnly: true})
	if err == nil {
		t.Fatal("Resume force-started a task that is no longer paused")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("Resume dispatched before state validation")
	}
}

func TestCompanionScopeAndDependencyGuard(t *testing.T) {
	stamp, _ := time.Parse(time.RFC3339Nano, created)
	native := core.Snapshot{Groups: []core.Group{{Name: "default"}}, Tasks: []core.Task{{ID: 0, CreatedAt: stamp, Command: "same"}}}
	other := core.Snapshot{Groups: []core.Group{{Name: "default"}}, Tasks: []core.Task{{ID: 0, CreatedAt: stamp.Add(time.Second), Command: "same"}}}
	if matchingSubmissionTargets(native, other, core.AddRequest{Group: "default", After: []int{0}}) == nil {
		t.Fatal("companion's recycled dependency accepted")
	}
	client, conn, calls := fakeClient(t, wireFixture(`{"Queued":{}}`), "printf '1\\n'")
	req := core.Request{Operation: "add", Add: &core.AddRequest{Command: "echo dependent", Directory: "/", After: []int{0}}, Guards: map[int]time.Time{0: stamp.Add(-time.Second)}}
	if _, err := client.Execute(context.Background(), conn, req); err == nil {
		t.Fatal("recycled dependency ID accepted")
	}
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("stale add invoked a mutation")
	}
}

func TestUnknownMalformedProfileDoesNotUseDefaultDaemon(t *testing.T) {
	client := New()
	defer client.Close()
	path := filepath.Join(t.TempDir(), "pueue.yml")
	if err := os.WriteFile(path, []byte("profiles:\n  broken: typo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.effectiveConfig(core.Connection{Kind: "native", ConfigPath: path, Profile: "broken"}); err == nil {
		t.Fatal("malformed profile silently selected defaults")
	}
}
