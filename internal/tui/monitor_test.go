package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type recordingLogs struct {
	fakeBackend
	Batches     [][]int
	Connections []string
	Output      string
	Follows     int
}

func (b *recordingLogs) Logs(_ context.Context, c core.Connection, ids []int, _ int) (map[int]core.LogResult, error) {
	b.Batches = append(b.Batches, append([]int(nil), ids...))
	b.Connections = append(b.Connections, c.ID)
	result := map[int]core.LogResult{}
	for _, id := range ids {
		result[id] = core.LogResult{Output: b.Output, ObservedAt: time.Now()}
	}
	return result, nil
}
func (b *recordingLogs) Follow(ctx context.Context, _ core.Connection, _ int, _ int, emit func(string)) error {
	b.Follows++
	emit("progress: 50%\n")
	<-ctx.Done()
	return ctx.Err()
}

// Run newly scheduled effects, but do not run a returned stream read/timer.
func effects(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			effects(m, c)
		}
		return
	}
	if msg != nil {
		m.Update(msg)
	}
}
func monitorFixture(t *testing.T) (*model, *recordingLogs) {
	m, _ := fixture(t)
	b := &recordingLogs{Output: "same\nsame\nprogress: 25%\n"}
	m.backend = b
	m.scope = "local"
	m.width = 120
	m.height = 36
	s := m.states["local"]
	s.Snapshot.Tasks[1].Status = "running"
	s.Snapshot.Tasks[1].Result = ""
	s.Snapshot.Tasks[1].EndedAt = nil
	m.reconcileSelection()
	for _, row := range m.rows() {
		m.selected[row.key()] = row
	}
	return m, b
}
func TestMonitorBatchesAndHonorsCadence(t *testing.T) {
	m, b := monitorFixture(t)
	cmd := m.addWatches()
	if len(m.logPolls) != 1 {
		t.Fatal("same host not combined into one in-flight poll")
	}
	if next := m.syncLogs(); next != nil {
		t.Fatal("overlapping poll was queued")
	}
	effects(m, cmd)
	if len(b.Batches) != 1 || len(b.Batches[0]) != 2 {
		t.Fatalf("poll calls: %v", b.Batches)
	}
	for _, s := range m.sessions {
		if !strings.Contains(s.Text, "same\nsame") {
			t.Fatal("legitimate repeated lines lost")
		}
		s.NextDue = time.Time{}
	}
	effects(m, m.syncLogs())
	if len(b.Batches) != 2 {
		t.Fatal("new interval did not poll")
	}
	for _, s := range m.sessions {
		if strings.Count(s.Text, "same") != 2 {
			t.Fatal("poll snapshots were appended")
		}
		if !s.HasProgress || s.Progress.Percent != 25 {
			t.Fatal("progress not parsed from shared log read")
		}
	}
	effects(m, m.syncLogs())
	if len(b.Batches) != 2 {
		t.Fatal("polling ignored interval")
	}
}
func TestHiddenPollingCancelledAndCannotOverwriteNewPage(t *testing.T) {
	m, b := monitorFixture(t)
	m.width = 80
	m.height = 24
	for i := 0; i < 2; i++ {
		task := m.states["local"].Snapshot.Tasks[0]
		task.ID = 20 + i
		m.states["local"].Snapshot.Tasks = append(m.states["local"].Snapshot.Tasks, task)
		r := taskRow{m.cfg.Connections[0], task}
		m.selected[r.key()] = r
	}
	old := m.addWatches()
	m.monitorPage = 1
	m.monitorIndex = 2
	fresh := m.syncLogs()
	effects(m, old)
	effects(m, fresh)
	if len(b.Batches) != 1 {
		t.Fatalf("cancelled hidden page performed I/O: %v", b.Batches)
	}
	for _, id := range b.Batches[0] {
		if id < 20 {
			t.Fatal("old page was read after switching")
		}
	}
}
func TestLiveSessionSharedByDetailAndFullscreen(t *testing.T) {
	m, _ := fixture(t)
	b := &recordingLogs{}
	m.backend = b
	m.scope = "local"
	m.reconcileSelection()
	effects(m, m.syncLogs())
	if b.Follows != 1 {
		t.Fatalf("expected one live stream, got %d", b.Follows)
	}
	r, _ := m.task()
	effects(m, m.openLog(r, false))
	if b.Follows != 1 {
		t.Fatal("fullscreen spawned duplicate stream")
	}
	m.logKey(key("esc"))
	if b.Follows != 1 {
		t.Fatal("return to detail spawned stream")
	}
	if m.log.HasProgress == false {
		t.Fatal("live progress missing")
	}
}
func TestPollScrollFreezesSnapshotAndManualModeHasNoTimerReads(t *testing.T) {
	m, b := monitorFixture(t)
	effects(m, m.addWatches())
	s := m.monitorSession()
	m.log = s
	before := s.Text
	s.NextDue = time.Time{}
	inflight := m.syncLogs()
	m.scrollLog(s, -1)
	b.Output = "changed\n"
	effects(m, inflight)
	if s.Text != before {
		t.Fatal("in-flight poll changed a frozen reading snapshot")
	}
	m.changeLogMode(s, "manual", "")
	effects(m, m.syncLogs())
	calls := len(b.Batches)
	s.NextDue = time.Time{}
	effects(m, m.syncLogs())
	if len(b.Batches) != calls {
		t.Fatal("manual mode polled on timer")
	}
	effects(m, m.fetchNow(s))
	if s.Text != "changed\n" {
		t.Fatalf("manual refresh failed: %q", s.Text)
	}
}
func TestAttemptChangeClearsPreviousProgress(t *testing.T) {
	m, _ := monitorFixture(t)
	effects(m, m.addWatches())
	s := m.monitorSession()
	if !s.HasProgress {
		t.Fatal("missing progress")
	}
	state := m.states["local"]
	for i := range state.Snapshot.Tasks {
		if state.Snapshot.Tasks[i].ID == s.Task.ID {
			state.Snapshot.Tasks[i].StartedAt = nil
			state.Snapshot.Tasks[i].Status = "queued"
		}
	}
	m.updateSessionTasks("local")
	effects(m, m.syncLogs())
	if s.Text != "" || s.HasProgress {
		t.Fatal("queued retry retained prior attempt's output")
	}
}
func TestDefaultNoAndMouseCannotClickThrough(t *testing.T) {
	m, b := fixture(t)
	m.scope = "local"
	m.reconcileSelection()
	m.move(1)
	var restart action
	for _, a := range m.actions() {
		if a.ID == "restart" && !a.Target.Request.InPlace {
			restart = a
			break
		}
	}
	m.perform(restart)
	if cmd := m.overlayKey(key("enter")); cmd != nil || m.confirm != nil || b.writes != 0 {
		t.Fatal("default Enter applied mutation")
	}
	m.perform(restart)
	m.width = 120
	m.height = 36
	click := tea.MouseClickMsg{X: 7, Y: 35, Button: tea.MouseLeft}
	release := tea.MouseReleaseMsg{X: 7, Y: 35, Button: tea.MouseLeft}
	m.mouseEvent(click)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if cmd := m.mouseEvent(release); cmd != nil {
		effects(m, cmd)
	}
	if b.writes != 0 {
		t.Fatal("resize between press/release applied mutation")
	}
	m.mouse = false
	m.mouseEvent(tea.MouseClickMsg{X: 7, Y: 29, Button: tea.MouseLeft})
	m.mouseEvent(tea.MouseReleaseMsg{X: 7, Y: 29, Button: tea.MouseLeft})
	if b.writes != 0 {
		t.Fatal("disabled mouse applied mutation")
	}
}
func TestWatchPreferencesPersistAndRemainReadOnlyAcrossHosts(t *testing.T) {
	m, _ := monitorFixture(t)
	effects(m, m.addWatches())
	s := m.monitorSession()
	m.changeLogMode(s, "poll", "30s")
	s.Paused = true
	m.rememberWatch(s)
	m.monitor = false
	m.scope = "lab"
	m.reconcileSelection()
	effects(m, m.addWatches())
	if len(m.watches) != 3 {
		t.Fatalf("cross-host watches lost: %v", m.watches)
	}
	saved := m.captureState()
	if saved.Watches[0].Interval != "30s" || !saved.Watches[0].Paused {
		t.Fatal("watch overrides not saved")
	}
	if len(m.selected) != 0 {
		t.Fatal("read-only watches leaked into mutation selection")
	}
	n := newModel(m.ctx, config.Default(), "", m.backend, "")
	n.Update(stateLoadedMsg{State: saved})
	if len(n.watches) != 3 {
		t.Fatal("watch list not restored")
	}
}
func TestMonitorLayoutBoundedAtEverySize(t *testing.T) {
	m, _ := monitorFixture(t)
	effects(m, m.addWatches())
	for _, size := range [][2]int{{140, 42}, {120, 36}, {80, 24}, {38, 12}, {12, 4}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v := m.View().Content
		if rows := len(strings.Split(v, "\n")); rows > size[1] {
			t.Fatal(fmt.Sprintf("%v overflowed height: %d", size, rows))
		}
	}
}
