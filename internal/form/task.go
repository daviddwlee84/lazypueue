package form

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type TaskModel struct {
	Done, Cancelled bool
	Request         core.AddRequest
	// Guards pins every dependency incarnation shown in the accepted review.
	// Hosts pass it with the add request; later snapshots never mutate this map.
	Guards                    map[int]time.Time
	ConnectionID              string
	Err                       error
	connections               []core.Connection
	backend                   core.Backend
	fields                    []textinput.Model
	command                   textarea.Model
	focus                     int
	advanced, review, loading bool
	snapshot                  core.Snapshot
	snapshotErr               error
	generation                uint64
	cancel                    context.CancelFunc
	chooser                   *picker
	plan                      core.Plan
	reviewOffset              int
	width                     int
}
type taskSnapshotMsg struct {
	owner      *TaskModel
	generation uint64
	connection string
	snapshot   core.Snapshot
	err        error
}

var taskLabels = []string{"Connection (Ctrl+L choose)", "Command (multiline)", "Working directory on daemon host", "Group (Ctrl+G choose/create)", "Label (optional)", "Mode: queued / stashed / immediate (Ctrl+P choose)", "Dependencies: comma-separated IDs (Ctrl+D choose)", "Delay (optional; Pueue date expression)", "Priority", "Create missing group: yes / no", "New group parallelism (0 = unlimited)"}

