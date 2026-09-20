package tui

import (
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type pendingAction struct {
	Connection                core.Connection
	Request                   core.Request
	Title, Consequence, Extra string
}
type action struct {
	ID, Label, Key string
	Target         *pendingAction
	Value          string
}

func (m *model) actions() []action {
	out := []action{{ID: "add", Label: "Add task", Key: "n"}}
	if m.tab == 0 {
		if r, ok := m.task(); ok {
			out = append(out, action{ID: "log", Label: "Show log", Key: "enter"}, action{ID: "follow", Label: "Follow output", Key: "F"}, action{ID: "copy-id", Label: "Copy task ID", Value: strconv.Itoa(r.Task.ID)}, action{ID: "copy-command", Label: "Copy command", Value: r.Task.Command}, action{ID: "copy-add", Label: "Copy pueue add command"}, action{ID: "clone", Label: "Duplicate and edit"}, action{ID: "after", Label: "Add job after this", Key: "a"})
			rows := m.actionRows()
			ids := make([]int, 0, len(rows))
			guards := map[int]time.Time{}
			allTerminal, allRunning, allPaused, allQueued, allStashed, allKillable, allRemovable := true, true, true, true, true, true, true
			for _, row := range rows {
				t := row.Task
				ids = append(ids, t.ID)
				guards[t.ID] = t.CreatedAt
				allTerminal = allTerminal && core.Eligible("restart", t)
				allRunning = allRunning && core.Eligible("pause", t)
				allPaused = allPaused && t.State() == "paused" && core.Eligible("start", t)
				allQueued = allQueued && core.Eligible("stash", t)
				allStashed = allStashed && core.Eligible("enqueue", t)
				allKillable = allKillable && core.Eligible("kill", t)
				allRemovable = allRemovable && core.Eligible("remove", t)
			}
			add := func(op, label, key, consequence string, inplace bool) {
				out = append(out, action{ID: op, Label: label, Key: key, Target: &pendingAction{Connection: rows[0].Connection, Request: core.Request{Operation: op, IDs: ids, Guards: guards, InPlace: inplace, ResumeOnly: op == "start" && allPaused}, Title: label, Consequence: consequence}})
			}
			if allTerminal {
				add("restart", "Restart as new task", "R", "Create new tasks and preserve the original logs. Pueue clears dependencies on restarted copies.", false)
				add("restart", "Restart in place (overwrite logs)", "", "Reuse the IDs and permanently overwrite their logs.", true)
			}
			if allRunning {
				add("pause", "Pause", "p", "", false)
			}
			if allPaused {
				add("start", "Resume", "p", "", false)
			}
			if allQueued || allStashed {
				add("start", "Start now (ignore dependencies / slots)", "", "Force-start these tasks, ignoring dependencies and parallelism limits.", false)
			}
			if allQueued {
				add("stash", "Stash", "", "", false)
			}
			if allStashed {
				add("enqueue", "Enqueue", "", "", false)
			}
			if allKillable {
				add("kill", "Kill tasks", "", "Stop these processes. Their logs remain available.", false)
			}
			if allRemovable {
				add("remove", "Remove tasks and logs", "", "Permanently remove these tasks and their logs.", false)
			}
		}
	}
	if g, ok := m.selectedGroup(); ok {
		add := func(op, label, consequence string) {
			out = append(out, action{ID: op, Label: label, Target: &pendingAction{Connection: g.Connection, Request: core.Request{Operation: op, Group: g.Group.Name}, Title: label, Consequence: consequence}})
		}
		add("group-pause", "Pause group", "Pause the group and its running tasks.")
		add("group-start", "Resume group", "Resume this group's queue.")
		out = append(out, action{ID: "parallel-input", Label: "Set group parallelism", Target: &pendingAction{Connection: g.Connection, Request: core.Request{Operation: "parallel", Group: g.Group.Name}, Title: "Set parallelism", Consequence: "Change the group's execution capacity. 0 means unlimited."}})
		add("restart-failed", "Restart failed tasks in group", "Create new tasks for the failures; preserve original logs.")
		if g.Group.Name != "default" {
			add("group-remove", "Remove empty group", "Remove this empty group. Pueue refuses to remove groups that still contain tasks.")
		}
	}
	if c, ok := m.actionConnection(); ok {
		out = append(out, action{ID: "group-input", Label: "Create group", Target: &pendingAction{Connection: c, Request: core.Request{Operation: "group-add", Parallel: 1}, Title: "Create group (parallelism 1)"}})
		group := m.group
		if g, ok := m.selectedGroup(); ok {
			group = g.Group.Name
		}
		out = append(out, action{ID: "clean-success", Label: "Clean successful tasks", Target: &pendingAction{Connection: c, Request: core.Request{Operation: "clean", Group: group, SuccessfulOnly: true}, Title: "Clean successful tasks", Consequence: "Permanently delete the listed successful tasks and their logs."}}, action{ID: "clean-all", Label: "Clean all finished tasks (including failures)", Target: &pendingAction{Connection: c, Request: core.Request{Operation: "clean", Group: group}, Title: "Clean finished tasks", Consequence: "Permanently delete the listed finished tasks, including failures and their logs."}})
		if c.Kind == "ssh" || c.SSHHost != "" {
			out = append(out, action{ID: "authenticate", Label: "Authenticate SSH and reconnect", Target: &pendingAction{Connection: c}})
		}
	}
	out = append(out, action{ID: "status-filter", Label: "Filter by status", Key: "s"}, action{ID: "connections", Label: "Manage connections", Key: "t"}, action{ID: "refresh", Label: "Refresh", Key: "r"}, action{ID: "events", Label: "Recent operation results"}, action{ID: "help", Label: "Help", Key: "?"})
	return out
}
func (m *model) actionConnection() (core.Connection, bool) {
	if m.tab == 0 {
		if r, ok := m.task(); ok {
			return r.Connection, true
		}
	} else {
		if g, ok := m.selectedGroup(); ok {
			return g.Connection, true
		}
	}
	if m.scope != "all" {
		return m.connection(m.scope)
	}
	if len(m.cfg.Connections) == 1 {
		return m.cfg.Connections[0], true
	}
	return core.Connection{}, false
}
func (m *model) actionRows() []taskRow {
	if len(m.selected) > 0 {
		out := make([]taskRow, 0, len(m.selected))
		for _, r := range m.selected {
			out = append(out, r)
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Task.ID < out[j].Task.ID })
		return out
	}
	if r, ok := m.task(); ok {
		return []taskRow{r}
	}
	return nil
}
func (m *model) filteredActions() []action {
	q := strings.ToLower(m.input.Value())
	if q == "" {
		return m.menu
	}
	var out []action
	for _, a := range m.menu {
		if strings.Contains(strings.ToLower(a.Label), q) {
			out = append(out, a)
		}
	}
	return out
}
func (m *model) openActions() tea.Cmd {
	m.overlay = "actions"
	m.menu = m.actions()
	m.menuIndex = 0
	m.input.SetValue("")
	m.input.Prompt = "Action: "
	m.prefix = false
	return m.input.Focus()
}

