package form

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type fakeBackend struct {
	snapshot core.Snapshot
	err      error
	calls    int
}

func (f *fakeBackend) Snapshot(context.Context, core.Connection) (core.Snapshot, error) {
	f.calls++
	return f.snapshot, f.err
}
func (f *fakeBackend) Log(context.Context, core.Connection, int, int) (string, error) { return "", nil }
func (f *fakeBackend) Follow(context.Context, core.Connection, int, int, func(string)) error {
	return nil
}
func (f *fakeBackend) Preview(c core.Connection, r core.Request) (core.Plan, error) {
	return core.Plan{ConnectionID: c.ID, Display: "pueue add -- " + r.Add.Command}, nil
}
func (f *fakeBackend) Execute(context.Context, core.Connection, core.Request) (core.Result, error) {
	panic("forms must never execute mutations")
}
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	if strings.HasPrefix(s, "ctrl+") {
		return tea.KeyPressMsg{Code: []rune(s[5:])[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
func taskFixture(t *testing.T) (*TaskModel, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{snapshot: core.Snapshot{Groups: []core.Group{{Name: "default", Status: "running"}}, Tasks: []core.Task{{ID: 7, Command: "first", Status: "running"}}}}
	m := NewTask([]core.Connection{{ID: "local", Kind: "local"}, {ID: "remote", Kind: "ssh", SSHHost: "lab"}}, core.AddRequest{Command: "echo hello", Directory: "/tmp", Group: "default", Mode: "queued"}, "local", b)
	m.command.Focus()
	cmd := m.loadSnapshot()
	m.Update(cmd())
	return m, b
}
func TestTaskTypingPasteAndExplicitReview(t *testing.T) {
	m, _ := taskFixture(t)
	for _, r := range "qjkh/l?:" {
		m.Update(key(string(r)))
	}
	m.Update(key("enter"))
	m.Update(tea.PasteMsg{Content: "echo pasted\nq"})
	if m.Done || m.Cancelled || m.review || !strings.Contains(m.command.Value(), "echo pasted\nq") {
		t.Fatalf("typing acted as navigation: %q", m.command.Value())
	}
	m.Update(key("ctrl+s"))
	if !m.review || m.Done || m.Err != nil {
		t.Fatalf("review failed: %v", m.Err)
	}
	repeated := key("ctrl+s")
	repeated.IsRepeat = true
	m.Update(repeated)
	if m.Done {
		t.Fatal("holding the review key also submitted the form")
	}
	m.Update(key("enter"))
	if m.Done {
		t.Fatal("Enter submitted review")
	}
	m.Update(key("esc"))
	if m.review || m.Cancelled {
		t.Fatal("Esc did not return to draft")
	}
	m.Update(key("ctrl+s"))
	m.Update(key("ctrl+s"))
	if !m.Done {
		t.Fatal("explicit submit did not finish")
	}
	m.Reject(errors.New("daemon went away"))
	if m.Done || m.Cancelled || m.review || m.Err == nil || !strings.Contains(m.command.Value(), "echo pasted") {
		t.Fatal("failed submission discarded draft")
	}
}
func TestTaskChangingConnectionClearsHostSpecificDraftAndLateSnapshot(t *testing.T) {
	m, _ := taskFixture(t)
	m.fields[6].SetValue("7")
	oldGeneration := m.generation
	cmd := m.setConnection("remote")
	if m.fields[6].Value() != "" || m.fields[2].Value() != "" {
		t.Fatal("cross-host path or dependency retained")
	}
	m.Update(taskSnapshotMsg{owner: m, generation: oldGeneration, connection: "local", snapshot: core.Snapshot{Tasks: []core.Task{{ID: 99}}}})
	if len(m.snapshot.Tasks) != 0 {
		t.Fatal("old target overwrote state")
	}
	m.Update(cmd())
	if len(m.snapshot.Tasks) != 1 {
		t.Fatal("new target response not received")
	}
}

func TestTaskReviewPinsEveryDependencyIncarnationUntilSubmit(t *testing.T) {
	m, _ := taskFixture(t)
	before := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
	after := before.Add(time.Hour)
	m.snapshot.Tasks[0].CreatedAt = before
	m.fields[6].SetValue("7")
	m.Update(key("ctrl+s"))
	if !m.review || !m.Guards[7].Equal(before) {
		t.Fatalf("review did not guard a typed dependency: %#v %v", m.Guards, m.Err)
	}
	oldGuards := m.Guards
	replacement := core.Snapshot{Groups: append([]core.Group(nil), m.snapshot.Groups...), Tasks: []core.Task{{ID: 7, Command: "replacement", Status: "running", CreatedAt: after}}}
	m.Update(taskSnapshotMsg{owner: m, generation: m.generation, connection: m.ConnectionID, snapshot: replacement})
	m.Update(key("ctrl+s"))
	if !m.Done || !m.Guards[7].Equal(before) {
		t.Fatal("a later snapshot silently changed the submitted dependency incarnation")
	}
	m.Reject(errors.New("dependency changed"))
	m.Update(key("ctrl+s"))
	if !m.review || !m.Guards[7].Equal(after) {
		t.Fatal("a new explicit review did not capture the new snapshot")
	}
	if !oldGuards[7].Equal(before) {
		t.Fatal("review mutated a guard map already handed to a host")
	}
	m.setConnection("remote")
	if len(m.Guards) != 0 {
		t.Fatal("dependency guards crossed connections")
	}
}
func TestTaskDependencyAndCreateGroupReview(t *testing.T) {
	m, _ := taskFixture(t)
	m.Update(key("ctrl+d"))
	m.Update(key("enter"))
	m.Update(key("ctrl+s"))
	if m.fields[6].Value() != "7" {
		t.Fatal("dependency picker lost choice")
	}
	m.fields[5].SetValue("immediate")
	m.Update(key("ctrl+s"))
	if m.Err == nil || m.review {
		t.Fatal("immediate ignored dependencies")
	}
	m.fields[5].SetValue("queued")
	m.fields[3].SetValue("training")
	m.Update(key("ctrl+s"))
	if m.Err == nil {
		t.Fatal("implicit group creation permitted")
	}
	m.fields[9].SetValue("yes")
	m.fields[10].SetValue("2")
	m.Update(key("ctrl+s"))
	if !m.review || !m.Request.CreateGroup || m.Request.GroupParallel != 2 {
		t.Fatalf("create group review failed: %v", m.Err)
	}
	if !strings.Contains(m.View(100, 30), "Create group") {
		t.Fatal("group creation missing from review")
	}
}
func TestTaskCancelAndSmallUnicodeView(t *testing.T) {
	m, _ := taskFixture(t)
	m.command.SetValue("echo 專案 👩🏽‍💻 é")
	for _, size := range [][2]int{{1, 1}, {12, 4}, {38, 12}, {80, 24}, {140, 42}} {
		view := m.View(size[0], size[1])
		if len(strings.Split(view, "\n")) > size[1] {
			t.Fatalf("view height exceeds %v", size)
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("line too wide for %v: %q", size, line)
			}
		}
	}
	m.Update(key("ctrl+c"))
	if !m.Cancelled || m.Done {
		t.Fatal("cancel submitted")
	}
}
func TestConnectionTestIsAsyncAndInvalidatedByTyping(t *testing.T) {
	b := &fakeBackend{snapshot: core.Snapshot{Tasks: []core.Task{{ID: 1}}}}
	m := NewConnection(core.Connection{ID: "lab", Name: "Lab", Kind: "ssh", SSHHost: "box"}, true, b)
	m.Init()
	_, cmd := m.Update(key("ctrl+t"))
	if cmd == nil || !m.testing || b.calls != 0 {
		t.Fatal("test did synchronous I/O")
	}
	result := cmd()
	m.Update(key("x"))
	m.Update(result)
	if m.testResult != "" || m.testing {
		t.Fatal("stale test applied after draft edit")
	}
	if m.Connection.Name != "Lab" || m.Done {
		t.Fatal("draft test saved connection")
	}
	_, cmd = m.Update(key("ctrl+t"))
	m.Update(cmd())
	if !strings.Contains(m.testResult, "Connected") {
		t.Fatal("test result missing")
	}
}
func TestConnectionTypingCancelAndFailedSavePreservesDraft(t *testing.T) {
	m := NewConnection(core.Connection{ID: "local", Kind: "local"}, true, &fakeBackend{})
	m.Init()
	for _, r := range "qjkh/l?:" {
		m.Update(key(string(r)))
	}
	if m.Cancelled || m.Done || m.fields[0].Value() != "local" {
		t.Fatal("typing dispatched shortcuts or changed ID")
	}
	m.Update(key("ctrl+s"))
	if !m.review {
		t.Fatalf("review failed: %v", m.Err)
	}
	m.Update(key("enter"))
	if m.Done {
		t.Fatal("enter submitted")
	}
	m.Update(key("ctrl+s"))
	if !m.Done {
		t.Fatal("save did not finish")
	}
	m.Reject(errors.New("configuration conflict"))
	if m.Done || m.fields[1].Value() != "qjkh/l?:" {
		t.Fatal("failed save lost draft")
	}
	m.Update(key("esc"))
	if !m.Cancelled {
		t.Fatal("cancel failed")
	}
}
func TestConnectionKindFieldsAndNativeReferences(t *testing.T) {
	m := NewConnection(core.Connection{ID: "native", Kind: "native", ConfigPath: "/tmp/pueue.yml", Profile: "server", SSHHost: "lab"}, false, nil)
	m.Update(key("ctrl+s"))
	if !m.review {
		t.Fatalf("native reference invalid: %v", m.Err)
	}
	if m.Connection.SSHHost != "lab" || m.Connection.Profile != "server" {
		t.Fatal("hidden companion or profile lost")
	}
	m.Update(key("esc"))
	m.fields[7].SetValue("host")
	m.Update(key("ctrl+s"))
	if m.Err == nil {
		t.Fatal("ambiguous native configuration accepted")
	}
}
