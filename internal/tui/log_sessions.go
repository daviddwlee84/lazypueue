package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/logview"
)

type logPollFlight struct {
	Cancel     context.CancelFunc
	Generation uint64
	Targets    map[string]uint64
}
type pollTarget struct {
	Key, Attempt string
	Generation   uint64
	ID           int
}
type logPollMsg struct {
	Connection string
	Generation uint64
	Targets    []pollTarget
	Results    map[int]core.LogResult
	Err        error
	At         time.Time
}

func (m *model) ensureSession(r taskRow, multi, progressOnly bool) *logState {
	key := r.key()
	s := m.sessions[key]
	newSession := s == nil
	if s == nil {
		s = &logState{Key: key, Connection: r.Connection, Task: r.Task, AutoScroll: true, Buffer: logview.NewBuffer(10000, 2<<20)}
		m.sessions[key] = s
	}
	if s.Attempt != "" && s.Attempt != r.Task.AttemptIdentity() {
		m.stopSession(s)
		s.Buffer.Replace("")
		s.Text = ""
		s.HasProgress = false
		s.Final = false
		s.Done = false
		s.UpdatedAt = time.Time{}
		s.NextDue = time.Time{}
		s.Offset = 0
	}
	wasTerminal := s.Task.Terminal()
	wasProgressOnly := s.ProgressOnly
	s.Attempt = r.Task.AttemptIdentity()
	s.Task = r.Task
	s.Connection = r.Connection
	s.AccessedAt = time.Now()
	s.ProgressOnly = progressOnly
	opts := m.cfg.EffectiveLogs(r.Connection)
	mode := opts.SingleMode
	if multi {
		mode = opts.MultiMode
	}
	interval, _ := time.ParseDuration(opts.PollInterval)
	if interval < time.Second {
		interval = 10 * time.Second
	}
	lines := opts.TailLines
	if lines < 1 {
		lines = 200
	}
	for _, w := range m.watches {
		if w.Connection == r.Connection.ID && w.TaskID == r.Task.ID && w.CreatedAt.Equal(r.Task.CreatedAt) {
			if newSession {
				s.Paused = w.Paused
			}
			if s.PreferredMode == "" {
				s.PreferredMode = w.Mode
			}
			if s.PreferredInterval == "" {
				s.PreferredInterval = w.Interval
			}
			if s.Lines == 0 && w.Lines > 0 {
				s.Lines = w.Lines
			}
			break
		}
	}
	if s.PreferredMode != "" {
		mode = s.PreferredMode
	}
	if mode == "" || mode == "auto" {
		mode = "live"
		if r.Connection.Kind == "ssh" {
			mode = "poll"
		}
	}
	if s.PreferredInterval != "" {
		if d, e := time.ParseDuration(s.PreferredInterval); e == nil && d >= time.Second {
			interval = d
		}
	}
	if progressOnly {
		if mode != "manual" {
			mode = "poll"
		}
		lines = 20
		interval = max(interval, 10*time.Second)
	}
	if newSession || wasProgressOnly != progressOnly {
		maxLines, maxBytes := 10000, 2<<20
		if progressOnly {
			maxLines, maxBytes = 20, 64<<10
		}
		buffer := logview.NewBuffer(maxLines, maxBytes)
		buffer.Replace(s.Text)
		s.Buffer = buffer
		s.Text = buffer.Plain()
		s.NextDue = time.Time{}
		if !progressOnly {
			s.Lines = lines
		}
	}
	if s.Mode != "" && s.Mode != mode {
		m.stopSession(s)
		s.NextDue = time.Time{}
	}
	s.Mode = mode
	s.Interval = interval
	s.Following = mode == "live"
	if s.Lines == 0 || progressOnly {
		s.Lines = lines
	}
	if !wasTerminal && r.Task.Terminal() && !s.LiveStarted {
		s.Final = false
		s.NextDue = time.Time{}
	}
	return s
}
func (m *model) wantedLogs() map[string]*logState {
	wanted := map[string]*logState{}
	if m.width < 16 || m.height < 10 {
		return wanted
	}
	if m.taskForm != nil || m.connectionForm != nil || m.editForm != nil {
		return wanted
	}
	if m.monitor {
		if m.log.Open {
			wanted[m.log.Key] = m.log
			return wanted
		}
		start, end := m.monitorRange()
		for _, w := range m.watches[start:end] {
			r, ok := m.watchRow(w)
			if !ok {
				continue
			}
			s := m.ensureSession(r, true, false)
			wanted[s.Key] = s
		}
		return wanted
	}
	if m.log.Open {
		if r, ok := m.currentTask(m.log.Connection.ID, m.log.Task.ID, m.log.Task.CreatedAt); ok {
			s := m.ensureSession(r, false, false)
			wanted[s.Key] = s
		}
		return wanted
	}
	if m.tab != 0 {
		return wanted
	}
	if r, ok := m.task(); ok {
		s := m.ensureSession(r, false, false)
		m.log = s
		wanted[s.Key] = s
	}
	for _, r := range m.visibleTaskRows() {
		if r.Task.State() != "running" {
			continue
		}
		if _, ok := wanted[r.key()]; ok {
			continue
		}
		s := m.ensureSession(r, false, true)
		wanted[s.Key] = s
	}
	return wanted
}
func hasLog(t core.Task) bool {
	result := strings.ToLower(t.Result)
	return (t.StartedAt != nil || t.State() == "running" || t.State() == "paused" || strings.Contains(result, "spawn")) && !strings.Contains(result, "dependency")
}
func (m *model) syncLogs() tea.Cmd {
	wanted := m.wantedLogs()
	now := time.Now()
	var cmds []tea.Cmd
	due := map[string][]*logState{}
	for key, s := range m.sessions {
		_, visible := wanted[key]
		if !visible {
			if s.Active {
				m.stopSession(s)
			}
			continue
		}
		s.Active = true
		if s.Paused || s.Reading {
			if s.LiveStarted {
				m.stopSession(s)
				s.Active = true
			}
			continue
		}
		if !hasLog(s.Task) {
			if s.LiveStarted {
				m.stopSession(s)
				s.Active = true
			}
			if s.Task.State() == "queued" || s.Task.State() == "stashed" {
				s.Buffer.Replace("")
				s.Text = ""
				s.HasProgress = false
			}
			continue
		}
		if s.Pending || s.LiveStarted || s.Final || now.Before(s.NextDue) {
			continue
		}
		if s.Mode == "manual" {
			continue
		}
		if s.Mode == "live" && s.Task.State() == "running" {
			cmds = append(cmds, m.startLive(s))
			continue
		}
		due[s.Connection.ID] = append(due[s.Connection.ID], s)
	}
	for id, flight := range m.logPolls {
		any := false
		for key, s := range wanted {
			if generation, ok := flight.Targets[key]; ok && generation == s.Generation && s.Connection.ID == id && ((!s.Paused && !s.Reading) || s.ExplicitRead) {
				any = true
				break
			}
		}
		if !any {
			flight.Cancel()
			delete(m.logPolls, id)
		}
	}
	for _, sessions := range due {
		sort.Slice(sessions, func(i, j int) bool {
			if !sessions[i].NextDue.Equal(sessions[j].NextDue) {
				return sessions[i].NextDue.Before(sessions[j].NextDue)
			}
			if sessions[i].ProgressOnly != sessions[j].ProgressOnly {
				return !sessions[i].ProgressOnly
			}
			return sessions[i].Key < sessions[j].Key
		})
		lines := sessions[0].Lines
		batch := make([]*logState, 0, 16)
		for _, s := range sessions {
			if s.Lines == lines {
				batch = append(batch, s)
				if len(batch) == 16 {
					break
				}
			}
		}
		cmds = append(cmds, m.pollSessions(batch))
	}
	m.evictLogs()
	return tea.Batch(cmds...)
}
func (m *model) pollSessions(sessions []*logState) tea.Cmd {
	if len(sessions) == 0 {
		return nil
	}
	c := sessions[0].Connection
	if m.logPolls[c.ID] != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.logPollSequence++
	sequence := m.logPollSequence
	flight := &logPollFlight{Cancel: cancel, Generation: sequence, Targets: map[string]uint64{}}
	m.logPolls[c.ID] = flight
	var targets []pollTarget
	var ids []int
	lines := 0
	for _, s := range sessions {
		if s.Pending || s.LiveStarted {
			continue
		}
		s.Pending = true
		s.Active = true
		s.Generation = m.nextLogGeneration()
		targets = append(targets, pollTarget{s.Key, s.Attempt, s.Generation, s.Task.ID})
		flight.Targets[s.Key] = s.Generation
		ids = append(ids, s.Task.ID)
		lines = max(lines, s.Lines)
	}
	if len(ids) == 0 {
		cancel()
		delete(m.logPolls, c.ID)
		return nil
	}
	return func() tea.Msg {
		defer cancel()
		if ctx.Err() != nil {
			return logPollMsg{Connection: c.ID, Generation: sequence, Targets: targets, Err: ctx.Err(), At: time.Now()}
		}
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			return logPollMsg{Connection: c.ID, Generation: sequence, Targets: targets, Err: ctx.Err(), At: time.Now()}
		}
		defer func() { <-m.sem }()
		var results map[int]core.LogResult
		var err error
		if b, ok := m.backend.(core.BatchLogger); ok {
			results, err = b.Logs(ctx, c, ids, lines)
		} else {
			results = map[int]core.LogResult{}
			for _, id := range ids {
				text, e := m.backend.Log(ctx, c, id, lines)
				r := core.LogResult{Output: text, ObservedAt: time.Now()}
				if e != nil {
					r.Error = e.Error()
				}
				results[id] = r
			}
		}
		return logPollMsg{c.ID, sequence, targets, results, err, time.Now()}
	}
}
func (m *model) acceptPoll(v logPollMsg) tea.Cmd {
	flight := m.logPolls[v.Connection]
	if flight == nil || flight.Generation != v.Generation {
		return nil
	}
	delete(m.logPolls, v.Connection)
	for _, target := range v.Targets {
		s := m.sessions[target.Key]
		if s == nil || s.Generation != target.Generation || s.Attempt != target.Attempt || !s.Active {
			continue
		}
		s.Pending = false
		s.ExplicitRead = false
		s.CheckedAt = v.At
		s.NextDue = v.At.Add(s.Interval)
		if v.Err != nil {
			if v.Err != context.Canceled {
				s.Err = v.Err.Error()
			}
			continue
		}
		r, ok := v.Results[target.ID]
		if !ok {
			s.Err = "Task log is unavailable"
			continue
		}
		if r.Error != "" {
			s.Err = r.Error
			continue
		}
		if r.Task != nil && (!r.Task.CreatedAt.Equal(s.Task.CreatedAt) || r.Task.AttemptIdentity() != s.Attempt) {
			s.Err = "Task attempt changed; waiting for a new status snapshot"
			continue
		}
		s.Err = ""
		s.UpdatedAt = v.At
		if s.Text != r.Output {
			s.Buffer.Replace(r.Output)
			s.Text = s.Buffer.Plain()
			m.parseSessionProgress(s)
		}
		s.Final = s.Task.Terminal()
		s.Done = s.Final
		if s == m.log {
			m.previewText = s.Text
			m.previewError = ""
			m.previewPending = false
			m.previewKey = s.Key
		}
	}
	m.evictLogs()
	return nil
}
func (m *model) fetchNow(s *logState) tea.Cmd {
	if flight := m.logPolls[s.Connection.ID]; flight != nil {
		flight.Cancel()
		delete(m.logPolls, s.Connection.ID)
		for key, generation := range flight.Targets {
			if pending := m.sessions[key]; pending != nil && pending.Generation == generation {
				pending.Generation = m.nextLogGeneration()
				pending.Pending = false
				pending.ExplicitRead = false
				pending.NextDue = time.Time{}
			}
		}
	}
	s.NextDue = time.Time{}
	s.Final = false
	s.Active = true
	s.ExplicitRead = true
	if s.Mode == "live" && !s.Paused && !s.Reading && s.Task.State() == "running" {
		if s.LiveStarted {
			m.stopSession(s)
		}
		return m.startLive(s)
	}
	return m.pollSessions([]*logState{s})
}
func (m *model) changeLogMode(s *logState, mode, interval string) {
	m.stopSession(s)
	s.Mode = mode
	s.PreferredMode = mode
	s.Paused = false
	s.Reading = false
	s.AutoScroll = true
	s.Done = false
	s.Final = false
	s.NextDue = time.Time{}
	if interval != "" {
		s.PreferredInterval = interval
		s.Interval, _ = time.ParseDuration(interval)
	}
	m.rememberWatch(s)
}
func (m *model) rememberWatch(s *logState) {
	for i := range m.watches {
		w := &m.watches[i]
		if w.Connection == s.Connection.ID && w.TaskID == s.Task.ID && w.CreatedAt.Equal(s.Task.CreatedAt) {
			w.Mode = s.PreferredMode
			w.Interval = s.PreferredInterval
			w.Lines = s.Lines
			w.Paused = s.Paused
		}
	}
}
func (m *model) currentTask(connection string, id int, created time.Time) (taskRow, bool) {
	c, ok := m.connection(connection)
	if !ok {
		return taskRow{}, false
	}
	state := m.states[connection]
	if state == nil {
		return taskRow{}, false
	}
	for _, t := range state.Snapshot.Tasks {
		if t.ID == id && t.CreatedAt.Equal(created) {
			return taskRow{c, t}, true
		}
	}
	return taskRow{}, false
}
func (m *model) watchRow(w config.WatchState) (taskRow, bool) {
	return m.currentTask(w.Connection, w.TaskID, w.CreatedAt)
}
func (m *model) addWatches() tea.Cmd {
	rows := m.actionRows()
	if len(rows) == 0 {
		m.note("Select a task to add to Monitor.")
		return nil
	}
	for _, r := range rows {
		found := false
		for _, w := range m.watches {
			if w.Connection == r.Connection.ID && w.TaskID == r.Task.ID && w.CreatedAt.Equal(r.Task.CreatedAt) {
				found = true
				break
			}
		}
		if !found {
			m.watches = append(m.watches, config.WatchState{Connection: r.Connection.ID, TaskID: r.Task.ID, CreatedAt: r.Task.CreatedAt})
		}
	}
	m.selected = map[string]taskRow{}
	m.monitor = true
	m.monitorIndex = max(0, len(m.watches)-len(rows))
	m.monitorPage = m.monitorIndex / m.monitorCapacity()
	m.log.Open = false
	m.overlay = ""
	return m.syncLogs()
}
func (m *model) monitorCapacity() int {
	if m.width >= 120 && m.height >= 30 {
		return 4
	}
	if m.width >= 80 && m.height >= 20 {
		return 2
	}
	return 1
}
func (m *model) monitorRange() (int, int) {
	capacity := m.monitorCapacity()
	page := clamp(m.monitorPage, 0, max(0, (len(m.watches)-1)/capacity))
	start := min(len(m.watches), page*capacity)
	return start, min(len(m.watches), start+capacity)
}
func (m *model) monitorSession() *logState {
	if m.monitorIndex < 0 || m.monitorIndex >= len(m.watches) {
		return nil
	}
	r, ok := m.watchRow(m.watches[m.monitorIndex])
	if !ok {
		return nil
	}
	return m.ensureSession(r, true, false)
}
func (m *model) monitorKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	start, end := m.monitorRange()
	switch key {
	case "q", "esc":
		m.monitor = false
		return m.syncLogs()
	case "ctrl+c":
		return tea.Quit
	case "0", "1", "2", "3", "4":
		return m.directPane(key)
	case "tab", "right", "l":
		if end > start {
			m.monitorIndex = start + (m.monitorIndex-start+1)%(end-start)
		}
		return nil
	case "shift+tab", "left", "h":
		if end > start {
			m.monitorIndex = start + (m.monitorIndex-start+end-start-1)%(end-start)
		}
		return nil
	case "]":
		m.monitorPage = min(max(0, (len(m.watches)-1)/m.monitorCapacity()), m.monitorPage+1)
		m.monitorIndex = m.monitorPage * m.monitorCapacity()
		return m.syncLogs()
	case "[":
		m.monitorPage = max(0, m.monitorPage-1)
		m.monitorIndex = m.monitorPage * m.monitorCapacity()
		return m.syncLogs()
	case "x":
		if m.monitorIndex >= 0 && m.monitorIndex < len(m.watches) {
			w := m.watches[m.monitorIndex]
			if r, ok := m.watchRow(w); ok {
				if s := m.sessions[r.key()]; s != nil {
					m.stopSession(s)
				}
			}
			m.watches = append(m.watches[:m.monitorIndex], m.watches[m.monitorIndex+1:]...)
			m.monitorIndex = clamp(m.monitorIndex, 0, len(m.watches)-1)
			m.monitorPage = m.monitorIndex / m.monitorCapacity()
		}
		return m.syncLogs()
	case "a":
		m.monitor = false
		m.focus = 1
		m.note("Select tasks with Space, then W adds them to the watchlist. Existing watches are retained.")
		return m.syncLogs()
	case "m":
		m.mouse = !m.mouse
		m.pressed = nil
		return nil
	case "?":
		m.overlay = "help"
		m.menuIndex = 0
		return nil
	}
	s := m.monitorSession()
	if s == nil {
		return nil
	}
	m.log = s
	if key == "enter" {
		s.Open = true
		return m.syncLogs()
	}
	return m.logKey(k)
}
func (m *model) directPane(key string) tea.Cmd {
	m.log.Open = false
	m.inputMode = ""
	m.overlay = ""
	if key == "4" {
		m.monitor = true
		m.monitorIndex = clamp(m.monitorIndex, 0, len(m.watches)-1)
		m.monitorPage = m.monitorIndex / m.monitorCapacity()
		return m.syncLogs()
	}
	if key == "3" && m.monitor {
		if s := m.monitorSession(); s != nil {
			m.log = s
			s.Open = true
		}
		return m.syncLogs()
	}
	m.monitor = false
	switch key {
	case "0":
		m.focus = 0
	case "1":
		m.tab = 0
		m.focus = 1
	case "2":
		m.tab = 1
		m.focus = 1
	case "3":
		m.focus = 2
	}
	m.reconcileSelection()
	return m.syncLogs()
}
func (m *model) logSettingsKeypress(k tea.KeyPressMsg) tea.Cmd {
	s := m.sessions[m.logSettingsKey]
	if s == nil {
		m.overlay = ""
		return nil
	}
	choices := []string{"live", "poll", "manual", "2s", "5s", "10s", "30s", "1m", "custom"}
	switch k.String() {
	case "up", "k":
		m.menuIndex = max(0, m.menuIndex-1)
	case "down", "j":
		m.menuIndex = min(len(choices)-1, m.menuIndex+1)
	case "enter":
		choice := choices[m.menuIndex]
		m.overlay = ""
		if choice == "custom" {
			m.inputMode = "log-interval"
			m.input.Prompt = "Log poll interval (e.g. 10s, 2m): "
			m.input.SetValue(s.Interval.String())
			return m.input.Focus()
		}
		if m.menuIndex < 3 {
			m.changeLogMode(s, choice, "")
		} else {
			m.changeLogMode(s, "poll", choice)
		}
		return m.syncLogs()
	}
	return nil
}
func (m *model) evictLogs() {
	cost := func(s *logState) int {
		return s.Buffer.BufferedBytes() + len(s.Text) + len(s.Task.Command) + len(s.Task.OriginalCommand) + len(s.Task.Path) + len(s.Task.Label) + len(s.Task.Error) + len(s.Progress.Source)
	}
	total := 0
	var cold []*logState
	for _, s := range m.sessions {
		total += cost(s)
		if !s.Active {
			cold = append(cold, s)
		}
	}
	sort.Slice(cold, func(i, j int) bool { return cold[i].AccessedAt.Before(cold[j].AccessedAt) })
	remaining := len(cold)
	for _, s := range cold {
		if total <= 32<<20 && remaining <= 64 {
			break
		}
		m.stopSession(s)
		m.rememberWatch(s)
		total -= cost(s)
		remaining--
		delete(m.sessions, s.Key)
		if m.log == s {
			m.log = &logState{}
		}
	}
	if total > 32<<20 {
		for _, s := range m.sessions {
			if !s.ProgressOnly {
				continue
			}
			oldCost := cost(s)
			s.Buffer = logview.NewBuffer(20, 64<<10)
			s.Text = ""
			total -= oldCost - cost(s)
			if total <= 32<<20 {
				break
			}
		}
	}
}

func (m *model) logModeLabel(s *logState) string {
	mode := s.Mode
	if s.Mode == "poll" {
		mode = "poll " + s.Interval.String()
	}
	if s.Paused {
		return mode + " · collection paused"
	}
	if s.Reading {
		return mode + " · reading snapshot · G resumes"
	}
	if s.Final {
		return mode + " · final output"
	}
	return mode
}
func (m *model) logFreshness(s *logState) string {
	if s.Err != "" {
		return "STALE: " + clean(s.Err)
	}
	if s.Pending {
		return "loading…"
	}
	if s.UpdatedAt.IsZero() {
		return "no output observed"
	}
	return fmt.Sprintf("updated %s ago", duration(time.Since(s.UpdatedAt)))
}