func (m *model) key(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if m.overlay != "" {
		return m.overlayKey(k)
	}
	if m.inputMode != "" {
		return m.inputKey(k)
	}
	if m.log.Open {
		return m.logKey(k)
	}
	if key == "ctrl+c" || key == "q" {
		return tea.Quit
	}
	if m.prefix {
		m.prefix = false
		if key == "g" {
			return m.move(-1 << 30)
		}
	}
	switch key {
	case "esc":
		m.selected = map[string]taskRow{}
		m.currentView().Query = ""
		m.currentView().State = ""
		m.reconcileSelection()
		return m.preview()
	case "up", "k":
		return m.move(-1)
	case "down", "j":
		return m.move(1)
	case "home":
		return m.move(-1 << 30)
	case "end", "G":
		return m.move(1 << 30)
	case "g":
		m.prefix = true
		m.prefixGen++
		gen := m.prefixGen
		return tea.Tick(700*time.Millisecond, func(time.Time) tea.Msg { return prefixExpiredMsg(gen) })
	case "pgup":
		return m.move(-max(1, m.height-8))
	case "pgdown":
		return m.move(max(1, m.height-8))
	case "tab", "right", "l":
		m.focus = (m.focus + 1) % 3
		return nil
	case "shift+tab", "left", "h":
		m.focus = (m.focus + 2) % 3
		return nil
	case "1", "2":
		m.tab = 0
		if key == "2" {
			m.tab = 1
		}
		m.focus = 1
		m.detailOffset = 0
		m.selected = map[string]taskRow{}
		m.reconcileSelection()
		return m.preview()
	case "/":
		m.inputMode = "filter"
		m.input.Prompt = "Filter: "
		m.input.SetValue(m.currentView().Query)
		return m.input.Focus()
	case ":":
		return m.openActions()
	case "space", " ":
		if r, ok := m.task(); ok {
			if _, exists := m.selected[r.key()]; exists {
				delete(m.selected, r.key())
			} else {
				for _, s := range m.selected {
					if s.Connection.ID != r.Connection.ID {
						m.note("Selection belongs to " + s.Connection.DisplayName() + ". Esc clears it before selecting another host.")
						return nil
					}
				}
				m.selected[r.key()] = r
			}
		}
		return nil
	case "enter":
		if m.focus == 0 {
			scopes := m.scopes()
			if m.sidebarIndex < len(scopes) {
				s := scopes[m.sidebarIndex]
				return m.switchScope(s.Connection, s.Group)
			}
		}
		if g, ok := m.selectedGroup(); ok {
			m.tab = 0
			return m.switchScope(g.Connection.ID, g.Group.Name)
		}
	}
	if k.IsRepeat {
		return nil
	}
	for _, a := range m.actions() {
		if a.Key != "" && a.Key == key {
			return m.perform(a)
		}
	}
	return nil
}
func (m *model) inputKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "esc" || key == "ctrl+c" {
		if m.inputMode == "filter" {
			m.currentView().Query = ""
			m.reconcileSelection()
		}
		m.inputMode = ""
		m.input.Blur()
		m.inputAction = nil
		return m.preview()
	}
	if key == "enter" {
		mode := m.inputMode
		m.inputMode = ""
		m.input.Blur()
		if mode == "filter" {
			return nil
		}
		if mode == "log-search" {
			m.log.Query = m.input.Value()
			m.findLog(1)
			return nil
		}
		if m.inputAction != nil {
			a := *m.inputAction
			m.inputAction = nil
			value := strings.TrimSpace(m.input.Value())
			if mode == "parallel" {
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					m.note("Parallelism must be a non-negative integer (0 = unlimited).")
					m.inputMode = mode
					m.inputAction = &a
					return m.input.Focus()
				}
				a.Request.Parallel = n
			} else {
				if value == "" {
					m.note("Group name is required.")
					m.inputMode = mode
					m.inputAction = &a
					return m.input.Focus()
				}
				a.Request.Group = value
			}
			return m.prepare(a)
		}
		return nil
	}
	if m.inputMode == "filter" && (key == "up" || key == "down") {
		delta := 1
		if key == "up" {
			delta = -1
		}
		old := m.focus
		m.focus = 1
		cmd := m.move(delta)
		m.focus = old
		return cmd
	}
	old := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.inputMode == "filter" && old != m.input.Value() {
		v := m.currentView()
		v.Query = m.input.Value()
		v.Selected = ""
		v.Index = 0
		m.reconcileSelection()
		return tea.Batch(cmd, m.preview())
	}
	return cmd
}
func (m *model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if k.IsRepeat && key == "enter" {
		return nil
	}
	if key == "esc" || key == "ctrl+c" {
		m.overlay = ""
		m.input.Blur()
		m.confirm = nil
		return nil
	}
	switch m.overlay {
	case "actions":
		switch key {
		case "up":
			m.menuIndex = max(0, m.menuIndex-1)
			return nil
		case "down":
			m.menuIndex = min(max(0, len(m.filteredActions())-1), m.menuIndex+1)
			return nil
		case "enter":
			items := m.filteredActions()
			if m.menuIndex < len(items) {
				a := items[m.menuIndex]
				m.overlay = ""
				m.input.Blur()
				return m.perform(a)
			}
			return nil
		}
		old := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		if old != m.input.Value() {
			m.menuIndex = 0
		}
		return cmd
	case "confirm":
		if key == "down" || key == "j" {
			m.menuIndex++
			return nil
		}
		if key == "up" || key == "k" {
			m.menuIndex = max(0, m.menuIndex-1)
			return nil
		}
		if key == "enter" && m.confirm != nil {
			a := *m.confirm
			m.confirm = nil
			m.overlay = ""
			if a.Extra == "remove-connection" {
				return m.saveConnection(a.Connection, true)
			}
			return m.execute(a.Connection, a.Request, false)
		}
	case "connections":
		switch key {
		case "up", "k":
			m.menuIndex = max(0, m.menuIndex-1)
		case "down", "j":
			m.menuIndex = min(len(m.cfg.Connections), m.menuIndex+1)
		case "a", "n":
			return m.openConnection(false)
		case "e":
			return m.openConnection(true)
		case "enter":
			if m.menuIndex == 0 {
				return m.switchScope("all", "")
			}
			return m.switchScope(m.cfg.Connections[m.menuIndex-1].ID, "")
		case "d":
			if m.menuIndex > 0 {
				c := m.cfg.Connections[m.menuIndex-1]
				if s := m.states[c.ID]; s != nil && s.Writing {
					m.note("Wait for the pending operation before removing this connection.")
					return nil
				}
				m.confirm = &pendingAction{Connection: c, Title: "Remove connection", Consequence: "Remove this saved connection. Its daemon and tasks are unaffected.", Extra: "remove-connection"}
				m.menuIndex = 0
				m.overlay = "confirm"
			}
		case "t":
			if m.menuIndex > 0 {
				c := m.cfg.Connections[m.menuIndex-1]
				m.note("Testing " + c.DisplayName() + "…")
				return m.refresh(c, true)
			}
		case "A":
			if m.menuIndex > 0 {
				return m.authenticate(m.cfg.Connections[m.menuIndex-1])
			}
		}
	case "status":
		states := []string{"", "running", "queued", "paused", "stashed", "failed", "succeeded", "locked"}
		switch key {
		case "up", "k":
			m.menuIndex = max(0, m.menuIndex-1)
		case "down", "j":
			m.menuIndex = min(len(states)-1, m.menuIndex+1)
		case "enter":
			m.currentView().State = states[m.menuIndex]
			m.currentView().Selected = ""
			m.currentView().Index = 0
			m.overlay = ""
			m.reconcileSelection()
			return m.preview()
		}
	case "help", "events":
		if key == "q" || key == "?" || key == "enter" {
			m.overlay = ""
		}
		if key == "down" || key == "j" {
			m.menuIndex++
		}
		if key == "up" || key == "k" {
			m.menuIndex = max(0, m.menuIndex-1)
		}
	}
	return nil
}
func (m *model) perform(a action) tea.Cmd {
	switch a.ID {
	case "add":
		return m.openTask("new")
	case "clone":
		return m.openTask("clone")
	case "after":
		return m.openTask("after")
	case "log", "follow":
		if r, ok := m.task(); ok {
			return m.openLog(r, a.ID == "follow")
		}
	case "copy-id", "copy-command":
		m.note("Copied to terminal clipboard (OSC 52).")
		return tea.SetClipboard(a.Value)
	case "copy-add":
		if r, ok := m.task(); ok {
			p, e := m.backend.Preview(r.Connection, core.Request{Operation: "add", Add: &core.AddRequest{Command: r.Task.Command, Directory: r.Task.Path, Group: r.Task.Group, Label: r.Task.Label, Mode: "queued", After: r.Task.Dependencies, Priority: r.Task.Priority}})
			if e != nil {
				m.note(e.Error())
				return nil
			}
			m.note("Copied reproducible add command.")
			return tea.SetClipboard(p.Display)
		}
	case "connections":
		m.overlay = "connections"
		m.menuIndex = 0
	case "status-filter":
		m.overlay = "status"
		m.menuIndex = 0
	case "refresh":
		m.previewKey = ""
		var cmds []tea.Cmd
		for _, c := range m.cfg.Connections {
			if m.scope == "all" || m.scope == c.ID {
				cmds = append(cmds, m.refresh(c, true))
			}
		}
		return tea.Batch(append(cmds, m.preview())...)
	case "events":
		m.overlay = "events"
		m.menuIndex = 0
	case "help":
		m.overlay = "help"
		m.menuIndex = 0
	case "authenticate":
		if a.Target != nil {
			return m.authenticate(a.Target.Connection)
		}
	case "parallel-input", "group-input":
		if a.Target != nil {
			m.inputAction = a.Target
			m.inputMode = "group"
			m.input.Prompt = "Group name: "
			m.input.SetValue("")
			if a.ID == "parallel-input" {
				m.inputMode = "parallel"
				m.input.Prompt = "Parallel tasks (0 = unlimited): "
				m.input.SetValue("1")
			}
			return m.input.Focus()
		}
	case "clean-success", "clean-all":
		if a.Target != nil {
			p := *a.Target
			p.Request.Guards = map[int]time.Time{}
			if s := m.states[p.Connection.ID]; s != nil {
				for _, t := range s.Snapshot.Tasks {
					if !t.Terminal() || (p.Request.Group != "" && t.Group != p.Request.Group) || (p.Request.SuccessfulOnly && t.Failed()) {
						continue
					}
					p.Request.IDs = append(p.Request.IDs, t.ID)
					p.Request.Guards[t.ID] = t.CreatedAt
				}
			}
			if len(p.Request.IDs) == 0 {
				m.note("No matching finished tasks to clean.")
				return nil
			}
			return m.prepare(p)
		}
	case "restart-failed":
		if a.Target != nil {
			p := *a.Target
			p.Request.Guards = map[int]time.Time{}
			if s := m.states[p.Connection.ID]; s != nil {
				for _, t := range s.Snapshot.Tasks {
					if t.Failed() && (p.Request.Group == "" || t.Group == p.Request.Group) {
						p.Request.IDs = append(p.Request.IDs, t.ID)
						p.Request.Guards[t.ID] = t.CreatedAt
					}
				}
			}
			if len(p.Request.IDs) == 0 {
				m.note("No failed tasks in this group.")
				return nil
			}
			return m.prepare(p)
		}
	default:
		if a.Target != nil {
			return m.prepare(*a.Target)
		}
	}
	return nil
}
func (m *model) prepare(a pendingAction) tea.Cmd {
	if s := m.states[a.Connection.ID]; s != nil && s.Writing {
		m.note("Wait for the current operation on " + a.Connection.DisplayName() + ".")
		return nil
	}
	if _, err := m.backend.Preview(a.Connection, a.Request); err != nil {
		m.note(err.Error())
		return nil
	}
	if a.Consequence != "" || a.Request.Operation == "group-add" {
		m.confirm = &a
		m.menuIndex = 0
		m.overlay = "confirm"
		return nil
	}
	return m.execute(a.Connection, a.Request, false)
}
func (m *model) authenticate(c core.Connection) tea.Cmd {
	auth, ok := m.backend.(interface {
		AuthenticateCommand(core.Connection) (*exec.Cmd, error)
	})
	if !ok {
		m.note("This backend does not support SSH authentication.")
		return nil
	}
	cmd, err := auth.AuthenticateCommand(c)
	if err != nil {
		m.note(err.Error())
		return nil
	}
	m.stopLog()
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		if err != nil {
			return messageMsg("SSH authentication: " + err.Error())
		}
		return reconcileMsg(c.ID)
	})
}
