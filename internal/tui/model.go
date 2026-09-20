// Package tui hosts the same task and connection forms as the command line.
// Only Update owns UI state; subprocesses report immutable, scoped messages.
package tui

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
)

func Run(ctx context.Context, cfg config.Config, path string, backend core.Backend, connection string) error {
	if connection != "" && connection != "all" {
		if _, err := cfg.Connection(connection); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, cfg, path, backend, connection)
	defer m.close()
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err == nil && m.stateLoaded && !m.stateLoadFailed {
		err = config.SaveState(m.captureState())
	}
	return err
}

type connectionState struct {
	Snapshot         core.Snapshot
	Err              error
	Pending, Writing bool
	Generation       uint64
	Attempt          time.Time
	Cancel           context.CancelFunc
	WriteCancel      context.CancelFunc
}
type viewState struct {
	Selected      string
	Index, Offset int
	Query, State  string
}
type taskRow struct {
	Connection core.Connection
	Task       core.Task
}

func (r taskRow) key() string { return r.Connection.ID + "/" + r.Task.Identity() }

type groupRow struct {
	Connection core.Connection
	Group      core.Group
}

func (r groupRow) key() string { return r.Connection.ID + "/" + r.Group.Name }

type scopeRow struct{ Connection, Group, Label string }

type model struct {
	ctx                                   context.Context
	cfg                                   config.Config
	path                                  string
	backend                               core.Backend
	states                                map[string]*connectionState
	sem                                   chan struct{}
	scope, group                          string
	tab, focus, sidebarIndex              int
	views                                 map[string]*viewState
	width, height                         int
	status                                string
	events                                []string
	input                                 textinput.Model
	inputMode                             string
	overlay                               string
	menuIndex                             int
	menu                                  []action
	confirm                               *pendingAction
	inputAction                           *pendingAction
	selected                              map[string]taskRow
	taskForm                              *form.TaskModel
	connectionForm                        *form.ConnectionModel
	formPending                           bool
	formUnknown                           bool
	formCancelRequested                   bool
	formGuards                            map[int]time.Time
	formGuardConnection                   string
	connectionEditing                     bool
	localState                            config.State
	stateLoaded                           bool
	stateLoadFailed                       bool
	navigationTouched                     bool
	restoreScope                          bool
	previewText, previewError, previewKey string
	previewGeneration                     uint64
	previewCancel                         context.CancelFunc
	previewPending                        bool
	log                                   logState
	detailOffset                          int
	prefix                                bool
	prefixGen                             uint64
}
type tickMsg time.Time
type snapshotMsg struct {
	ID         string
	Generation uint64
	Snapshot   core.Snapshot
	Err        error
}
type previewMsg struct {
	Key        string
	Generation uint64
	Text       string
	Err        error
}
type operationMsg struct {
	ID       string
	Result   core.Result
	Err      error
	Add      *core.AddRequest
	FromForm bool
}
type reconcileMsg string
type stateLoadedMsg struct {
	State config.State
	Err   error
}
type persistedMsg struct{ Err error }
type connectionSavedMsg struct {
	Config     config.Config
	Connection core.Connection
	Err        error
	Removed    bool
}
type messageMsg string
type prefixExpiredMsg uint64

