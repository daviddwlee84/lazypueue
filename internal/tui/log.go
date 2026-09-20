package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/logview"
)

type logState struct {
	Open, Following, AutoScroll, Done                                  bool
	Connection                                                         core.Connection
	Task                                                               core.Task
	Key, Attempt                                                       string
	Generation                                                         uint64
	Text, Err, Query                                                   string
	Offset                                                             int
	Cancel                                                             context.CancelFunc
	Channel                                                            <-chan logChunkMsg
	Context                                                            context.Context
	Buffer                                                             *logview.Buffer
	Mode, PreferredMode                                                string
	Interval                                                           time.Duration
	PreferredInterval                                                  string
	Lines                                                              int
	Paused, Reading, Active, Pending, LiveStarted, Final, ProgressOnly bool
	CheckedAt, UpdatedAt, NextDue, AccessedAt                          time.Time
	Progress                                                           logview.Progress
	HasProgress                                                        bool
	ExplicitRead                                                       bool
}
type logChunkMsg struct {
	Key        string
	Generation uint64
	Text       string
	Done       bool
	Err        error
}
type reportMsg struct {
	Text string
	Err  error
}

func cleanLog(s string) string { return logview.Sanitize(s) }
func (m *model) nextLogGeneration() uint64 {
	m.logSessionSequence++
	return m.logSessionSequence
}
func (m *model) discardConnectionLogs(id string) {
	if flight := m.logPolls[id]; flight != nil {
		flight.Cancel()
		delete(m.logPolls, id)
	}
	for key, s := range m.sessions {
		if s.Connection.ID == id {
			m.stopSession(s)
			delete(m.sessions, key)
		}
	}
	if m.log != nil && m.log.Connection.ID == id {
		m.log = &logState{}
	}
	m.pagerSequence++
}
func (m *model) stopLog() {
	if m.log != nil {
		m.log.Open = false
	}
}
func (m *model) stopSession(s *logState) {
	s.ExplicitRead = false
	if s.Cancel != nil {
		s.Cancel()
		s.Cancel = nil
	}
	s.Generation = m.nextLogGeneration()
	s.Active = false
	s.LiveStarted = false
	s.Pending = false
}
func (m *model) openLog(r taskRow, follow bool) tea.Cmd {
	if m.log != nil {
		m.log.Open = false
	}
	s := m.ensureSession(r, m.monitor, false)
	m.log = s
	s.Open = true
	if follow {
		m.changeLogMode(s, "live", "")
	}
	return m.syncLogs()
}
func (m *model) startLive(s *logState) tea.Cmd {
	ctx, cancel := context.WithCancel(m.ctx)
	s.Cancel = cancel
	s.Context = ctx
	s.Generation = m.nextLogGeneration()
	gen := s.Generation
	s.LiveStarted = true
	s.ExplicitRead = false
	s.Pending = true
	s.Following = true
	s.Done = false
	s.Err = ""
	s.Active = true
	s.Buffer = logview.NewBuffer(10000, 2<<20)
	s.Text = ""
	s.Offset = 0
	ch := make(chan logChunkMsg, 16)
	s.Channel = ch
	c, id, lines, key := s.Connection, s.Task.ID, s.Lines, s.Key
	return func() tea.Msg {
		if ctx.Err() != nil {
			return nil
		}
		go func() {
			defer close(ch)
			err := m.backend.Follow(ctx, c, id, lines, func(text string) {
				select {
				case ch <- logChunkMsg{Key: key, Generation: gen, Text: text}:
				case <-ctx.Done():
				}
			})
			select {
			case ch <- logChunkMsg{Key: key, Generation: gen, Done: true, Err: err}:
			case <-ctx.Done():
			}
		}()
		select {
		case msg, ok := <-ch:
			if ok {
				return msg
			}
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) acceptLog(v logChunkMsg) tea.Cmd {
	s := m.sessions[v.Key]
	if s == nil && m.log != nil && m.log.Key == v.Key {
		s = m.log
	}
	if s == nil || s.Generation != v.Generation || (!s.Active && !s.Open) {
		return nil
	}
	if s.Buffer == nil {
		s.Buffer = logview.NewBuffer(10000, 2<<20)
	}
	s.Pending = false
	s.CheckedAt = time.Now()
	if v.Text != "" {
		s.Buffer.Append(v.Text)
		s.Text = s.Buffer.Plain()
		s.UpdatedAt = s.CheckedAt
		m.parseSessionProgress(s)
	}
	if v.Err != nil && v.Err != context.Canceled {
		s.Err = v.Err.Error()
	}
	if v.Done {
		s.Done = true
		s.LiveStarted = false
		s.Final = s.Task.Terminal() && v.Err == nil
		s.NextDue = time.Now().Add(s.Interval)
		return nil
	}
	m.evictLogs()
	ch, ctx := s.Channel, s.Context
	return func() tea.Msg {
		select {
		case next, ok := <-ch:
			if ok {
				return next
			}
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}
func (m *model) parseSessionProgress(s *logState) {
	text := s.Text
	if len(text) > 64<<10 {
		text = text[len(text)-(64<<10):]
	}
	if p, ok := logview.ParseProgress(text, s.UpdatedAt); ok {
		s.Progress = p
		s.HasProgress = true
	}
}
func (m *model) logLines() []string {
	if m.log == nil {
		return nil
	}
	return strings.Split(cleanLog(m.log.Text), "\n")
}
func (m *model) logCapacity() int {
	if m.log.Open {
		return max(1, m.height-9)
	}
	if m.monitor {
		for _, r := range m.monitorRects() {
			return max(1, r.H-4)
		}
	}
	return max(1, m.geometry().Detail.H-5)
}
func (m *model) scrollLog(s *logState, delta int) {
	if s == nil {
		return
	}
	capacity := m.logCapacity()
	last := max(0, len(strings.Split(s.Text, "\n"))-capacity)
	if s.AutoScroll {
		s.Offset = last
	}
	s.AutoScroll = false
	s.Offset = clamp(s.Offset+delta, 0, last)
	if s.Mode == "poll" {
		if !s.Reading {
			m.stopSession(s)
			s.Active = true
		}
		s.Reading = true
	}
}
func (m *model) logKey(k tea.KeyPressMsg) tea.Cmd {
	if m.log == nil {
		return nil
	}
	s := m.log
	key := k.String()
	capacity := m.logCapacity()
	switch key {
	case "esc", "q", "ctrl+c":
		m.stopLog()
		return m.syncLogs()
	case "up", "k":
		m.scrollLog(s, -1)
	case "down", "j":
		m.scrollLog(s, 1)
	case "pgup", "ctrl+b":
		if s.Offset == 0 && !s.AutoScroll {
			return m.loadOlder(s)
		}
		m.scrollLog(s, -capacity)
	case "pgdown", "ctrl+f":
		m.scrollLog(s, capacity)
	case "ctrl+u":
		m.scrollLog(s, -max(1, capacity/2))
	case "ctrl+d":
		m.scrollLog(s, max(1, capacity/2))
	case "home", "g":
		m.scrollLog(s, -1<<30)
	case "end", "G":
		s.AutoScroll = true
		s.Reading = false
		s.NextDue = time.Time{}
		return m.syncLogs()
	case "f", "F":
		if s.Mode != "live" {
			m.changeLogMode(s, "live", "")
			return m.syncLogs()
		}
		s.AutoScroll = !s.AutoScroll
	case "space", " ":
		s.Paused = !s.Paused
		if s.Paused {
			m.stopSession(s)
		} else {
			s.NextDue = time.Time{}
		}
		m.rememberWatch(s)
		return m.syncLogs()
	case "r":
		s.Reading = false
		s.NextDue = time.Time{}
		s.Final = false
		if s.Mode == "live" {
			m.stopSession(s)
		}
		return m.fetchNow(s)
	case "L":
		m.logSettingsKey = s.Key
		m.overlay = "log-settings"
		m.menuIndex = 0
		return nil
	case "/":
		m.inputMode = "log-search"
		m.input.Prompt = "Search log: "
		m.input.SetValue(s.Query)
		return m.input.Focus()
	case "n":
		m.findLog(1)
	case "N":
		m.findLog(-1)
	case "y":
		m.note("Copied loaded log text; it may be a bounded tail.")
		return tea.SetClipboard(cleanLog(s.Text))
	case "Y":
		return m.copyFailure(taskRow{s.Connection, s.Task})
	case "o":
		return m.fullLogPager(s)
	case "e":
		if k.IsRepeat {
			return nil
		}
		return m.openEditRow(taskRow{s.Connection, s.Task})
	case "i":
		m.infoTask = &taskRow{s.Connection, s.Task}
		m.overlay = "task-info"
		m.menuIndex = 0
	case "?":
		m.overlay = "help"
		m.menuIndex = 0
	case "R", "I":
		if k.IsRepeat || !core.Eligible("restart", s.Task) {
			return nil
		}
		a := pendingAction{Connection: s.Connection, Request: core.Request{Operation: "restart", IDs: []int{s.Task.ID}, Guards: map[int]time.Time{s.Task.ID: s.Task.CreatedAt}, InPlace: key == "I"}, Title: "Restart as new task", Consequence: "Create a new task and preserve the original logs. Pueue clears dependencies on restarted copies."}
		if a.Request.InPlace {
			a.Title = "Restart in place"
			a.Consequence = "Reuse this ID and permanently overwrite its logs."
		}
		return m.prepare(a)
	case "0", "1", "2", "3", "4":
		return m.directPane(key)
	case "m":
		m.mouse = !m.mouse
		m.pressed = nil
		return nil
	}
	return nil
}
func (m *model) findLog(direction int) {
	s := m.log
	if s == nil || s.Query == "" {
		return
	}
	lines := strings.Split(s.Text, "\n")
	for n := 1; n <= len(lines); n++ {
		i := (s.Offset + direction*n) % len(lines)
		if i < 0 {
			i += len(lines)
		}
		if strings.Contains(strings.ToLower(lines[i]), strings.ToLower(s.Query)) {
			s.AutoScroll = false
			s.Offset = i
			if s.Mode == "poll" {
				m.stopSession(s)
				s.Active = true
				s.Reading = true
			}
			m.status = fmt.Sprintf("Log match at line %d", i+1)
			return
		}
	}
	m.status = "No log matches."
}
func (m *model) loadOlder(s *logState) tea.Cmd {
	if s.Lines >= 10000 {
		m.note("Buffer limit reached; press o to open the full log in a pager.")
		return nil
	}
	s.Lines = min(10000, max(500, s.Lines*2))
	s.AutoScroll = false
	s.Reading = false
	s.NextDue = time.Time{}
	if s.Mode == "live" {
		m.changeLogMode(s, "poll", "")
		s.AutoScroll = false
	}
	m.note(fmt.Sprintf("Loading the latest %d lines; polling pauses while reading.", s.Lines))
	cmd := m.fetchNow(s)
	s.Reading = true
	return cmd
}
func (m *model) copyFailure(r taskRow) tea.Cmd {
	return func() tea.Msg {
		var text string
		var err error
		if batch, ok := m.backend.(core.BatchLogger); ok {
			results, e := batch.Logs(m.ctx, r.Connection, []int{r.Task.ID}, 500)
			err = e
			if entry, ok := results[r.Task.ID]; ok {
				if entry.Task != nil && (!entry.Task.CreatedAt.Equal(r.Task.CreatedAt) || entry.Task.AttemptIdentity() != r.Task.AttemptIdentity()) {
					return reportMsg{Err: fmt.Errorf("task attempt changed; select the current task before copying a report")}
				}
				text = entry.Output
				if entry.Error != "" {
					text = "Log unavailable: " + entry.Error
				}
			} else if e == nil {
				text = "Log unavailable: task no longer exists"
			}
		} else {
			text, err = m.backend.Log(m.ctx, r.Connection, r.Task.ID, 500)
		}
		if err != nil {
			text = "Log unavailable: " + err.Error()
		}
		return reportMsg{Text: logview.FailureReport(r.Connection, r.Task, text, time.Now())}
	}
}

type pagerFinishedMsg struct{ Err error }

func (m *model) fullLogPager(s *logState) tea.Cmd {
	provider, ok := m.backend.(interface {
		FullLogCommand(context.Context, core.Connection, int) (*exec.Cmd, error)
	})
	if !ok {
		m.note("Full-log pager is not available for this backend.")
		return nil
	}
	c, t := s.Connection, s.Task
	seq, key, attempt := m.pagerSequence, s.Key, s.Attempt
	return func() tea.Msg {
		cmd, err := provider.FullLogCommand(m.ctx, c, t.ID)
		return pagerReadyMsg{Command: cmd, Err: err, Sequence: seq, Key: key, Attempt: attempt}
	}
}

type pagerReadyMsg struct {
	Command      *exec.Cmd
	Err          error
	Sequence     uint64
	Key, Attempt string
}
type logPager struct {
	source, pager *exec.Cmd
	err           error
}

func (p *logPager) SetStdin(r io.Reader)  { p.pager.Stdin = r }
func (p *logPager) SetStdout(w io.Writer) { p.pager.Stdout = w }
func (p *logPager) SetStderr(w io.Writer) { p.pager.Stderr = w; p.source.Stderr = w }
func (p *logPager) Run() error {
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	p.source.Stdout = w
	p.pager.Stdin = r
	if err = p.source.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { e := p.source.Wait(); _ = w.Close(); done <- e }()
	err = p.pager.Run()
	killed := false
	var sourceErr error
	select {
	case sourceErr = <-done:
	default:
		killed = p.source.Process.Kill() == nil
		sourceErr = <-done
	}
	if err != nil {
		return err
	}
	if sourceErr != nil {
		if exit, ok := sourceErr.(*exec.ExitError); ok {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() && (status.Signal() == syscall.SIGPIPE || (killed && status.Signal() == syscall.SIGKILL)) {
				return nil
			}
		}
		return sourceErr
	}
	return nil
}