func NewTask(connections []core.Connection, initial core.AddRequest, selectedConnection string, backend core.Backend) *TaskModel {
	m := &TaskModel{connections: append([]core.Connection(nil), connections...), backend: backend, Request: initial, ConnectionID: selectedConnection, width: 80}
	if m.ConnectionID == "" || m.ConnectionID == "all" {
		if len(connections) > 0 {
			m.ConnectionID = connections[0].ID
		}
	}
	if initial.Group == "" {
		initial.Group = "default"
	}
	if initial.Mode == "" {
		initial.Mode = "queued"
	}
	if initial.Directory == "" {
		if c, ok := m.connection(); ok && c.Kind == "local" {
			initial.Directory, _ = os.Getwd()
		}
	}
	deps := make([]string, len(initial.After))
	for i, id := range initial.After {
		deps[i] = strconv.Itoa(id)
	}
	create := "no"
	if initial.CreateGroup {
		create = "yes"
	}
	values := []string{m.ConnectionID, "", initial.Directory, initial.Group, initial.Label, initial.Mode, strings.Join(deps, ","), initial.Delay, strconv.Itoa(initial.Priority), create, strconv.Itoa(initial.GroupParallel)}
	for _, v := range values {
		m.fields = append(m.fields, input(v))
	}
	m.command = textarea.New()
	m.command.CharLimit = 1024 * 1024
	m.command.SetWidth(72)
	m.command.SetHeight(4)
	m.command.SetValue(initial.Command)
	m.command.ShowLineNumbers = false
	m.focus = 1
	m.Request = initial
	return m
}
func (m *TaskModel) Init() tea.Cmd { return tea.Batch(m.command.Focus(), m.loadSnapshot()) }
func (m *TaskModel) Close() {
	m.generation++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.loading = false
}
func (m *TaskModel) connection() (core.Connection, bool) {
	for _, c := range m.connections {
		if c.ID == m.ConnectionID {
			return c, true
		}
	}
	return core.Connection{}, false
}
func (m *TaskModel) loadSnapshot() tea.Cmd {
	m.Close()
	m.snapshot = core.Snapshot{}
	m.snapshotErr = nil
	c, ok := m.connection()
	if !ok {
		m.snapshotErr = errors.New("choose an existing connection")
		return nil
	}
	if m.backend == nil {
		m.snapshotErr = errors.New("backend is unavailable")
		return nil
	}
	m.loading = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	m.cancel = cancel
	generation := m.generation
	backend := m.backend
	return func() tea.Msg {
		defer cancel()
		s, err := backend.Snapshot(ctx, c)
		return taskSnapshotMsg{m, generation, c.ID, s, err}
	}
}
func (m *TaskModel) setConnection(id string) tea.Cmd {
	if id == m.ConnectionID {
		return nil
	}
	m.ConnectionID = id
	m.Guards = nil
	m.fields[0].SetValue(id)
	m.fields[6].SetValue("")
	m.fields[2].SetValue("")
	m.fields[3].SetValue("default")
	m.fields[9].SetValue("no")
	m.review = false
	m.Err = nil
	if c, ok := m.connection(); ok && c.Kind == "local" {
		wd, _ := os.Getwd()
		m.fields[2].SetValue(wd)
	}
	return m.loadSnapshot()
}
func (m *TaskModel) count() int {
	if m.advanced {
		return len(m.fields)
	}
	return 6
}
func (m *TaskModel) focusField(index int) tea.Cmd {
	m.command.Blur()
	for i := range m.fields {
		m.fields[i].Blur()
	}
	m.focus = index
	if index == 1 {
		return m.command.Focus()
	}
	return m.fields[index].Focus()
}
func (m *TaskModel) Update(msg tea.Msg) (*TaskModel, tea.Cmd) {
	if m.Done || m.Cancelled {
		return m, nil
	}
	switch v := msg.(type) {
	case taskSnapshotMsg:
		if v.owner != m || v.generation != m.generation || v.connection != m.ConnectionID {
			return m, nil
		}
		m.loading = false
		m.snapshot = v.snapshot
		m.snapshotErr = v.err
		m.cancel = nil
		return m, nil
	case tea.WindowSizeMsg:
		m.width = max(1, v.Width)
		m.command.SetWidth(max(1, v.Width-6))
		m.command.SetHeight(max(1, min(5, v.Height-10)))
		for i := range m.fields {
			m.fields[i].SetWidth(max(1, v.Width-6))
		}
		return m, nil
	case tea.KeyPressMsg:
		if v.IsRepeat && v.String() == "ctrl+s" {
			return m, nil
		}
		key := v.String()
		if key == "ctrl+c" {
			m.Cancelled = true
			m.Close()
			return m, nil
		}
		if m.chooser != nil {
			return m, m.pickerKey(v)
		}
		if key == "esc" {
			if m.review {
				m.review = false
				return m, m.focusField(m.focus)
			}
			m.Cancelled = true
			m.Close()
			return m, nil
		}
		if m.review {
			switch key {
			case "ctrl+s":
				m.Done = true
				m.Close()
			case "pgdown", "down":
				m.reviewOffset++
			case "pgup", "up":
				m.reviewOffset = max(0, m.reviewOffset-1)
			}
			return m, nil
		}
		switch key {
		case "ctrl+s":
			return m, m.prepareReview()
		case "ctrl+o":
			m.advanced = !m.advanced
			if m.focus >= m.count() {
				return m, m.focusField(5)
			}
			return m, nil
		case "ctrl+r":
			return m, m.loadSnapshot()
		case "ctrl+l":
			return m, m.openPicker("connections")
		case "ctrl+g":
			return m, m.openPicker("groups")
		case "ctrl+d":
			return m, m.openPicker("dependencies")
		case "ctrl+p":
			switch m.focus {
			case 0:
				return m, m.openPicker("connections")
			case 3:
				return m, m.openPicker("groups")
			case 5:
				return m, m.openPicker("mode")
			case 6:
				return m, m.openPicker("dependencies")
			}
			return m, nil
		case "tab", "shift+tab":
			var cmd tea.Cmd
			if m.focus == 0 {
				cmd = m.setConnection(strings.TrimSpace(m.fields[0].Value()))
			}
			next := m.focus + 1
			if key == "shift+tab" {
				next = m.focus - 1
			}
			next = (next + m.count()) % m.count()
			return m, tea.Batch(cmd, m.focusField(next))
		}
	}
	if m.chooser != nil {
		return m, m.chooser.update(msg)
	}
	var cmd tea.Cmd
	if m.focus == 1 {
		before := m.command.Value()
		m.command, cmd = m.command.Update(msg)
		if before != m.command.Value() {
			m.Err = nil
		}
	} else {
		before := m.fields[m.focus].Value()
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
		if before != m.fields[m.focus].Value() {
			m.Err = nil
		}
	}
	return m, cmd
}
func (m *TaskModel) openPicker(kind string) tea.Cmd {
	var choices []choice
	switch kind {
	case "connections":
		for _, c := range m.connections {
			choices = append(choices, choice{c.ID, c.DisplayName() + " · " + c.Kind + " · " + c.ID})
		}
	case "groups":
		for _, g := range m.snapshot.Groups {
			choices = append(choices, choice{g.Name, g.Name + " · " + g.Status})
		}
		choices = append(choices, choice{"\x00create", "+ Create a new group…"})
	case "mode":
		for _, s := range []string{"queued", "stashed", "immediate"} {
			choices = append(choices, choice{s, s})
		}
	case "dependencies":
		for _, t := range m.snapshot.Tasks {
			choices = append(choices, choice{strconv.Itoa(t.ID), fmt.Sprintf("%d · %s · %s", t.ID, t.Status, t.Command)})
		}
	}
	m.chooser = newPicker(kind, choices)
	if kind == "dependencies" {
		for _, s := range strings.Split(m.fields[6].Value(), ",") {
			m.chooser.selected[strings.TrimSpace(s)] = true
		}
	}
	return m.chooser.query.Focus()
}
func (m *TaskModel) pickerKey(v tea.KeyPressMsg) tea.Cmd {
	p := m.chooser
	if v.String() == "esc" {
		m.chooser = nil
		return m.focusField(m.focus)
	}
	if v.String() == "ctrl+s" && p.kind == "dependencies" {
		var ids []int
		for id, selected := range p.selected {
			if selected {
				n, err := strconv.Atoi(id)
				if err == nil {
					ids = append(ids, n)
				}
			}
		}
		sort.Ints(ids)
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		m.fields[6].SetValue(strings.Join(parts, ","))
		m.chooser = nil
		return m.focusField(m.focus)
	}
	if v.String() == "enter" {
		items := p.visible()
		if len(items) == 0 {
			return nil
		}
		c := items[min(p.index, len(items)-1)]
		if p.kind == "dependencies" {
			p.selected[c.id] = !p.selected[c.id]
			return nil
		}
		m.chooser = nil
		switch p.kind {
		case "connections":
			return tea.Batch(m.setConnection(c.id), m.focusField(m.focus))
		case "groups":
			if c.id == "\x00create" {
				m.fields[3].SetValue("")
				m.fields[9].SetValue("yes")
				m.advanced = true
				return m.focusField(3)
			}
			m.fields[3].SetValue(c.id)
			m.fields[9].SetValue("no")
		case "mode":
			m.fields[5].SetValue(c.id)
		}
		return m.focusField(m.focus)
	}
	return p.update(v)
}
func (m *TaskModel) draft() (core.AddRequest, error) {
	r := core.AddRequest{Command: m.command.Value(), Directory: strings.TrimSpace(m.fields[2].Value()), Group: strings.TrimSpace(m.fields[3].Value()), Label: m.fields[4].Value(), Mode: strings.TrimSpace(m.fields[5].Value()), Delay: strings.TrimSpace(m.fields[7].Value())}
	if strings.TrimSpace(r.Command) == "" {
		return r, errors.New("command is required")
	}
	if r.Directory == "" {
		return r, errors.New("working directory on the daemon host is required")
	}
	if r.Group == "" {
		return r, errors.New("group is required")
	}
	if r.Mode != "queued" && r.Mode != "stashed" && r.Mode != "immediate" {
		return r, errors.New("mode must be queued, stashed, or immediate")
	}
	var err error
	r.Priority, err = strconv.Atoi(strings.TrimSpace(m.fields[8].Value()))
	if err != nil {
		return r, errors.New("priority must be an integer")
	}
	r.GroupParallel, err = strconv.Atoi(strings.TrimSpace(m.fields[10].Value()))
	if err != nil || r.GroupParallel < 0 {
		return r, errors.New("group parallelism must be a non-negative integer")
	}
	switch strings.ToLower(strings.TrimSpace(m.fields[9].Value())) {
	case "yes", "true":
		r.CreateGroup = true
	case "no", "false", "":
	default:
		return r, errors.New("create group must be yes or no")
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(m.fields[6].Value(), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, e := strconv.Atoi(part)
		if e != nil || id < 0 {
			return r, errors.New("dependencies must be non-negative task IDs separated by commas")
		}
		if !seen[id] {
			r.After = append(r.After, id)
			seen[id] = true
		}
	}
	if r.Mode == "immediate" && (len(r.After) > 0 || r.Delay != "") {
		return r, errors.New("immediate mode cannot be combined with dependencies or a delay")
	}
	return r, nil
}
func (m *TaskModel) prepareReview() tea.Cmd {
	if id := strings.TrimSpace(m.fields[0].Value()); id != m.ConnectionID {
		cmd := m.setConnection(id)
		m.Err = errors.New("connection changed; review the cleared directory and dependencies")
		return cmd
	}
	r, err := m.draft()
	if err != nil {
		m.Err = err
		return nil
	}
	c, ok := m.connection()
	if !ok {
		m.Err = errors.New("choose an existing connection")
		return nil
	}
	if m.loading {
		m.Err = errors.New("wait for the connection's groups and tasks to load")
		return nil
	}
	if m.snapshotErr != nil {
		m.Err = fmt.Errorf("load groups/tasks first (Ctrl+R retries): %w", m.snapshotErr)
		return nil
	}
	groupExists := false
	for _, g := range m.snapshot.Groups {
		if g.Name == r.Group {
			groupExists = true
		}
	}
	if !groupExists && !r.CreateGroup {
		m.Err = errors.New("group does not exist; choose Ctrl+G → Create a new group")
		return nil
	}
	if groupExists && r.CreateGroup {
		r.CreateGroup = false
	}
	guards := make(map[int]time.Time, len(r.After))
	for _, id := range r.After {
		found := false
		for _, t := range m.snapshot.Tasks {
			if t.ID == id {
				if t.Failed() {
					m.Err = fmt.Errorf("dependency %d has already failed; choose a task that can succeed", id)
					return nil
				}
				found = true
				guards[id] = t.CreatedAt
				break
			}
		}
		if !found {
			m.Err = fmt.Errorf("dependency %d no longer exists on %s", id, c.DisplayName())
			return nil
		}
	}
	if m.backend == nil {
		m.Err = errors.New("backend is unavailable")
		return nil
	}
	plan, err := m.backend.Preview(c, core.Request{Operation: "add", Add: &r})
	if err != nil {
		m.Err = err
		return nil
	}
	m.Request = r
	m.Guards = guards
	m.plan = plan
	m.review = true
	m.reviewOffset = 0
	m.Err = nil
	m.command.Blur()
	for i := range m.fields {
		m.fields[i].Blur()
	}
	return nil
}
func (m *TaskModel) Reject(err error) tea.Cmd {
	m.Done = false
	m.review = false
	m.Err = err
	return m.focusField(m.focus)
}
func (m *TaskModel) View(w, h int) string {
	if m.Cancelled {
		return ""
	}
	if m.chooser != nil {
		return m.chooser.view(w, h)
	}
	status := "Ctrl+L connections · Ctrl+G groups · Ctrl+D dependencies"
	if m.loading {
		status = "Loading groups and tasks…"
	}
	if m.snapshotErr != nil {
		status = "Could not load choices: " + m.snapshotErr.Error() + " · Ctrl+R retry"
	}
	if m.Err != nil {
		status = m.Err.Error()
	}
	if m.review {
		r := m.Request
		body := []string{"Connection: " + safe(m.ConnectionID), "Directory on daemon host: " + safe(r.Directory), "Group: " + safe(r.Group) + " · Mode: " + r.Mode, "Label: " + safe(r.Label), "Command:"}
		body = append(body, strings.Split(safe(r.Command), "\n")...)
		if r.CreateGroup {
			body = append(body, fmt.Sprintf("Create group %q first, parallelism %d", r.Group, r.GroupParallel))
		}
		if len(r.After) > 0 {
			body = append(body, fmt.Sprintf("After %v: every dependency must succeed", r.After))
		}
		if r.Delay != "" {
			body = append(body, "Delay: "+safe(r.Delay))
		}
		body = append(body, fmt.Sprintf("Priority: %d", r.Priority))
		if c, ok := m.connection(); ok {
			source := "local Pueue client environment"
			if c.Kind == "ssh" || m.plan.Transport == "ssh" {
				source = "remote Pueue client environment"
			}
			body = append(body, "Environment: "+source)
		}
		body = append(body, "Equivalent command:")
		body = append(body, strings.Split(safe(m.plan.Display), "\n")...)
		for _, s := range m.plan.Consequences {
			body = append(body, safe(s))
		}
		return render("Review new task", wrapLines(body, w), m.reviewOffset, w, h, status, "Ctrl+S submit · Esc edit · ↑↓ scroll · Ctrl+C cancel")
	}
	var body []string
	focusLine := 0
	for i := 0; i < m.count(); i++ {
		mark := "  "
		if i == m.focus {
			mark = "* "
			focusLine = len(body)
		}
		body = append(body, mark+taskLabels[i])
		if i == 1 {
			body = append(body, strings.Split(m.command.View(), "\n")...)
		} else {
			body = append(body, m.fields[i].View())
		}
	}
	return render("New Pueue task", body, focusLine, w, h, status, "Tab next · Shift+Tab back · Ctrl+O advanced · Ctrl+S review · Esc cancel")
}
