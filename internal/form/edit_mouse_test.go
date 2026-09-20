package form

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

func editFixture() *EditModel {
	return NewEdit(core.Task{ID: 7, Command: "expanded", OriginalCommand: "python train.py", Path: "/work", Label: "training", Group: "jobs", Priority: 2, Status: "done", Result: "failed", CreatedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC), Dependencies: []int{3}}, core.Connection{ID: "lab", Kind: "ssh", SSHHost: "lab"}, &fakeBackend{})
}
func TestEditInputDefaultNoAndFailedDraft(t *testing.T) {
	m := editFixture()
	m.Init()
	if m.command.Value() != "python train.py" {
		t.Fatal("did not preserve original command")
	}
	m.Update(tea.PasteMsg{Content: "\necho qjkh/?:"})
	m.Update(key("ctrl+s"))
	if !m.review || m.Request.Operation != "edit-restart" || m.Request.InPlace || m.Request.Guards[7] != m.task.CreatedAt {
		t.Fatalf("bad review: %#v %v", m.Request, m.Err)
	}
	m.Update(key("enter"))
	if m.Done || m.review {
		t.Fatal("Enter did not choose default No")
	}
	m.Update(key("ctrl+s"))
	m.Update(key("n"))
	if m.Done || m.review {
		t.Fatal("n submitted")
	}
	m.Update(key("ctrl+s"))
	m.Update(key("y"))
	if !m.Done {
		t.Fatal("y did not submit")
	}
	m.Reject(errors.New("daemon disconnected"))
	if m.Done || m.Err == nil || !strings.Contains(m.command.Value(), "echo qjkh/?:") {
		t.Fatal("failed draft lost")
	}
}
func TestEditAdvancedPoliciesAndScheduledRestriction(t *testing.T) {
	m := editFixture()
	m.SetRequest(core.Request{Operation: "edit-restart", InPlace: true})
	m.Update(key("ctrl+s"))
	if !m.review || !m.Request.InPlace || !strings.Contains(m.View(100, 30), "overwrite") {
		t.Fatal("in-place consequence missing")
	}
	running := m.task
	running.Status = "running"
	m = NewEdit(running, m.connection, &fakeBackend{})
	m.Update(key("ctrl+s"))
	if m.Request.Operation != "stop-edit-restart" {
		t.Fatal("running correction lacks stop")
	}
	scheduled := m.task
	scheduled.Status = "queued"
	now := time.Now()
	scheduled.ScheduledAt = &now
	m = NewEdit(scheduled, m.connection, &fakeBackend{})
	m.fields[5].SetValue("yes")
	m.Update(key("ctrl+s"))
	if m.Err == nil || m.review {
		t.Fatal("scheduled task stashed silently")
	}
}
func click(x, y int) tea.MouseClickMsg { return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft} }
func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}
func TestMousePureGeometryAndResizeCancellation(t *testing.T) {
	m, _ := taskFixture(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 26})
	body, focus, spans := m.body()
	hits := fieldHits(body, focus, spans, 100, 26)
	var target formHit
	for _, h := range hits {
		if h.index == 4 {
			target = h
		}
	}
	m.Update(click(target.x+1, target.y))
	if m.focus != 4 {
		t.Fatal("field click did not focus without View")
	}
	m.Update(key("q"))
	if m.Cancelled || m.fields[4].Value() != "q" {
		t.Fatal("clicked input did not own printable key")
	}
	m.Update(click(2, 25))
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(release(2, 25))
	if m.review {
		t.Fatal("resize activated stale button")
	}
	m.Update(click(2, 23))
	m.Update(release(2, 23))
	if !m.review {
		t.Fatalf("review button failed: %v", m.Err)
	}
	m.Update(click(2, 23))
	m.Update(release(50, 23))
	if m.Done {
		t.Fatal("release outside button submitted")
	}
}
func TestEditMouseRequiresMatchingYesRelease(t *testing.T) {
	m := editFixture()
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(key("ctrl+s"))
	foot := buttonFooter([]formButton{{"back", "No, edit"}, {"submit", "Yes, apply"}, {"cancel", "Cancel"}}, "", 100, 24)
	var yes formHit
	for _, h := range foot.hits {
		if h.id == "submit" {
			yes = h
		}
	}
	m.Update(click(yes.x+1, yes.y))
	m.Update(key("esc"))
	m.Update(release(yes.x+1, yes.y))
	if m.Done {
		t.Fatal("changed modal accepted old press")
	}
	m.Update(key("ctrl+s"))
	m.Update(click(yes.x+1, yes.y))
	m.Update(release(yes.x+1, yes.y))
	if !m.Done {
		t.Fatal("matching yes click did not submit")
	}
}
func TestConnectionEditRetainsLogOverrides(t *testing.T) {
	initial := core.Connection{ID: "lab", Kind: "ssh", SSHHost: "host", Logs: core.LogSettings{MultiMode: "manual", PollInterval: "30s"}}
	m := NewConnection(initial, true, &fakeBackend{})
	m.Update(key("ctrl+s"))
	if m.Err != nil || m.Connection.Logs != initial.Logs {
		t.Fatalf("connection edit dropped logs: %#v %v", m.Connection, m.Err)
	}
}
