package form

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type EditModel struct {
	Done, Cancelled             bool
	Request                     core.Request
	Err                         error
	task                        core.Task
	connection                  core.Connection
	backend                     core.Backend
	command                     textarea.Model
	fields                      []textinput.Model
	focus                       int
	advanced, review            bool
	operation                   string
	plan                        core.Plan
	reviewOffset, width, height int
	pressed                     *formPress
}

var editLabels = []string{"Command (multiline)", "Working directory on daemon host", "Label", "Priority", "Retry mode: new / in-place (queued tasks: original)", "Leave stashed after editing: yes / no"}

func NewEdit(task core.Task, c core.Connection, backend core.Backend) *EditModel {
	command := task.OriginalCommand
	if command == "" {
		command = task.Command
	}
	m := &EditModel{task: task, connection: c, backend: backend, width: 80, height: 24}
	m.command = textarea.New()
	m.command.CharLimit = 1024 * 1024
	m.command.SetWidth(72)
	m.command.SetHeight(4)
	m.command.SetValue(command)
	m.command.ShowLineNumbers = false
	mode, stashed := "new", "no"
	switch task.State() {
	case "queued", "stashed":
		m.operation = "edit"
		mode = "original"
		if task.State() == "stashed" && task.ScheduledAt == nil {
			stashed = "yes"
		}
	case "running", "paused":
		m.operation = "stop-edit-restart"
	default:
		m.operation = "edit-restart"
	}
	for _, value := range []string{"", task.Path, task.Label, strconv.Itoa(task.Priority), mode, stashed} {
		m.fields = append(m.fields, input(value))
	}
	m.Request = core.Request{Operation: m.operation, IDs: []int{task.ID}, Guards: map[int]time.Time{task.ID: task.CreatedAt}, Edit: &core.EditRequest{Command: command, Directory: task.Path, Label: task.Label, Priority: task.Priority, Stashed: stashed == "yes"}}
	return m
}

