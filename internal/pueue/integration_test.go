package pueue

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"go.yaml.in/yaml/v3"
)

// Explicit opt-in, and every daemon/socket/log/secret lives in a new private
// directory. These tests never address the user's default Pueue daemon.
func TestIsolatedDaemonIntegration(t *testing.T) {
	if os.Getenv("LAZYPUEUE_INTEGRATION") != "1" {
		t.Skip("set LAZYPUEUE_INTEGRATION=1 to exercise disposable Pueue daemons")
	}
	for _, bin := range []string{"pueue", "pueued"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is unavailable", bin)
		}
	}
	for _, transport := range []string{"unix", "tls"} {
		t.Run(transport, func(t *testing.T) { integrationWorkflow(t, transport) })
	}
}

func integrationWorkflow(t *testing.T, transport string) {
	c, local, native, dir := isolatedDaemon(t, transport)
	ctx := context.Background()
	do := func(conn core.Connection, req core.Request) core.Result {
		t.Helper()
		result, err := c.Execute(ctx, conn, req)
		if err != nil {
			t.Fatalf("%s: %v (%+v)", req.Operation, err, result)
		}
		return result
	}
	add := func(command, mode string, after ...int) int {
		t.Helper()
		result := do(local, core.Request{Operation: "add", Add: &core.AddRequest{Command: command, Directory: dir, Group: "jobs", Mode: mode, After: after, Priority: -2, Label: "--dash-label"}})
		if result.TaskID == nil {
			t.Fatal("add did not return an ID")
		}
		return *result.TaskID
	}
	wait := func(condition func(core.Snapshot) bool) core.Snapshot {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		var last core.Snapshot
		var err error
		for time.Now().Before(deadline) {
			last, err = c.Snapshot(ctx, native)
			if err == nil && condition(last) {
				return last
			}
			time.Sleep(40 * time.Millisecond)
		}
		states := map[int]string{}
		for _, task := range last.Tasks {
			states[task.ID] = task.State()
		}
		t.Fatalf("daemon condition did not settle: states=%v err=%v", states, err)
		return last
	}
	taskState := func(id int, state string) func(core.Snapshot) bool {
		return func(s core.Snapshot) bool {
			for _, task := range s.Tasks {
				if task.ID == id {
					return task.State() == state
				}
			}
			return false
		}
	}

	do(local, core.Request{Operation: "group-add", Group: "jobs", Parallel: 1})
	do(local, core.Request{Operation: "group-add", Group: "--literal-group", Parallel: 0})
	do(native, core.Request{Operation: "group-remove", Group: "--literal-group"})
	do(native, core.Request{Operation: "parallel", Group: "jobs", Parallel: 2})
	do(native, core.Request{Operation: "group-pause", Group: "jobs"})
	wait(func(s core.Snapshot) bool {
		for _, g := range s.Groups {
			if g.Name == "jobs" {
				return g.Status == "paused" && g.Parallel == 2
			}
		}
		return false
	})
	first := add("printf 'first 中文\\n'", "queued")
	wait(taskState(first, "queued"))
	do(native, core.Request{Operation: "stash", IDs: []int{first}})
	wait(taskState(first, "stashed"))
	if _, err := c.Log(ctx, native, first, 20); err == nil {
		t.Fatal("never-started task returned a log")
	}
	second := add("printf 'second\\n'", "queued", first)
	do(native, core.Request{Operation: "enqueue", IDs: []int{first}})
	do(native, core.Request{Operation: "group-start", Group: "jobs"})
	wait(taskState(first, "succeeded"))
	wait(taskState(second, "succeeded"))
	log, err := c.Log(ctx, native, first, 20)
	if err != nil || !strings.Contains(log, "first 中文") {
		t.Fatalf("remote log: %q %v", log, err)
	}

	live := add("printf 'one\\n'; sleep 1; printf 'two\\n'", "queued")
	wait(taskState(live, "running"))
	var output strings.Builder
	followCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	err = c.Follow(followCtx, native, live, 20, func(chunk string) { output.WriteString(chunk) })
	cancel()
	if err != nil || !strings.Contains(output.String(), "two") {
		t.Fatalf("remote follow: %q %v", output.String(), err)
	}

	long := add("sleep 5", "queued")
	wait(taskState(long, "running"))
	do(native, core.Request{Operation: "pause", IDs: []int{long}})
	wait(taskState(long, "paused"))
	do(native, core.Request{Operation: "start", IDs: []int{long}})
	wait(taskState(long, "running"))
	do(native, core.Request{Operation: "kill", IDs: []int{long}})
	wait(taskState(long, "failed"))

	before := wait(taskState(first, "succeeded"))
	do(native, core.Request{Operation: "restart", IDs: []int{first}})
	after := wait(func(s core.Snapshot) bool {
		return len(s.Tasks) == len(before.Tasks)+1 && s.Tasks[len(s.Tasks)-1].Terminal()
	})
	if after.Tasks[len(after.Tasks)-1].ID == first {
		t.Fatal("default restart reused the task ID")
	}
	failed := add("printf 'failure\\n'; exit 7", "queued")
	before = wait(taskState(failed, "failed"))
	var oldStart time.Time
	for _, task := range before.Tasks {
		if task.ID == failed && task.StartedAt != nil {
			oldStart = *task.StartedAt
		}
	}
	do(native, core.Request{Operation: "restart", IDs: []int{failed}, InPlace: true})
	wait(func(s core.Snapshot) bool {
		for _, task := range s.Tasks {
			if task.ID == failed {
				return task.Failed() && task.StartedAt != nil && task.StartedAt.After(oldStart)
			}
		}
		return false
	})
	before, _ = c.Snapshot(ctx, native)
	do(native, core.Request{Operation: "restart-failed", IDs: []int{failed}})
	wait(func(s core.Snapshot) bool {
		return len(s.Tasks) == len(before.Tasks)+1 && s.Tasks[len(s.Tasks)-1].Failed()
	})

	snap, _ := c.Snapshot(ctx, native)
	var guard time.Time
	for _, task := range snap.Tasks {
		if task.ID == first {
			guard = task.CreatedAt
		}
	}
	do(native, core.Request{Operation: "clean", IDs: []int{first}, Guards: map[int]time.Time{first: guard}, SuccessfulOnly: true})
	wait(func(s core.Snapshot) bool {
		firstExists, secondExists := false, false
		for _, task := range s.Tasks {
			if task.ID == first {
				firstExists = true
			}
			if task.ID == second {
				secondExists = true
			}
		}
		return !firstExists && secondExists
	})
	if _, err := c.Execute(ctx, native, core.Request{Operation: "group-remove", Group: "jobs"}); err == nil {
		t.Fatal("nonempty group removal accepted")
	}
	do(native, core.Request{Operation: "clean", Group: "jobs"})
	wait(func(s core.Snapshot) bool { return len(s.Tasks) == 0 })
	do(native, core.Request{Operation: "group-remove", Group: "jobs"})
	wait(func(s core.Snapshot) bool {
		for _, g := range s.Groups {
			if g.Name == "jobs" {
				return false
			}
		}
		return true
	})
	t.Log("verified add/dependencies, pause/resume/kill, stash/enqueue, both restarts, exact-ID clean, group controls, native logs and follow")
}