func newModel(ctx context.Context, cfg config.Config, path string, backend core.Backend, connection string) *model {
	restoreScope := connection == "" && (cfg.DefaultConnection == "" || cfg.DefaultConnection == "all")
	i := textinput.New()
	i.Prompt = "/ "
	i.CharLimit = 4096
	if connection == "" {
		connection = cfg.DefaultConnection
	}
	if connection == "" {
		connection = "all"
	}
	m := &model{ctx: ctx, cfg: cfg, path: path, backend: backend, scope: connection, states: map[string]*connectionState{}, sem: make(chan struct{}, 4), views: map[string]*viewState{}, selected: map[string]taskRow{}, width: 100, height: 30, focus: 1, input: i}
	for _, c := range cfg.Connections {
		m.states[c.ID] = &connectionState{}
	}
	m.views[m.viewKey()] = &viewState{}
	m.restoreScope = restoreScope
	return m
}
func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.tick(), func() tea.Msg { s, e := config.LoadState(); return stateLoadedMsg{s, e} }}
	for _, c := range m.cfg.Connections {
		cmds = append(cmds, m.refresh(c, false))
	}
	return tea.Batch(cmds...)
}
func (m *model) close() {
	if m.taskForm != nil {
		m.taskForm.Close()
	}
	if m.connectionForm != nil {
		m.connectionForm.Close()
	}
	for _, s := range m.states {
		if s.Cancel != nil {
			s.Cancel()
		}
		if s.WriteCancel != nil {
			s.WriteCancel()
		}
	}
	if m.previewCancel != nil {
		m.previewCancel()
	}
	m.stopLog()
}
func (m *model) tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}
func (m *model) refresh(c core.Connection, force bool) tea.Cmd {
	s := m.states[c.ID]
	if s == nil {
		return nil
	}
	if s.Pending && !force {
		return nil
	}
	if s.Cancel != nil {
		s.Cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	s.Cancel = cancel
	s.Pending = true
	s.Generation++
	s.Attempt = time.Now()
	gen := s.Generation
	return func() tea.Msg {
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			return snapshotMsg{ID: c.ID, Generation: gen, Err: ctx.Err()}
		}
		defer func() { <-m.sem }()
		snap, err := m.backend.Snapshot(ctx, c)
		return snapshotMsg{c.ID, gen, snap, err}
	}
}
func (m *model) viewKey() string { return fmt.Sprintf("%s/%s/%d", m.scope, m.group, m.tab) }
func (m *model) currentView() *viewState {
	k := m.viewKey()
	if m.views[k] == nil {
		return &viewState{}
	}
	return m.views[k]
}
func (m *model) connection(id string) (core.Connection, bool) {
	for _, c := range m.cfg.Connections {
		if c.ID == id {
			return c, true
		}
	}
	return core.Connection{}, false
}
func (m *model) rows() []taskRow {
	v := m.currentView()
	var out []taskRow
	q := strings.ToLower(v.Query)
	for _, c := range m.cfg.Connections {
		if m.scope != "all" && m.scope != c.ID {
			continue
		}
		s := m.states[c.ID]
		if s == nil {
			continue
		}
		for _, t := range s.Snapshot.Tasks {
			if m.group != "" && t.Group != m.group {
				continue
			}
			if v.State != "" && ((v.State == "locked" && !t.Locked) || (v.State != "locked" && t.State() != v.State)) {
				continue
			}
			text := fmt.Sprintf("%s %s %d %s %s %s %s %s %s", c.ID, c.DisplayName(), t.ID, t.Command, t.Label, t.Group, t.Path, t.State(), t.Result+" "+t.Error)
			if q != "" && !strings.Contains(strings.ToLower(text), q) {
				continue
			}
			out = append(out, taskRow{c, t})
		}
	}
	order := map[string]int{"running": 0, "paused": 1, "queued": 2, "stashed": 3, "locked": 4, "failed": 5, "succeeded": 6}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		oa, ok := order[a.Task.State()]
		if !ok {
			oa = 7
		}
		ob, ok := order[b.Task.State()]
		if !ok {
			ob = 7
		}
		if oa != ob {
			return oa < ob
		}
		if a.Connection.ID != b.Connection.ID {
			return a.Connection.ID < b.Connection.ID
		}
		if a.Task.Terminal() {
			return a.Task.ID > b.Task.ID
		}
		return a.Task.ID < b.Task.ID
	})
	return out
}
func (m *model) groups() []groupRow {
	var out []groupRow
	q := strings.ToLower(m.currentView().Query)
	for _, c := range m.cfg.Connections {
		if m.scope != "all" && m.scope != c.ID {
			continue
		}
		s := m.states[c.ID]
		if s == nil {
			continue
		}
		for _, g := range s.Snapshot.Groups {
			if m.group != "" && m.group != g.Name {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(c.DisplayName()+" "+g.Name+" "+g.Status), q) {
				continue
			}
			out = append(out, groupRow{c, g})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Connection.ID != out[j].Connection.ID {
			return out[i].Connection.ID < out[j].Connection.ID
		}
		return out[i].Group.Name < out[j].Group.Name
	})
	return out
}
func (m *model) reconcileSelection() {
	if m.views[m.viewKey()] == nil {
		m.views[m.viewKey()] = &viewState{}
	}
	v := m.currentView()
	var keys []string
	if m.tab == 0 {
		for _, r := range m.rows() {
			keys = append(keys, r.key())
		}
	} else {
		for _, r := range m.groups() {
			keys = append(keys, r.key())
		}
	}
	for i, k := range keys {
		if k == v.Selected {
			v.Index = i
			return
		}
	}
	if v.Selected != "" {
		for _, c := range m.cfg.Connections {
			if m.scope != "all" && m.scope != c.ID {
				continue
			}
			if s := m.states[c.ID]; s != nil && s.Snapshot.ObservedAt.IsZero() && s.Err == nil {
				return
			}
		}
	}
	v.Index = clamp(v.Index, 0, len(keys)-1)
	v.Selected = ""
	if len(keys) > 0 {
		v.Selected = keys[v.Index]
	}
}
func (m *model) task() (taskRow, bool) {
	rows := m.rows()
	v := m.currentView()
	if m.tab != 0 || v.Index < 0 || v.Index >= len(rows) {
		return taskRow{}, false
	}
	return rows[v.Index], true
}
func (m *model) selectedGroup() (groupRow, bool) {
	rows := m.groups()
	v := m.currentView()
	if m.tab != 1 || v.Index < 0 || v.Index >= len(rows) {
		return groupRow{}, false
	}
	return rows[v.Index], true
}
func (m *model) scopes() []scopeRow {
	out := []scopeRow{{Connection: "all", Label: "All connections"}}
	for _, c := range m.cfg.Connections {
		out = append(out, scopeRow{Connection: c.ID, Label: c.DisplayName()})
		if s := m.states[c.ID]; s != nil {
			for _, g := range s.Snapshot.Groups {
				out = append(out, scopeRow{Connection: c.ID, Group: g.Name, Label: "  " + g.Name})
			}
		}
	}
	return out
}
func (m *model) switchScope(id, group string) tea.Cmd {
	m.stopLog()
	m.scope = id
	m.group = group
	m.selected = map[string]taskRow{}
	m.detailOffset = 0
	m.focus = 1
	m.overlay = ""
	m.inputMode = ""
	m.prefix = false
	m.reconcileSelection()
	return m.preview()
}
func (m *model) move(delta int) tea.Cmd {
	if m.focus == 0 {
		m.sidebarIndex = clamp(m.sidebarIndex+delta, 0, len(m.scopes())-1)
		return nil
	}
	if m.focus == 2 {
		m.detailOffset = max(0, m.detailOffset+delta)
		return nil
	}
	v := m.currentView()
	n := len(m.rows())
	if m.tab == 1 {
		n = len(m.groups())
	}
	v.Index = clamp(v.Index+delta, 0, n-1)
	v.Selected = ""
	if m.tab == 0 && n > 0 {
		v.Selected = m.rows()[v.Index].key()
	} else if m.tab == 1 && n > 0 {
		v.Selected = m.groups()[v.Index].key()
	}
	m.detailOffset = 0
	return m.preview()
}
func (m *model) preview() tea.Cmd {
	if m.log.Open {
		return nil
	}
	r, ok := m.task()
	if !ok {
		m.previewText = ""
		m.previewError = ""
		m.previewKey = ""
		return nil
	}
	k := r.key()
	if k == m.previewKey {
		return nil
	}
	if m.previewCancel != nil {
		m.previewCancel()
	}
	m.previewGeneration++
	gen := m.previewGeneration
	m.previewKey = k
	m.previewText = ""
	m.previewError = ""
	m.previewPending = false
	if r.Task.StartedAt == nil && !r.Task.Terminal() {
		m.previewText = "This task has not started."
		return nil
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.previewCancel = cancel
	m.previewPending = true
	return func() tea.Msg {
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
		s, e := m.backend.Log(ctx, r.Connection, r.Task.ID, 200)
		return previewMsg{k, gen, s, e}
	}
}
func (m *model) note(s string) {
	m.status = s
	m.events = append(m.events, time.Now().Format("15:04:05")+"  "+s)
	if len(m.events) > 100 {
		m.events = m.events[len(m.events)-100:]
	}
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.navigationTouched = true
	}
	var cmds []tea.Cmd
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.height = max(1, v.Height)
		m.input.SetWidth(max(1, m.width-8))
	case tickMsg:
		cmds = append(cmds, m.tick())
		active := m.cfg.TUI.RefreshSeconds
		if active <= 0 {
			active = 2
		}
		background := m.cfg.TUI.BackgroundSeconds
		if background <= 0 {
			background = 5
		}
		for _, c := range m.cfg.Connections {
			interval := background
			if m.scope == "all" || m.scope == c.ID {
				interval = active
			}
			s := m.states[c.ID]
			if s != nil && time.Since(s.Attempt) >= time.Duration(interval)*time.Second {
				cmds = append(cmds, m.refresh(c, false))
			}
		}
	case snapshotMsg:
		s := m.states[v.ID]
		if s == nil || s.Generation != v.Generation {
			return m, nil
		}
		s.Pending = false
		s.Err = v.Err
		if v.Err == nil {
			s.Snapshot = v.Snapshot
			m.pruneSelection(v.ID)
			if m.log.Open && m.log.Connection.ID == v.ID {
				exists := false
				for _, t := range v.Snapshot.Tasks {
					if m.log.Key == v.ID+"/"+t.Identity() {
						exists = true
						break
					}
				}
				if !exists {
					m.stopLog()
					m.note("Inspected task was removed or its ID was reused.")
				}
			}
		}
		m.reconcileSelection()
		cmds = append(cmds, m.preview())
	case previewMsg:
		if v.Key == m.previewKey && v.Generation == m.previewGeneration {
			m.previewPending = false
			m.previewText = cleanLog(v.Text)
			m.previewError = ""
			if v.Err != nil {
				m.previewError = v.Err.Error()
			}
		}
	case logChunkMsg:
		return m, m.acceptLog(v)
	case operationMsg:
		if s := m.states[v.ID]; s != nil {
			s.Writing = false
			s.WriteCancel = nil
		}
		m.formPending = false
		m.formUnknown = v.FromForm && v.Result.Unknown
		if v.Err != nil {
			if v.Result.Message != "" && v.Result.Message != v.Err.Error() {
				v.Err = fmt.Errorf("%s: %w", v.Result.Message, v.Err)
			}
			if len(v.Result.Outcomes) > 0 {
				var details []string
				for _, outcome := range v.Result.Outcomes {
					if outcome.Error != "" {
						details = append(details, fmt.Sprintf("#%d %s", outcome.ID, outcome.Error))
					}
				}
				if len(details) > 0 {
					v.Err = fmt.Errorf("%w (%s)", v.Err, strings.Join(details, "; "))
				}
			}
			m.note(v.ID + ": " + v.Err.Error())
			if v.FromForm && m.taskForm != nil {
				cmds = append(cmds, m.taskForm.Reject(v.Err))
			}
		} else {
			m.note(v.ID + ": " + v.Result.Message + " · refreshing state")
			if v.FromForm {
				if m.taskForm != nil {
					m.taskForm.Close()
				}
				m.taskForm = nil
				if v.Add != nil {
					if m.localState.LastUsed == nil {
						m.localState.LastUsed = map[string]config.LastUsed{}
					}
					m.localState.LastUsed[v.ID] = config.LastUsed{Directory: v.Add.Directory, Group: v.Add.Group}
					cmds = append(cmds, m.saveState())
				}
			}
			if v.Result.TaskID != nil {
				m.scope = v.ID
				m.group = ""
				m.tab = 0
				m.reconcileSelection()
				m.currentView().Query = ""
				m.currentView().State = ""
				m.currentView().Index = 0
				cmds = append(cmds, m.selectAdded(v.ID, *v.Result.TaskID))
			}
			m.selected = map[string]taskRow{}
		}
		if c, ok := m.connection(v.ID); ok {
			cmds = append(cmds, m.refresh(c, true), tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return reconcileMsg(v.ID) }), tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return reconcileMsg(v.ID) }))
		}
		if v.FromForm && m.formCancelRequested && m.taskForm != nil && !v.Result.Unknown {
			m.taskForm.Close()
			m.taskForm = nil
		}
		m.formCancelRequested = false
	case addedSelectionMsg:
		if m.scope == v.Connection && m.tab == 0 {
			for _, r := range m.rows() {
				if r.Task.ID == v.ID && r.Connection.ID == v.Connection {
					m.currentView().Selected = r.key()
					m.reconcileSelection()
					cmds = append(cmds, m.preview())
					break
				}
			}
		}
	case reconcileMsg:
		if c, ok := m.connection(string(v)); ok {
			cmds = append(cmds, m.refresh(c, true))
		}
	case stateLoadedMsg:
		m.stateLoaded = true
		if v.Err == nil {
			if v.State.LastUsed == nil {
				v.State.LastUsed = map[string]config.LastUsed{}
			}
			for k, last := range m.localState.LastUsed {
				v.State.LastUsed[k] = last
			}
			m.localState = v.State
			if !m.navigationTouched {
				for k, saved := range v.State.Views {
					m.views[k] = &viewState{Selected: saved.Selected, Index: max(0, saved.Index), Offset: max(0, saved.Offset), Query: saved.Query, State: saved.State}
				}
				if m.restoreScope && v.State.Scope != "" {
					if _, ok := m.connection(v.State.Scope); ok || v.State.Scope == "all" {
						m.scope = v.State.Scope
					}
				}
				m.reconcileSelection()
				cmds = append(cmds, m.preview())
			}
		} else {
			m.stateLoadFailed = true
			m.note("Preferences: " + v.Err.Error())
		}
	case persistedMsg:
		if v.Err != nil {
			m.note("Save preferences: " + v.Err.Error())
		}
	case connectionSavedMsg:
		m.formPending = false
		if v.Err != nil {
			m.note(v.Err.Error())
			if m.connectionForm != nil {
				cmds = append(cmds, m.connectionForm.Reject(v.Err))
			}
		} else {
			if m.connectionForm != nil {
				m.connectionForm.Close()
			}
			m.connectionForm = nil
			m.cfg = v.Config
			m.overlay = "connections"
			m.menuIndex = 0
			if v.Removed {
				if s := m.states[v.Connection.ID]; s != nil && s.Cancel != nil {
					s.Cancel()
				}
				delete(m.states, v.Connection.ID)
				if m.scope == v.Connection.ID {
					cmds = append(cmds, m.switchScope("all", ""))
					m.overlay = "connections"
				}
				m.note("Connection removed: " + v.Connection.DisplayName())
			} else {
				if old := m.states[v.Connection.ID]; old != nil && old.Cancel != nil {
					old.Cancel()
				}
				gen := uint64(0)
				if old := m.states[v.Connection.ID]; old != nil {
					gen = old.Generation
				}
				m.states[v.Connection.ID] = &connectionState{Generation: gen}
				m.note("Connection saved: " + v.Connection.DisplayName())
				cmds = append(cmds, m.refresh(v.Connection, true))
			}
		}
	case messageMsg:
		m.note(string(v))
	case prefixExpiredMsg:
		if uint64(v) == m.prefixGen {
			m.prefix = false
		}
	}
	// Forms own all input, including global mnemonic keys and Ctrl+C.
	if m.taskForm != nil {
		if m.formPending {
			if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.String() == "esc") {
				m.formCancelRequested = true
				if s := m.states[m.taskForm.ConnectionID]; s != nil && s.WriteCancel != nil {
					s.WriteCancel()
				}
				m.note("Cancelling the wait; any accepted task remains in Pueue.")
			}
		}
		if m.formUnknown {
			m.status = "Outcome unknown. Inspect the queue before retrying; Esc returns without another submission."
			if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "esc" || key.String() == "ctrl+c") {
				m.taskForm.Close()
				m.taskForm = nil
				m.formUnknown = false
			}
			return m, tea.Batch(cmds...)
		}
		if !m.formPending {
			var cmd tea.Cmd
			m.taskForm, cmd = m.taskForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.taskForm.Cancelled {
				m.taskForm.Close()
				m.taskForm = nil
				cmds = append(cmds, m.preview())
			} else if m.taskForm.Done {
				c, ok := m.connection(m.taskForm.ConnectionID)
				if !ok {
					cmds = append(cmds, m.taskForm.Reject(fmt.Errorf("connection no longer exists")))
				} else {
					a := m.taskForm.Request
					m.formPending = true
					guards := map[int]time.Time{}
					for id, created := range m.taskForm.Guards {
						guards[id] = created
					}
					for _, id := range a.After {
						if m.formGuardConnection != c.ID {
							break
						}
						if t, ok := m.formGuards[id]; ok {
							guards[id] = t
						}
					}
					cmds = append(cmds, m.execute(c, core.Request{Operation: "add", Add: &a, Guards: guards}, true))
				}
			}
		}
		return m, tea.Batch(cmds...)
	}
	if m.connectionForm != nil {
		if !m.formPending {
			var cmd tea.Cmd
			m.connectionForm, cmd = m.connectionForm.Update(msg)
			cmds = append(cmds, cmd)
			if m.connectionForm.Cancelled {
				m.connectionForm.Close()
				m.connectionForm = nil
				m.overlay = "connections"
			} else if m.connectionForm.Done {
				c := m.connectionForm.Connection
				m.formPending = true
				cmds = append(cmds, m.saveConnection(c, false))
			}
		}
		return m, tea.Batch(cmds...)
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		cmds = append(cmds, m.key(key))
		return m, tea.Batch(cmds...)
	}
	if m.inputMode != "" || m.overlay == "actions" {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}
