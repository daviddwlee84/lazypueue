package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

func TestLogLifecycleSearchFreezesLatePollSuccessAndFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("failure=", failure), func(t *testing.T) {
			m, b := monitorFixture(t)
			effects(m, m.addWatches())
			s := m.monitorSession()
			m.log = s
			before := s.Text
			s.Query = "same"
			b.Output = "late replacement\n"
			pending := m.pollSessions([]*logState{s})
			if pending == nil {
				t.Fatal("test did not schedule an automatic read")
			}
			reply := pending().(logPollMsg)
			if failure {
				reply.Err = errors.New("late network failure")
			}
			m.findLog(1)
			if !s.Reading {
				t.Fatal("search did not freeze the snapshot")
			}
			m.Update(reply)
			if s.Text != before || s.Err != "" || s.Pending {
				t.Fatalf("late poll changed searched snapshot: text=%q err=%q pending=%t", s.Text, s.Err, s.Pending)
			}
		})
	}
}

func TestLogLifecyclePagerReportsNaturalSourceFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := exec.CommandContext(ctx, "sh", "-c", "printf 'diagnostic line\\n'; exit 7")
	pager := exec.CommandContext(ctx, "cat")
	p := &logPager{source: source, pager: pager}
	p.SetStdout(io.Discard)
	p.SetStderr(io.Discard)
	err := p.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 7 {
		t.Fatalf("source exit 7 was hidden or replaced: %v", err)
	}
}

func TestLogLifecycleEarlyPagerCloseStopsSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := exec.CommandContext(ctx, "sh", "-c", "printf 'ready\\n'; exec sleep 30")
	pager := exec.CommandContext(ctx, "sh", "-c", "IFS= read -r line; exit 0")
	p := &logPager{source: source, pager: pager}
	p.SetStdout(io.Discard)
	p.SetStderr(io.Discard)
	started := time.Now()
	if err := p.Run(); err != nil {
		t.Fatalf("intentional pager close reported failure: %v", err)
	}
	if ctx.Err() != nil || time.Since(started) > 2*time.Second {
		t.Fatal("source was left running until the timeout")
	}
	if source.ProcessState == nil {
		t.Fatal("source process was not waited on")
	}
}

func TestLogLifecycleStalePagerReadyCannotTakeTerminal(t *testing.T) {
	for _, scenario := range []string{"sequence", "attempt", "sequence-error", "attempt-error"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := fixture(t)
			m.scope = "local"
			m.reconcileSelection()
			row, _ := m.task()
			s := m.ensureSession(row, false, false)
			m.log = s
			_ = m.startLive(s)
			m.status = "current status"
			ready := pagerReadyMsg{Command: exec.Command("true"), Sequence: m.pagerSequence, Key: s.Key, Attempt: s.Attempt}
			if strings.HasPrefix(scenario, "sequence") {
				ready.Sequence++
			} else {
				ready.Attempt += "/obsolete"
			}
			if strings.HasSuffix(scenario, "error") {
				ready.Err = errors.New("obsolete pager error")
			}
			_, cmd := m.Update(ready)
			if cmd != nil || !s.LiveStarted || m.status != "current status" {
				t.Fatal("stale pager reply changed terminal ownership, live collection, or status")
			}
		})
	}
	t.Run("current request remains usable", func(t *testing.T) {
		m, _ := fixture(t)
		m.scope = "local"
		m.reconcileSelection()
		row, _ := m.task()
		s := m.ensureSession(row, false, false)
		m.log = s
		_ = m.startLive(s)
		_, cmd := m.Update(pagerReadyMsg{Command: exec.Command("true"), Sequence: m.pagerSequence, Key: s.Key, Attempt: s.Attempt})
		if cmd == nil || s.LiveStarted {
			t.Fatal("current pager request was rejected")
		}
		// Do not execute the terminal handoff; subprocess behavior is tested above.
	})
}

func TestLogLifecycleFailedSpawnReadsDiagnosticLog(t *testing.T) {
	for _, result := range []string{"failed-to-spawn", "dependency-failed"} {
		t.Run(result, func(t *testing.T) {
			m, _ := fixture(t)
			m.scope = "local"
			b := &recordingLogs{Output: "Pueue error: failed to spawn task\n"}
			m.backend = b
			task := m.states["local"].Snapshot.Tasks[0]
			task.Status = "done"
			task.Result = result
			task.EndedAt = task.StartedAt
			m.states["local"].Snapshot.Tasks = []core.Task{task}
			m.reconcileSelection()
			effects(m, m.syncLogs())
			if result == "failed-to-spawn" {
				if len(b.Batches) != 1 || !strings.Contains(m.log.Text, "failed to spawn") {
					t.Fatal("spawn diagnostic was classified as an absent log")
				}
			} else if len(b.Batches) != 0 {
				t.Fatal("a never-run dependency failure tried to read a log")
			}
		})
	}
}

type lifecycleLogs struct {
	recordingLogs
	Lines []int
}

func (b *lifecycleLogs) Logs(ctx context.Context, c core.Connection, ids []int, lines int) (map[int]core.LogResult, error) {
	b.Lines = append(b.Lines, lines)
	return b.recordingLogs.Logs(ctx, c, ids, lines)
}