func isolatedDaemon(t *testing.T, transport string) (*Client, core.Connection, core.Connection, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lazypueue-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, name := range []string{"certs", "runtime", "data"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	shared := map[string]any{
		"pueue_directory": filepath.Join(dir, "data"), "runtime_directory": filepath.Join(dir, "runtime"),
		"use_unix_socket": transport == "unix", "unix_socket_path": filepath.Join(dir, "daemon.sock"),
		"shared_secret_path": filepath.Join(dir, "secret"), "daemon_cert": filepath.Join(dir, "certs", "daemon.cert"), "daemon_key": filepath.Join(dir, "certs", "daemon.key"),
	}
	if transport == "tls" {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		shared["host"] = "127.0.0.1"
		shared["port"] = fmt.Sprint(port)
	}
	config := filepath.Join(dir, "pueue.yml")
	data, _ := yaml.Marshal(map[string]any{"shared": shared, "client": map[string]any{"read_local_logs": true, "show_confirmation_questions": true}})
	if err = os.WriteFile(config, data, 0600); err != nil {
		t.Fatal(err)
	}
	// Parent owns exactly this child; shutdown/kill never invokes the real user's daemon.
	daemon := exec.Command("pueued", "--config", config)
	logPath := filepath.Join(dir, "daemon-output.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	daemon.Stdout = logFile
	daemon.Stderr = logFile
	if err = daemon.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = daemon.Process.Kill(); _ = daemon.Wait(); _ = logFile.Close() }) })
	client := New()
	t.Cleanup(func() { _ = client.Close() })
	local := core.Connection{ID: "test-local", Kind: "local", ConfigPath: config}
	native := core.Connection{ID: "test-native", Kind: "native", ConfigPath: config}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		snapshot, readErr := client.Snapshot(ctx, native)
		cancel()
		if readErr == nil && len(snapshot.Groups) > 0 {
			// Exercise generated endpoint config, independent of default Pueue config.
			native.ConfigPath = ""
			native.SecretPath = shared["shared_secret_path"].(string)
			if transport == "unix" {
				native.SocketPath = shared["unix_socket_path"].(string)
			} else {
				native.Host = "127.0.0.1"
				native.Port, _ = strconv.Atoi(shared["port"].(string))
				native.CertPath = shared["daemon_cert"].(string)
			}
			if _, err := client.Snapshot(context.Background(), native); err != nil {
				t.Fatalf("generated native connection: %v", err)
			}
			badSecret := filepath.Join(dir, "wrong-secret")
			if err := os.WriteFile(badSecret, []byte("incorrect-test-secret"), 0600); err != nil {
				t.Fatal(err)
			}
			wrong := native
			wrong.SecretPath = badSecret
			if _, err := client.Snapshot(context.Background(), wrong); err == nil {
				t.Fatal("incorrect secret accepted")
			}
			if transport == "tls" {
				badCert := filepath.Join(dir, "wrong-cert")
				if err := os.WriteFile(badCert, []byte("not a certificate"), 0600); err != nil {
					t.Fatal(err)
				}
				wrong = native
				wrong.CertPath = badCert
				if _, err := client.Snapshot(context.Background(), wrong); err == nil {
					t.Fatal("invalid certificate accepted")
				}
			}
			return client, local, native, dir
		}
		time.Sleep(30 * time.Millisecond)
	}
	data, _ = os.ReadFile(logPath)
	t.Fatalf("isolated daemon did not start: %s", data)
	return nil, core.Connection{}, core.Connection{}, ""
}
