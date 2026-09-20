package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type volumeCall struct {
	connection string
	ids        []int
	lines      int
	release    chan struct{}
}

// Counts returned task-output bytes, not JSON, SSH, or TLS framing. A quiet
// Follow returns its initial tail once; every Logs call returns that same tail.
// Explicit gates make slow remote calls observable without sleeping for polling
// intervals or depending on a real daemon/network.
type volumeBackend struct {
	fakeBackend
	mu                sync.Mutex
	tail              string
	started           chan *volumeCall
	followStarted     chan struct{}
	calls             []*volumeCall
	active, peak      int
	perHost, peakHost map[string]int
	outputBytes       int
	followBytes       int
	followCalls       int
}

func newVolumeBackend(tail string) *volumeBackend {
	return &volumeBackend{tail: tail, started: make(chan *volumeCall, 32), followStarted: make(chan struct{}, 32), perHost: map[string]int{}, peakHost: map[string]int{}}
}

func (b *volumeBackend) Logs(ctx context.Context, c core.Connection, ids []int, lines int) (map[int]core.LogResult, error) {
	call := &volumeCall{connection: c.ID, ids: append([]int(nil), ids...), lines: lines, release: make(chan struct{})}
	b.mu.Lock()
	b.calls = append(b.calls, call)
	b.active++
	b.peak = max(b.peak, b.active)
	b.perHost[c.ID]++
	b.peakHost[c.ID] = max(b.peakHost[c.ID], b.perHost[c.ID])
	b.mu.Unlock()
	defer func() { b.mu.Lock(); b.active--; b.perHost[c.ID]--; b.mu.Unlock() }()
	b.started <- call
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.release:
	}
	result := map[int]core.LogResult{}
	for _, id := range ids {
		result[id] = core.LogResult{Output: b.tail, ObservedAt: time.Now()}
	}
	b.mu.Lock()
	b.outputBytes += len(ids) * len(b.tail)
	b.mu.Unlock()
	return result, nil
}

func (b *volumeBackend) Follow(ctx context.Context, _ core.Connection, _ int, _ int, emit func(string)) error {
	emit(b.tail)
	b.mu.Lock()
	b.followCalls++
	b.followBytes += len(b.tail)
	b.mu.Unlock()
	b.followStarted <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

func (b *volumeBackend) counts() (calls, active, peak, outputBytes, follows, followBytes int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls), b.active, b.peak, b.outputBytes, b.followCalls, b.followBytes
}

func volumeFixture(t *testing.T, hosts, tasks int, interval string) (*model, *volumeBackend, map[string][]*logState) {
	t.Helper()
	b := newVolumeBackend(strings.Repeat("unchanged task output\n", 200))
	cfg := config.Default()
	cfg.Connections = nil
	cfg.Logs.SingleMode, cfg.Logs.MultiMode, cfg.Logs.PollInterval = "poll", "poll", interval
	for i := 0; i < hosts; i++ {
		cfg.Connections = append(cfg.Connections, core.Connection{ID: fmt.Sprintf("host-%02d", i), Kind: "ssh", SSHHost: fmt.Sprintf("fixture-%02d", i)})
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := newModel(ctx, cfg, "", b, "all")
	m.width, m.height = 160, 48
	t.Cleanup(func() { cancel(); m.close() })
	created := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC)
	started := created.Add(time.Second)
	byHost := map[string][]*logState{}
	for _, c := range cfg.Connections {
		snapshot := core.Snapshot{ConnectionID: c.ID, ObservedAt: time.Now(), Groups: []core.Group{{Name: "default", Status: "running", Parallel: tasks}}}
		for i := 0; i < tasks; i++ {
			task := core.Task{ID: i + 1, Command: "long-running fixture", Group: "default", Status: "running", CreatedAt: created.Add(time.Duration(i) * time.Second), StartedAt: &started}
			snapshot.Tasks = append(snapshot.Tasks, task)
			session := m.ensureSession(taskRow{c, task}, true, false)
			byHost[c.ID] = append(byHost[c.ID], session)
			m.watches = append(m.watches, config.WatchState{Connection: c.ID, TaskID: task.ID, CreatedAt: task.CreatedAt})
		}
		m.states[c.ID].Snapshot = snapshot
	}
	m.monitor = true
	m.reconcileSelection()
	return m, b, byHost
}

// Command goroutines only execute effects; the test goroutine applies messages
// to the model, matching Bubble Tea's single-owner Update contract.
func runVolumeEffect(cmd tea.Cmd, results chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, child := range batch {
				runVolumeEffect(child, results)
			}
		} else if msg != nil {
			results <- msg
		}
	}()
}

func volumeStarted(t *testing.T, b *volumeBackend) *volumeCall {
	t.Helper()
	select {
	case call := <-b.started:
		return call
	case <-time.After(3 * time.Second):
		t.Fatal("poll worker did not reach the blocking batch logger")
		return nil
	}
}

func volumeResult(t *testing.T, results <-chan tea.Msg) logPollMsg {
	t.Helper()
	select {
	case msg := <-results:
		poll, ok := msg.(logPollMsg)
		if !ok {
			t.Fatalf("unexpected effect result %T", msg)
		}
		if poll.Err != nil {
			t.Fatalf("poll failed: %v", poll.Err)
		}
		return poll
	case <-time.After(3 * time.Second):
		t.Fatal("released poll worker did not complete")
		return logPollMsg{}
	}
}

