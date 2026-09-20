package pueue

import (
	"context"
	"encoding/json"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/pelletier/go-toml/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEditorScriptWritesDataWithoutExecutingCommand(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	e := core.EditRequest{Command: "printf '\"\"\" 中文'; touch " + shellQuote(marker) + "\n# $HOME $(echo bad)", Directory: "/remote/dir with 'quote'", Label: "label '\"\\\nnext", Priority: -9}
	script, err := editorScript(7, e)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "editor.sh")
	if err = os.WriteFile(helper, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "tasks.toml")
	if err = os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", helper, file).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", output, err)
	}
	data, _ := os.ReadFile(file)
	var parsed map[string]map[string]any
	if err = toml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["7"]["command"] != e.Command || parsed["7"]["path"] != e.Directory || parsed["7"]["label"] != e.Label {
		t.Fatalf("edited fields changed: %s", data)
	}
	folder := filepath.Join(dir, "files")
	if err = os.MkdirAll(filepath.Join(folder, "7"), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", helper, folder).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", output, err)
	}
	command, _ := os.ReadFile(filepath.Join(folder, "7", "command"))
	if string(command) != e.Command {
		t.Fatal("file-mode command changed")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("editor executed the user's command")
	}
}

func TestScheduledTaskCannotBeMisrepresentedAsUnscheduledStash(t *testing.T) {
	client, conn, calls := fakeClient(t, wireFixture(`{"Stashed":{"enqueue_at":"2027-01-01T00:00:00Z"}}`), "exit 0")
	e := core.EditRequest{Command: "echo fixed", Directory: "/", Stashed: true}
	_, err := client.Execute(context.Background(), conn, core.Request{Operation: "edit", IDs: []int{0}, Edit: &e})
	if err == nil || !strings.Contains(err.Error(), "scheduled task retains its schedule") {
		t.Fatalf("bad validation: %v", err)
	}
	if _, err = os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("scheduled task mutated before rejection")
	}
}

func TestBatchLogsDropEnvironmentAndPreserveRepeatedLines(t *testing.T) {
	fixture := wireFixture(`{"Running":{}}`, `{"Done":{"result":"Success"}}`)
	var state map[string]any
	if err := json.Unmarshal(fixture, &state); err != nil {
		t.Fatal(err)
	}
	raw := state["tasks"].(map[string]any)
	logs, _ := json.Marshal(map[string]any{"0": map[string]any{"task": raw["0"], "output": "tick\ntick\n"}, "1": map[string]any{"task": raw["1"], "output": "done\n"}})
	client, conn, calls := fakeClient(t, fixture, "printf '%s' "+shellQuote(string(logs)))
	values, err := client.Logs(context.Background(), conn, []int{0, 1, 99}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if values[0].Output != "tick\ntick\n" || values[0].Task == nil || values[99].Error == "" {
		t.Fatalf("bad batch: %+v", values)
	}
	encoded, _ := json.Marshal(values)
	if strings.Contains(string(encoded), "envs") || strings.Contains(string(encoded), "never-retain") {
		t.Fatal("log task metadata leaked captured environment")
	}
	data, _ := os.ReadFile(calls)
	if strings.Count(string(data), "log\n") != 1 {
		t.Fatalf("batch made separate subprocess calls: %s", data)
	}
}

func TestBatchLogsDoNotIsolateAuthenticationFailures(t *testing.T) {
	client, conn, calls := fakeClient(t, wireFixture(), "printf '%s' 'Permission denied (publickey)' >&2\nexit 255")
	if _, err := client.Logs(context.Background(), conn, []int{0, 1, 2}, 20); err == nil {
		t.Fatal("authentication failure ignored")
	}
	data, _ := os.ReadFile(calls)
	if strings.Count(string(data), "log\n") != 1 {
		t.Fatalf("authentication failure retried per task: %s", data)
	}
}

func TestRemoveExitZeroPartialFailureIsReconciled(t *testing.T) {
	fixture := wireFixture(`{"Done":{"result":"Success"}}`, `{"Done":{"result":"Success"}}`)
	var state map[string]any
	_ = json.Unmarshal(fixture, &state)
	delete(state["tasks"].(map[string]any), "0")
	remaining, _ := json.Marshal(state)
	client, conn, _ := fakeClient(t, fixture, "printf '%s' "+shellQuote(string(remaining))+" > __STATE__\nprintf 'removed 0; failed 1\\n'\nexit 0")
	result, err := client.Execute(context.Background(), conn, core.Request{Operation: "remove", IDs: []int{0, 1}})
	if err == nil || len(result.Outcomes) != 2 || result.Outcomes[0].Error != "" || result.Outcomes[1].Error == "" {
		t.Fatalf("partial removal reported success: %+v %v", result, err)
	}
}

func TestRemovalLayersAndClearGuard(t *testing.T) {
	layers, err := removalLayers([]core.Task{{ID: 1}, {ID: 2, Dependencies: []int{1}}, {ID: 3, Dependencies: []int{2}}})
	if err != nil || !reflect.DeepEqual(layers, [][]int{{3}, {2}, {1}}) {
		t.Fatalf("bad dependency order: %v %v", layers, err)
	}
	client, conn, calls := fakeClient(t, wireFixture(`{"Queued":{}}`), "exit 0")
	_, err = client.Execute(context.Background(), conn, core.Request{Operation: "clear-group", Group: "default", IDs: []int{0}, Guards: map[int]time.Time{0: time.Unix(0, 0)}})
	if err == nil {
		t.Fatal("clear accepted recycled ID")
	}
	if _, err = os.Stat(calls); !os.IsNotExist(err) {
		t.Fatal("clear mutated before validating guards")
	}
}
