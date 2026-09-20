package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
)

type fakeBackend struct {
	request    core.Request
	connection core.Connection
	writes     int
}

func (f *fakeBackend) Snapshot(context.Context, core.Connection) (core.Snapshot, error) {
	return core.Snapshot{}, nil
}
func (f *fakeBackend) Log(context.Context, core.Connection, int, int) (string, error) {
	return "tail", nil
}
func (f *fakeBackend) Follow(ctx context.Context, _ core.Connection, _ int, _ int, emit func(string)) error {
	emit("first\n")
	<-ctx.Done()
	return ctx.Err()
}
func (f *fakeBackend) Preview(c core.Connection, r core.Request) (core.Plan, error) {
	return core.Plan{ConnectionID: c.ID, Display: "pueue " + r.Operation}, nil
}
func (f *fakeBackend) Execute(_ context.Context, c core.Connection, r core.Request) (core.Result, error) {
	f.writes++
	f.connection = c
	f.request = r
	return core.Result{ConnectionID: c.ID, Message: "accepted"}, nil
}
func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	if strings.HasPrefix(s, "ctrl+") {
		return tea.KeyPressMsg{Code: []rune(s[5:])[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}
func fixture(t *testing.T) (*model, *fakeBackend) {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	b := &fakeBackend{}
	cfg := config.Default()
	cfg.Connections = append(cfg.Connections, core.Connection{ID: "lab", Name: "Lab", Kind: "ssh", SSHHost: "lab"})
	ctx, cancel := context.WithCancel(context.Background())
	m := newModel(ctx, cfg, "", b, "all")
	t.Cleanup(func() { cancel(); m.close() })
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	started := created.Add(time.Second)
	ended := started.Add(time.Minute)
	for _, c := range cfg.Connections {
		m.states[c.ID].Snapshot = core.Snapshot{ConnectionID: c.ID, ObservedAt: time.Now(), Groups: []core.Group{{Name: "default", Status: "running", Parallel: 1}}, Tasks: []core.Task{{ID: 7, Command: "echo 專案 é 👩🏽‍💻", Label: "train-專案", Group: "default", Path: "/tmp", Status: "running", CreatedAt: created, StartedAt: &started}, {ID: 8, Command: "false", Group: "default", Path: "/tmp", Status: "done", Result: "Failed", CreatedAt: created.Add(time.Minute), StartedAt: &started, EndedAt: &ended}}}
	}
	m.reconcileSelection()
	return m, b
}
func TestSelectionIdentityAndStaleResults(t *testing.T) {
	m, _ := fixture(t)
	m.switchScope("local", "")
	m.move(1)
	selected := m.currentView().Selected
	s := m.states["local"]
	s.Generation = 3
	snap := s.Snapshot
	snap.Tasks = append([]core.Task{{ID: 1, Command: "new", Group: "default", Status: "running", CreatedAt: time.Now()}}, snap.Tasks...)
	m.Update(snapshotMsg{ID: "local", Generation: 3, Snapshot: snap})
	if m.currentView().Selected != selected {
		t.Fatal("refresh moved selection away from its identity")
	}
	m.Update(snapshotMsg{ID: "local", Generation: 2, Err: errors.New("late")})
	if s.Err != nil {
		t.Fatal("late error replaced current success")
	}
	m.Update(snapshotMsg{ID: "local", Generation: 3, Err: errors.New("offline")})
	if len(s.Snapshot.Tasks) != 3 || s.Err == nil {
		t.Fatal("failed refresh discarded useful rows")
	}
	for i := range snap.Tasks {
		if snap.Tasks[i].ID == 8 {
			snap.Tasks[i].CreatedAt = time.Now()
		}
	}
	m.previewKey = selected
	m.Update(snapshotMsg{ID: "local", Generation: 3, Snapshot: snap})
	if m.currentView().Selected == selected || m.previewKey == selected {
		t.Fatal("reused ID kept prior task/log identity")
	}
}
func TestInputOwnsKeysAndFilterEnterDoesNotOpen(t *testing.T) {
	m, _ := fixture(t)
	m.Update(key("/"))
	for _, r := range "qjkh/l?:" {
		m.Update(key(string(r)))
	}
	if m.currentView().Query != "qjkh/l?:" || m.overlay != "" || m.log.Open {
		t.Fatal("field input triggered a shortcut")
	}
	m.Update(key("enter"))
	if m.inputMode != "" || m.log.Open {
		t.Fatal("filter Enter opened a log")
	}
	m.Update(key("esc"))
	if m.currentView().Query != "" {
		t.Fatal("Esc did not clear filter")
	}
	m.Update(key(":"))
	m.Update(key("q"))
	if m.overlay != "actions" || m.input.Value() != "q" {
		t.Fatal("action search q quit instead of typing")
	}
}
func TestCapturedActionRoutesToOriginalHost(t *testing.T) {
	m, b := fixture(t)
	m.switchScope("local", "")
	m.move(1)
	var restart action
	for _, a := range m.actions() {
		if a.ID == "restart" && !a.Target.Request.InPlace {
			restart = a
			break
		}
	}
	m.switchScope("lab", "")
	m.perform(restart)
	if m.confirm == nil || m.confirm.Connection.ID != "local" {
		t.Fatal("action target followed current host")
	}
	repeated := key("enter")
	repeated.IsRepeat = true
	if m.overlayKey(repeated) != nil || m.confirm == nil {
		t.Fatal("repeated Enter submitted a confirmation")
	}
	cmd := m.overlayKey(key("enter"))
	if cmd == nil {
		t.Fatal("no execute command")
	}
	cmd()
	if b.connection.ID != "local" || len(b.request.Guards) != 1 || b.request.InPlace {
		t.Fatalf("wrong write target: %+v", b.request)
	}
	if m.states["lab"].Writing {
		t.Fatal("pending write leaked into other connection")
	}
}
func TestMultiselectDoesNotCrossHosts(t *testing.T) {
	m, _ := fixture(t)
	m.Update(key(" "))
	first, _ := m.task()
	m.move(1)
	second, _ := m.task()
	if first.Connection.ID == second.Connection.ID {
		t.Fatal("fixture ordering unexpected")
	}
	m.Update(key(" "))
	if len(m.selected) != 1 {
		t.Fatal("cross-host multiselect accepted")
	}
}
func TestAddAfterAndUnknownOutcome(t *testing.T) {
	m, b := fixture(t)
	r, _ := m.task()
	m.openTask("after")
	if m.taskForm.ConnectionID != r.Connection.ID || len(m.taskForm.Request.After) != 1 || m.taskForm.Request.After[0] != r.Task.ID {
		t.Fatal("Add after draft lost target/dependency")
	}
	m.Update(operationMsg{ID: r.Connection.ID, Err: errors.New("connection lost"), Result: core.Result{Unknown: true}, FromForm: true})
	m.Update(key("ctrl+s"))
	m.Update(key("ctrl+s"))
	if !m.formUnknown || b.writes != 0 {
		t.Fatal("unknown result allowed duplicate submit")
	}
	m.Update(key("esc"))
	if m.taskForm != nil {
		t.Fatal("cannot return from unknown result")
	}
}
func TestLogGenerationBoundAndControls(t *testing.T) {
	m, _ := fixture(t)
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	m.log = logState{Open: true, Key: "local/7", Generation: 4, Context: ctx, AutoScroll: true}
	m.acceptLog(logChunkMsg{Key: "lab/7", Generation: 4, Text: "wrong host", Done: true})
	m.acceptLog(logChunkMsg{Key: "local/7", Generation: 3, Text: "old", Done: true})
	if m.log.Text != "" {
		t.Fatal("late stream leaked")
	}
	m.acceptLog(logChunkMsg{Key: "local/7", Generation: 4, Text: strings.Repeat("line\n", 12000) + "\x1b]52;c;attack\a\x1b[2Jtail", Done: true})
	if strings.Count(m.log.Text, "\n") > 10000 || len(m.log.Text) > 2*1024*1024 {
		t.Fatal("unbounded log")
	}
	if strings.Contains(strings.Join(m.logLines(), "\n"), "\x1b") {
		t.Fatal("external control sequence escaped sanitizer")
	}
	m.logKey(key("up"))
	if m.log.AutoScroll {
		t.Fatal("scroll did not pause following")
	}
	m.logKey(key("G"))
	if !m.log.AutoScroll {
		t.Fatal("G did not resume following")
	}
}
func TestLayoutResizeUnicodeAndNoRenderMutation(t *testing.T) {
	m, _ := fixture(t)
	m.previewText = "last log line"
	before := len(m.views)
	for _, size := range [][2]int{{140, 42}, {120, 36}, {100, 30}, {80, 24}, {38, 12}, {12, 4}, {1, 1}, {80, 8}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		view := m.View()
		lines := strings.Split(view.Content, "\n")
		if len(lines) > size[1] {
			t.Fatalf("%v: height %d", size, len(lines))
		}
		for _, line := range lines {
			if width := ansi.StringWidth(line); width > size[0] {
				t.Fatalf("%v: width %d: %q", size, width, line)
			}
		}
		if !view.AltScreen {
			t.Fatal("dashboard lacks alternate screen")
		}
	}
	if len(m.views) != before {
		t.Fatal("render created state")
	}
}
func TestFormOwnsQuitAndPaste(t *testing.T) {
	m, b := fixture(t)
	m.taskForm = form.NewTask(m.cfg.Connections, core.AddRequest{Group: "default", Mode: "queued"}, "local", b)
	m.taskForm.Init()
	m.Update(key("q"))
	m.Update(tea.PasteMsg{Content: "jk\n?/:"})
	if m.taskForm == nil || m.taskForm.Done || m.taskForm.Cancelled {
		t.Fatal("form text escaped into dashboard")
	}
}

func TestViewStateRestoreDoesNotOverrideManualNavigation(t *testing.T) {
	m, _ := fixture(t)
	m.switchScope("local", "")
	m.currentView().Query = "false"
	m.reconcileSelection()
	saved := m.captureState()
	n := newModel(m.ctx, m.cfg, "", m.backend, "")
	for id, s := range m.states {
		n.states[id].Snapshot = s.Snapshot
	}
	n.Update(stateLoadedMsg{State: saved})
	if n.scope != "local" || n.currentView().Query != "false" {
		t.Fatal("saved scope/filter were not restored")
	}
	n.currentView().Query = "new"
	if saved.Views[n.viewKey()].Query != "false" {
		t.Fatal("captured state aliases live UI")
	}
	other := newModel(m.ctx, m.cfg, "", m.backend, "")
	other.Update(key("2"))
	other.Update(stateLoadedMsg{State: saved})
	if other.scope != "all" || other.tab != 1 {
		t.Fatal("late preferences overrode user navigation")
	}
}
func TestConnectionCannotBeOverwrittenOrChangedDuringWrite(t *testing.T) {
	m, _ := fixture(t)
	c, _ := m.connection("local")
	c.Name = "replacement"
	msg := m.saveConnection(c, false)().(connectionSavedMsg)
	if msg.Err == nil {
		t.Fatal("new connection reused existing ID")
	}
	m.connectionEditing = true
	m.states["local"].Writing = true
	msg = m.saveConnection(c, false)().(connectionSavedMsg)
	if msg.Err == nil {
		t.Fatal("connection changed while write pending")
	}
	m.overlay = "connections"
	m.menuIndex = 1
	m.openConnection(true)
	if m.connectionForm != nil {
		t.Fatal("editing started while write pending")
	}
	m.overlayKey(key("d"))
	if m.confirm != nil {
		t.Fatal("removal offered while write pending")
	}
}
func TestGroupRestartPinsReviewedFailures(t *testing.T) {
	m, b := fixture(t)
	m.tab = 1
	m.switchScope("local", "")
	var selected action
	for _, a := range m.actions() {
		if a.ID == "restart-failed" {
			selected = a
			break
		}
	}
	m.perform(selected)
	if m.confirm == nil || len(m.confirm.Request.IDs) != 1 || len(m.confirm.Request.Guards) != 1 {
		t.Fatal("group restart did not capture task identities")
	}
	s := m.states["local"]
	extra := s.Snapshot.Tasks[1]
	extra.ID = 20
	s.Snapshot.Tasks = append(s.Snapshot.Tasks, extra)
	cmd := m.overlayKey(key("enter"))
	cmd()
	if len(b.request.IDs) != 1 || b.request.IDs[0] != 8 {
		t.Fatal("restart expanded after review")
	}
}
func TestLockedTaskActionsAndResumeSemantics(t *testing.T) {
	m, _ := fixture(t)
	m.switchScope("local", "")
	m.states["local"].Snapshot.Tasks[0].Locked = true
	m.currentView().State = "locked"
	m.reconcileSelection()
	if len(m.rows()) != 1 {
		t.Fatal("locked filter misses underlying running task")
	}
	for _, a := range m.actions() {
		if a.ID == "pause" || a.ID == "kill" {
			t.Fatal("locked task offered mutation")
		}
	}
	m.states["local"].Snapshot.Tasks[0].Locked = false
	m.states["local"].Snapshot.Tasks[0].Status = "paused"
	m.currentView().State = ""
	m.reconcileSelection()
	found := false
	for _, a := range m.actions() {
		if a.Label == "Resume" {
			found = true
			if !a.Target.Request.ResumeOnly {
				t.Fatal("resume could force-start a later queued task")
			}
		}
	}
	if !found {
		t.Fatal("paused task missing resume")
	}
}