// SetRequest lets CLI hosts prefill business flags and force the requested
// operation/restart policy. It must be called before Init or review.
func (m *EditModel) SetRequest(initial core.Request) {
	if initial.Operation != "" {
		m.operation = initial.Operation
		m.Request.Operation = initial.Operation
	}
	if initial.Edit != nil {
		e := *initial.Edit
		m.command.SetValue(e.Command)
		m.fields[1].SetValue(e.Directory)
		m.fields[2].SetValue(e.Label)
		m.fields[3].SetValue(strconv.Itoa(e.Priority))
		value := "no"
		if e.Stashed {
			value = "yes"
		}
		m.fields[5].SetValue(value)
		m.Request.Edit = &e
	}
	mode := "new"
	if m.operation == "edit" {
		mode = "original"
	} else if initial.InPlace {
		mode = "in-place"
	}
	m.fields[4].SetValue(mode)
	m.Request.InPlace = initial.InPlace
	if stamp, ok := initial.Guards[m.task.ID]; ok {
		m.Request.Guards = map[int]time.Time{m.task.ID: stamp}
	}
}
func (m *EditModel) Init() tea.Cmd { return m.command.Focus() }
func (m *EditModel) Close()        { m.pressed = nil }
func (m *EditModel) count() int {
	if m.advanced {
		return 6
	}
	return 4
}
func (m *EditModel) focusField(index int) tea.Cmd {
	m.command.Blur()
	for i := range m.fields {
		m.fields[i].Blur()
	}
	m.focus = index
	if index == 0 {
		return m.command.Focus()
	}
	return m.fields[index].Focus()
}
func (m *EditModel) Update(msg tea.Msg) (*EditModel, tea.Cmd) {
	if m.Done || m.Cancelled {
		return m, nil
	}
	if cmd, handled := m.mouseUpdate(msg); handled {
		return m, cmd
	}
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		m.pressed = nil
		m.command.SetWidth(max(1, v.Width-6))
		m.command.SetHeight(max(1, min(5, v.Height-10)))
		for i := range m.fields {
			m.fields[i].SetWidth(max(1, v.Width-6))
		}
		return m, nil
	case tea.KeyPressMsg:
		m.pressed = nil
		key := v.String()
		if key == "ctrl+c" {
			m.Cancelled = true
			m.Close()
			return m, nil
		}
		if m.review {
			if v.IsRepeat {
				return m, nil
			}
			switch key {
			case "y", "Y":
				m.Done = true
				m.Close()
			case "enter", "n", "N", "esc":
				m.review = false
				return m, m.focusField(m.focus)
			case "down", "pgdown":
				m.reviewOffset++
			case "up", "pgup":
				m.reviewOffset = max(0, m.reviewOffset-1)
			}
			return m, nil
		}
		switch key {
		case "esc":
			m.Cancelled = true
			m.Close()
			return m, nil
		case "ctrl+s":
			if !v.IsRepeat {
				m.prepareReview()
			}
			return m, nil
		case "ctrl+o":
			m.advanced = !m.advanced
			if m.focus >= m.count() {
				return m, m.focusField(3)
			}
			return m, nil
		case "tab", "shift+tab":
			next := m.focus + 1
			if key == "shift+tab" {
				next = m.focus - 1
			}
			return m, m.focusField((next + m.count()) % m.count())
		}
	}
	var cmd tea.Cmd
	if m.focus == 0 {
		m.command, cmd = m.command.Update(msg)
	} else {
		m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
	}
	m.Err = nil
	return m, cmd
}
func (m *EditModel) prepareReview() {
	priority, err := strconv.Atoi(strings.TrimSpace(m.fields[3].Value()))
	if err != nil || priority < math.MinInt32 || priority > math.MaxInt32 {
		m.Err = errors.New("priority must be a signed 32-bit integer")
		return
	}
	e := core.EditRequest{Command: m.command.Value(), Directory: strings.TrimSpace(m.fields[1].Value()), Label: m.fields[2].Value(), Priority: priority}
	if strings.TrimSpace(e.Command) == "" || e.Directory == "" {
		m.Err = errors.New("command and daemon-host working directory are required")
		return
	}
	if strings.ContainsRune(e.Command+e.Directory+e.Label, 0) {
		m.Err = errors.New("fields cannot contain NUL bytes")
		return
	}
	switch strings.ToLower(strings.TrimSpace(m.fields[5].Value())) {
	case "yes", "true":
		e.Stashed = true
	case "no", "false", "":
	default:
		m.Err = errors.New("leave stashed must be yes or no")
		return
	}
	if m.operation == "edit" && m.task.ScheduledAt != nil && e.Stashed {
		m.Err = errors.New("scheduled task retains its schedule; enqueue after editing or duplicate as an unscheduled stashed task")
		return
	}
	mode := strings.TrimSpace(m.fields[4].Value())
	inPlace := false
	if m.operation == "edit" {
		if mode != "original" {
			m.Err = errors.New("queued/stashed tasks keep their original ID; use Duplicate to create another task")
			return
		}
	} else {
		switch mode {
		case "new":
		case "in-place":
			inPlace = true
		default:
			m.Err = errors.New("retry mode must be new or in-place")
			return
		}
	}
	if m.task.Locked {
		m.Err = errors.New("this task is already locked by another edit")
		return
	}
	req := core.Request{Operation: m.operation, IDs: []int{m.task.ID}, Guards: map[int]time.Time{m.task.ID: m.Request.Guards[m.task.ID]}, Edit: &e, InPlace: inPlace}
	if m.backend == nil {
		m.Err = errors.New("backend is unavailable")
		return
	}
	plan, err := m.backend.Preview(m.connection, req)
	if err != nil {
		m.Err = err
		return
	}
	m.Request = req
	m.plan = plan
	m.review = true
	m.reviewOffset = 0
	m.Err = nil
	m.command.Blur()
	for i := range m.fields {
		m.fields[i].Blur()
	}
}
func (m *EditModel) Reject(err error) tea.Cmd {
	m.Done = false
	m.review = false
	m.Err = err
	return m.focusField(m.focus)
}
func (m *EditModel) body() ([]string, int, map[int]span) {
	var fields []formField
	for i := 0; i < m.count(); i++ {
		value := m.fields[i].View()
		if i == 0 {
			value = m.command.View()
		}
		label := editLabels[i]
		if i == 5 && m.task.ScheduledAt != nil {
			label += " (scheduled task: must stay no)"
		}
		fields = append(fields, formField{i, label, value})
	}
	return fieldBody(fields, m.focus)
}
func (m *EditModel) footer() string {
	if m.review {
		return buttonFooter([]formButton{{"back", "No, edit"}, {"submit", "Yes, apply"}, {"cancel", "Cancel"}}, "y apply · Enter/n/Esc edit", m.width, m.height).text
	}
	return buttonFooter([]formButton{{"review", "Review"}, {"cancel", "Cancel"}}, "Tab fields · Ctrl+O advanced · Ctrl+S review", m.width, m.height).text
}
func (m *EditModel) View(w, h int) string {
	if m.Cancelled {
		return ""
	}
	status := "Edits keep the group's identity, dependencies and captured environment."
	if m.Err != nil {
		status = m.Err.Error()
	}
	if m.review {
		e := m.Request.Edit
		mode := "keep original task ID"
		if m.Request.Operation != "edit" {
			mode = "create a new task; keep old logs"
			if m.Request.InPlace {
				mode = "reuse task ID; overwrite previous logs"
			}
		}
		body := []string{fmt.Sprintf("Connection: %s (%s)", m.connection.DisplayName(), m.connection.ID), fmt.Sprintf("Task #%d · %s", m.task.ID, m.task.State()), "Result: " + mode, "Directory: " + e.Directory, "Label: " + e.Label, fmt.Sprintf("Priority: %d", e.Priority), fmt.Sprintf("Group (unchanged): %s · Dependencies: %v", m.task.Group, m.task.Dependencies), "Command:"}
		body = append(body, strings.Split(e.Command, "\n")...)
		if m.Request.Operation == "stop-edit-restart" {
			body = append(body, "Stop this running process before applying the correction.")
		}
		if m.Request.InPlace {
			body = append(body, "Previous logs will be permanently overwritten.")
		}
		if e.Stashed {
			body = append(body, "Leave stashed; enqueue explicitly when ready.")
		} else if m.task.ScheduledAt != nil && m.operation == "edit" {
			body = append(body, "Keep scheduled start: "+m.task.ScheduledAt.Format(time.RFC3339))
		} else {
			body = append(body, "Queue after applying; execution can begin when a slot is available.")
		}
		body = append(body, m.plan.Consequences...)
		foot := buttonFooter([]formButton{{"back", "No, edit"}, {"submit", "Yes, apply"}, {"cancel", "Cancel"}}, "y apply · Enter/n/Esc edit", w, h).text
		return render("Review task correction · default No", wrapLines(body, w), m.reviewOffset, w, h, status, foot)
	}
	body, focus, _ := m.body()
	foot := buttonFooter([]formButton{{"review", "Review"}, {"cancel", "Cancel"}}, "Tab fields · Ctrl+O advanced · Ctrl+S review", w, h).text
	return render(fmt.Sprintf("Edit task #%d · %s", m.task.ID, m.connection.DisplayName()), body, focus, w, h, status, foot)
}