func TestLogLifecyclePausedExplicitReadSurvivesScheduler(t *testing.T) {
	m, b := monitorFixture(t)
	effects(m, m.addWatches())
	s := m.monitorSession()
	m.log = s
	s.Paused = true
	m.stopSession(s)
	b.Output = "explicit while paused\n"
	manual := m.fetchNow(s)
	effects(m, m.syncLogs())
	effects(m, manual)
	if !s.Paused || s.Text != b.Output || s.Pending || s.ExplicitRead {
		t.Fatalf("manual read altered pause state or never settled: %+v", s)
	}
	calls := len(b.Batches)
	s.NextDue = time.Time{}
	effects(m, m.syncLogs())
	if len(b.Batches) != calls {
		t.Fatal("paused collection resumed after one explicit read")
	}
}

func TestLogLifecycleLoadOlderSupersedesOldBatch(t *testing.T) {
	m, _ := monitorFixture(t)
	b := &lifecycleLogs{recordingLogs: recordingLogs{Output: "larger snapshot\n"}}
	m.backend = b
	old := m.addWatches()
	s := m.monitorSession()
	m.log = s
	newer := m.loadOlder(s)
	effects(m, m.syncLogs())
	effects(m, old)
	effects(m, newer)
	if len(b.Batches) != 1 || len(b.Lines) != 1 || b.Lines[0] < 500 {
		t.Fatalf("older-lines request lost or old batch still read: batches=%v lines=%v", b.Batches, b.Lines)
	}
	if !s.Reading || s.Text != "larger snapshot\n" || s.Pending {
		t.Fatalf("older snapshot did not settle while reading: %+v", s)
	}
}

func TestLogLifecycleEvictedSessionRejectsOldLiveCompletion(t *testing.T) {
	m, _ := fixture(t)
	m.scope = "local"
	m.reconcileSelection()
	row, _ := m.task()
	old := m.ensureSession(row, false, false)
	_ = m.startLive(old) // Construct a pending read without running any I/O.
	stale := logChunkMsg{Key: old.Key, Generation: old.Generation, Text: "obsolete stream\n", Done: true}
	m.stopSession(old)
	old.AccessedAt = time.Unix(0, 0)
	for i := 0; i < 65; i++ {
		key := fmt.Sprintf("cold-%d", i)
		m.sessions[key] = &logState{Key: key, AccessedAt: time.Now().Add(time.Duration(i) * time.Second)}
	}
	m.evictLogs()
	if _, exists := m.sessions[row.key()]; exists {
		t.Fatal("test did not evict original session")
	}
	fresh := m.ensureSession(row, false, false)
	_ = m.startLive(fresh)
	m.acceptLog(stale)
	if fresh.Text != "" || !fresh.LiveStarted {
		t.Fatal("an old completion was accepted after the same task's session was evicted and recreated")
	}
}

func TestLogLifecycleConnectionReplacementDropsCachedOutput(t *testing.T) {
	m, _ := fixture(t)
	m.scope = "local"
	m.reconcileSelection()
	row, _ := m.task()
	s := m.ensureSession(row, false, false)
	s.PreferredMode = "manual"
	s.Mode = "manual"
	s.Active = true
	s.Buffer.Replace("old endpoint output\n")
	s.Text = s.Buffer.Plain()
	m.log = s
	changed := m.cfg
	changed.Connections = append([]core.Connection(nil), m.cfg.Connections...)
	changed.Connections[0] = core.Connection{ID: "local", Name: "Replacement", Kind: "ssh", SSHHost: "new-host"}
	saved := connectionSavedMsg{Config: changed, Connection: changed.Connections[0]}
	m.Update(saved)
	oldTask := row.Task
	oldTask.Command = "different endpoint task"
	snapshot := core.Snapshot{ConnectionID: "local", Tasks: []core.Task{oldTask}, Groups: []core.Group{{Name: "default", Status: "running", Parallel: 1}}, ObservedAt: time.Now()}
	m.Update(snapshotMsg{ID: "local", Generation: m.states["local"].Generation, Snapshot: snapshot})
	current, ok := m.currentTask("local", oldTask.ID, oldTask.CreatedAt)
	if !ok {
		t.Fatal("replacement task missing")
	}
	fresh := m.ensureSession(current, false, false)
	if fresh.Text != "" {
		t.Fatalf("old endpoint's cached log retained under new connection: %q", fresh.Text)
	}
}

func TestLogLifecycleEvictionPreservesWatchedPause(t *testing.T) {
	m, _ := fixture(t)
	m.scope = "local"
	m.reconcileSelection()
	row, _ := m.task()
	s := m.ensureSession(row, false, false)
	s.Paused = true
	s.PreferredMode = "poll"
	s.PreferredInterval = "30s"
	s.Active = false
	s.AccessedAt = time.Unix(0, 0)
	m.watches = []config.WatchState{{Connection: row.Connection.ID, TaskID: row.Task.ID, CreatedAt: row.Task.CreatedAt}}
	for i := 0; i < 65; i++ {
		k := fmt.Sprint("cold-", i)
		m.sessions[k] = &logState{Key: k, AccessedAt: time.Now().Add(time.Duration(i) * time.Second)}
	}
	m.evictLogs()
	restored := m.ensureSession(row, true, false)
	if !restored.Paused || restored.Mode != "poll" || restored.Interval != 30*time.Second {
		t.Fatalf("eviction discarded collection pause/settings: %+v", restored)
	}
}