func (m *model) pruneSelection(id string) {
	for k, r := range m.selected {
		if r.Connection.ID != id {
			continue
		}
		found := false
		for _, t := range m.states[id].Snapshot.Tasks {
			if t.Identity() == r.Task.Identity() {
				found = true
				break
			}
		}
		if !found {
			delete(m.selected, k)
		}
	}
}

type addedSelectionMsg struct {
	Connection string
	ID         int
}

func (m *model) selectAdded(c string, id int) tea.Cmd {
	return tea.Tick(800*time.Millisecond, func(time.Time) tea.Msg { return addedSelectionMsg{c, id} })
}
func (m *model) saveState() tea.Cmd {
	s := m.captureState()
	s.LastUsed = map[string]config.LastUsed{}
	for k, v := range m.localState.LastUsed {
		s.LastUsed[k] = v
	}
	return func() tea.Msg { return persistedMsg{config.SaveState(s)} }
}
func (m *model) captureState() config.State {
	s := m.localState
	s.Scope = m.scope
	s.Views = map[string]config.ViewState{}
	for k, v := range m.views {
		s.Views[k] = config.ViewState{Selected: v.Selected, Index: v.Index, Offset: v.Offset, Query: v.Query, State: v.State}
	}
	s.LastUsed = map[string]config.LastUsed{}
	for k, v := range m.localState.LastUsed {
		s.LastUsed[k] = v
	}
	return s
}
func (m *model) execute(c core.Connection, r core.Request, fromForm bool) tea.Cmd {
	s := m.states[c.ID]
	if s == nil || s.Writing {
		m.formPending = false
		m.note("An operation is already pending on this connection.")
		return nil
	}
	s.Writing = true
	ctx, cancel := context.WithCancel(m.ctx)
	s.WriteCancel = cancel
	return func() tea.Msg {
		defer cancel()
		result, err := m.backend.Execute(ctx, c, r)
		return operationMsg{c.ID, result, err, r.Add, fromForm}
	}
}
func (m *model) saveConnection(c core.Connection, removed bool) tea.Cmd {
	if s := m.states[c.ID]; s != nil && s.Writing {
		return func() tea.Msg {
			return connectionSavedMsg{Connection: c, Err: fmt.Errorf("wait for the operation on %s before changing its connection", c.DisplayName())}
		}
	}
	cfg := m.cfg
	cfg.Connections = append([]core.Connection(nil), m.cfg.Connections...)
	found := false
	for i, old := range cfg.Connections {
		if old.ID == c.ID {
			if !removed && !m.connectionEditing {
				return func() tea.Msg {
					return connectionSavedMsg{Connection: c, Err: fmt.Errorf("connection ID %q already exists; choose a unique ID", c.ID)}
				}
			}
			found = true
			if removed {
				cfg.Connections = append(cfg.Connections[:i], cfg.Connections[i+1:]...)
			} else {
				cfg.Connections[i] = c
			}
			break
		}
	}
	if !found && !removed {
		cfg.Connections = append(cfg.Connections, c)
	}
	if removed && cfg.DefaultConnection == c.ID {
		cfg.DefaultConnection = "all"
	}
	return func() tea.Msg {
		err := config.Save(m.path, cfg)
		if err == nil {
			if fresh, e := config.Load(m.path); e == nil {
				cfg = fresh
			}
		}
		return connectionSavedMsg{cfg, c, err, removed}
	}
}
func (m *model) openTask(kind string) tea.Cmd {
	m.stopLog()
	m.formUnknown = false
	m.formCancelRequested = false
	m.formGuards = map[int]time.Time{}
	cID := m.scope
	contextGroup := m.group
	if g, ok := m.selectedGroup(); ok && kind == "new" {
		cID = g.Connection.ID
		contextGroup = g.Group.Name
	}
	a := core.AddRequest{Group: "default", Mode: "queued", GroupParallel: 1}
	if r, ok := m.task(); ok {
		if cID == "all" {
			cID = r.Connection.ID
		}
		if kind == "after" || kind == "clone" {
			cID = r.Connection.ID
			a.Directory = r.Task.Path
			a.Group = r.Task.Group
			if kind == "after" {
				a.After = []int{r.Task.ID}
				m.formGuards[r.Task.ID] = r.Task.CreatedAt
			} else {
				a.Command = r.Task.Command
				a.Label = r.Task.Label
				a.Priority = r.Task.Priority
				a.After = append([]int(nil), r.Task.Dependencies...)
			}
		}
	}
	if cID == "all" {
		cID = ""
	}
	if kind == "new" {
		if last, ok := m.localState.LastUsed[cID]; ok {
			a.Directory = last.Directory
			a.Group = last.Group
		}
		if c, ok := m.connection(cID); ok && c.Kind == "local" && a.Directory == "" {
			a.Directory, _ = os.Getwd()
		}
		if contextGroup != "" {
			a.Group = contextGroup
		}
	}
	m.overlay = ""
	m.inputMode = ""
	m.taskForm = form.NewTask(m.cfg.Connections, a, cID, m.backend)
	m.formGuardConnection = cID
	return m.taskForm.Init()
}
func (m *model) openConnection(edit bool) tea.Cmd {
	c := core.Connection{Kind: "ssh"}
	if edit {
		idx := m.menuIndex - 1
		if idx < 0 || idx >= len(m.cfg.Connections) {
			return nil
		}
		c = m.cfg.Connections[idx]
		if s := m.states[c.ID]; s != nil && s.Writing {
			m.note("Wait for the operation on " + c.DisplayName() + " before editing its connection.")
			return nil
		}
	}
	m.connectionEditing = edit
	m.connectionForm = form.NewConnection(c, edit, m.backend)
	m.connectionForm.SavePath = m.path
	m.overlay = ""
	return m.connectionForm.Init()
}
func clamp(x, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(hi, max(lo, x))
}
