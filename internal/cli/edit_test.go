package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
)

func TestEditWizardFailureAndMouseCapture(t *testing.T) {
	f := setup(t)
	task := core.Task{ID: 3, Status: "done", Result: "failed", Command: "false", Path: "/tmp"}
	model := form.NewEdit(task, core.Connection{ID: "local", Kind: "local"}, f)
	host := &wizardHost{edit: model, width: 80, height: 24, mouse: true}
	host.Update(wizardResultMsg{result: core.Result{Message: "Task remains stashed"}, err: errors.New("retry failed")})
	if host.edit.Err == nil || !strings.Contains(host.edit.Err.Error(), "Task remains stashed") {
		t.Fatalf("edit failure did not return to the form: %v", host.edit.Err)
	}
	if host.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("standalone wizard does not request mouse events")
	}
	host.mouse = false
	if host.View().MouseMode == tea.MouseModeCellMotion {
		t.Fatal("disabled mouse still captured")
	}
}

func TestRestartShortFlagsAndRequiredConfirmation(t *testing.T) {
	f := setup(t)
	_, _, err := invoke(t, f, false, "restart", "3")
	if ExitCode(err) != 2 || !strings.Contains(err.Error(), "--yes") || f.writes != 0 {
		t.Fatalf("new-ID restart bypassed review: %v %+v", err, f)
	}
	_, _, err = invoke(t, f, false, "restart", "-i", "3", "--yes")
	if err != nil || !f.request.InPlace || f.writes != 1 {
		t.Fatalf("-i was not in-place: %v %+v", err, f)
	}
	_, _, err = invoke(t, f, false, "restart", "-e", "3", "--yes")
	if ExitCode(err) != 2 || f.writes != 1 {
		t.Fatalf("-e opened UI without a terminal: %v %+v", err, f)
	}
}

func TestScriptedEditKeepsContextAndGuards(t *testing.T) {
	f := setup(t)
	created := time.Date(2026, 9, 20, 1, 2, 3, 0, time.UTC)
	f.snapshotOverride = &core.Snapshot{Tasks: []core.Task{{ID: 3, Status: "done", Result: "failed", Command: "expanded original", OriginalCommand: "original", Path: "/tmp", Group: "default", Label: "old", Priority: 4, CreatedAt: created}}, Groups: []core.Group{{Name: "default", Status: "running"}}}
	_, _, err := invoke(t, f, false, "edit", "3", "--command", "echo fixed", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	request := f.request
	if request.Operation != "edit-restart" || request.InPlace || request.Edit.Command != "echo fixed" || request.Edit.Directory != "/tmp" || request.Edit.Priority != 4 || !request.Guards[3].Equal(created) {
		t.Fatalf("edit lost original context: %+v", request)
	}
	_, _, err = invoke(t, f, false, "edit", "3", "--command", "echo other")
	if ExitCode(err) != 2 || f.writes != 1 {
		t.Fatalf("edit bypassed confirmation: %v %+v", err, f)
	}
}

func TestClearGroupCapturesExactTasks(t *testing.T) {
	f := setup(t)
	created := time.Now()
	f.snapshotOverride = &core.Snapshot{Tasks: []core.Task{{ID: 3, Group: "default", Status: "running", CreatedAt: created}, {ID: 4, Group: "elsewhere", Status: "queued", CreatedAt: created}}, Groups: []core.Group{{Name: "default", Status: "running"}, {Name: "elsewhere", Status: "running"}}}
	_, _, err := invoke(t, f, false, "group", "clear", "default", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if f.request.Operation != "clear-group" || len(f.request.IDs) != 1 || f.request.IDs[0] != 3 || !f.request.Guards[3].Equal(created) {
		t.Fatalf("clear scope was not frozen: %+v", f.request)
	}
	_, _, err = invoke(t, f, false, "group", "clear", "does-not-exist", "--yes")
	if err == nil || f.writes != 1 {
		t.Fatalf("missing group accepted: %v", err)
	}
}

func TestMultiLogAndFullFlagPolicy(t *testing.T) {
	f := setup(t)
	output, _, err := invoke(t, f, false, "log", "3", "4", "--json")
	var result struct {
		Logs map[string]core.LogResult `json:"logs"`
	}
	if err != nil || json.Unmarshal([]byte(output), &result) != nil || len(result.Logs) != 2 {
		t.Fatalf("multi log: %v %s", err, output)
	}
	reads := f.reads
	_, _, err = invoke(t, f, false, "log", "3", "--full", "--json")
	if ExitCode(err) != 2 || f.reads != reads {
		t.Fatalf("full JSON policy: %v", err)
	}
	_, _, err = invoke(t, f, false, "follow", "3", "4")
	if ExitCode(err) != 2 || f.reads != reads {
		t.Fatalf("multi stream was silently truncated: %v", err)
	}
}