func TestPollingResourceBoundAcrossConnections(t *testing.T) {
	m, b, byHost := volumeFixture(t, 8, 2, "10s")
	results := make(chan tea.Msg, 32)
	for _, c := range m.cfg.Connections {
		cmd := m.pollSessions(byHost[c.ID])
		if cmd == nil {
			t.Fatalf("first poll for %s was not scheduled", c.ID)
		}
		runVolumeEffect(cmd, results)
	}
	initial := make([]*volumeCall, 4)
	for i := range initial {
		initial[i] = volumeStarted(t, b)
		if len(initial[i].ids) != 2 {
			t.Fatalf("connection was split into per-task calls: %+v", initial[i])
		}
	}
	// This is only a short asynchronous admission check. All cadence tests below
	// advance NextDue explicitly; no polling interval is waited out.
	select {
	case extra := <-b.started:
		t.Fatalf("fifth worker entered while four hosts were blocked: %s", extra.connection)
	case <-time.After(40 * time.Millisecond):
	}
	for _, c := range m.cfg.Connections {
		if m.pollSessions(byHost[c.ID]) != nil {
			t.Fatalf("another flight was queued for pending connection %s", c.ID)
		}
	}
	if calls, active, peak, _, _, _ := b.counts(); calls != 4 || active != 4 || peak != 4 {
		t.Fatalf("admission counts: calls=%d active=%d peak=%d", calls, active, peak)
	}
	close(initial[0].release)
	fifth := volumeStarted(t, b)
	if _, active, peak, _, _, _ := b.counts(); active != 4 || peak != 4 {
		t.Fatalf("released capacity was not reused within the bound: active=%d peak=%d", active, peak)
	}
	for _, call := range initial[1:] {
		close(call.release)
	}
	close(fifth.release)
	for i := 0; i < 3; i++ {
		close(volumeStarted(t, b).release)
	}
	for i := 0; i < 8; i++ {
		m.acceptPoll(volumeResult(t, results))
	}
	if calls, active, peak, output, _, _ := b.counts(); calls != 8 || active != 0 || peak != 4 || output != 16*len(b.tail) {
		t.Fatalf("finished counts: calls=%d active=%d peak=%d bytes=%d", calls, active, peak, output)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for host, peak := range b.peakHost {
		if peak != 1 {
			t.Errorf("connection %s had %d simultaneous flights", host, peak)
		}
	}
}

func TestPollingCadenceDoesNotQueueCatchUpAndRetransmitsItsTail(t *testing.T) {
	m, b, byHost := volumeFixture(t, 1, 2, "1h")
	results := make(chan tea.Msg, 16)
	followCtx, stopFollow := context.WithCancel(context.Background())
	var followWorkers sync.WaitGroup
	for _, session := range byHost["host-00"] {
		followWorkers.Add(1)
		go func(s *logState) {
			defer followWorkers.Done()
			_ = b.Follow(followCtx, s.Connection, s.Task.ID, 200, func(string) {})
		}(session)
	}
	t.Cleanup(func() { stopFollow(); followWorkers.Wait() })
	for i := 0; i < 2; i++ {
		select {
		case <-b.followStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("quiet follow fixture did not emit its initial tail")
		}
	}
	runVolumeEffect(m.syncLogs(), results)
	first := volumeStarted(t, b)
	for i := 0; i < 40; i++ {
		for _, s := range byHost["host-00"] {
			s.NextDue = time.Now().Add(-100 * time.Hour)
		}
		if m.syncLogs() != nil {
			t.Fatal("timer ticks queued another poll behind an in-flight read")
		}
	}
	close(first.release)
	firstResult := volumeResult(t, results)
	m.acceptPoll(firstResult)
	for _, s := range byHost["host-00"] {
		if !s.NextDue.Equal(firstResult.At.Add(time.Hour)) {
			t.Fatalf("next poll was scheduled from missed ticks instead of completion: %s", s.NextDue)
		}
	}
	for i := 0; i < 50; i++ {
		if m.syncLogs() != nil {
			t.Fatal("a one-hour interval performed an extra read while not due")
		}
	}
	if calls, _, _, output, follows, followed := b.counts(); calls != 1 || follows != 2 || output != 2*len(b.tail) || followed != output {
		t.Fatalf("initial payload counts: polls=%d follows=%d poll bytes=%d follow bytes=%d", calls, follows, output, followed)
	}
	// Simulate a much-overdue timer. It should generate one new snapshot, not
	// one read for each elapsed interval, and retransmit unchanged output.
	for _, s := range byHost["host-00"] {
		s.NextDue = firstResult.At.Add(-100 * time.Hour)
	}
	runVolumeEffect(m.syncLogs(), results)
	second := volumeStarted(t, b)
	if len(second.ids) != 2 || second.lines != 200 {
		t.Fatalf("second interval lost batching or changed its tail bound: %+v", second)
	}
	for i := 0; i < 20; i++ {
		if m.syncLogs() != nil {
			t.Fatal("overdue intervals created a catch-up queue")
		}
	}
	close(second.release)
	secondResult := volumeResult(t, results)
	m.acceptPoll(secondResult)
	if m.syncLogs() != nil {
		t.Fatal("completion immediately scheduled a catch-up read")
	}
	for _, s := range byHost["host-00"] {
		if s.Text != b.tail || !s.NextDue.Equal(secondResult.At.Add(time.Hour)) {
			t.Fatal("snapshot polling appended old output or drifted its completion-based cadence")
		}
	}
	calls, _, peak, output, follows, followed := b.counts()
	if calls != 2 || peak != 1 || follows != 2 || output != 4*len(b.tail) || followed != 2*len(b.tail) {
		t.Fatalf("quiet output accounting: polls=%d peak=%d follow calls=%d poll bytes=%d follow bytes=%d", calls, peak, follows, output, followed)
	}
	t.Logf("two unchanged batched snapshots returned %d task-output bytes; quiet follows returned %d initial-tail bytes. Polling bounds cadence/concurrency, not necessarily transferred bytes.", output, followed)
}
