package form

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

type ConnectionModel struct {
	Done, Cancelled                    bool
	Connection                         core.Connection
	SavePath                           string
	Err                                error
	backend                            core.Backend
	fields                             []textinput.Model
	focus                              int
	editing, advanced, review, testing bool
	generation                         uint64
	cancel                             context.CancelFunc
	testResult                         string
	chooser                            *picker
	reviewOffset                       int
	width, height                      int
	pressed                            *formPress
}
type connectionTestMsg struct {
	owner      *ConnectionModel
	generation uint64
	snapshot   core.Snapshot
	err        error
}

var connectionLabels = []string{"ID (stable; cannot be renamed when editing)", "Display name (optional)", "Kind: local / ssh / native (Ctrl+P choose)", "SSH host alias (native: optional companion)", "Pueue binary (optional)", "Pueue config path (SSH: remote; otherwise local)", "Native Pueue profile (optional)", "Native daemon hostname / IP", "Native daemon port", "TLS certificate file (local path)", "Secret file (local path; contents never shown)", "Companion remote Pueue binary (optional)", "Companion remote config path (optional)", "Companion remote profile (optional)", "Unix socket path (on this machine; optional)"}

func NewConnection(initial core.Connection, editing bool, backend core.Backend) *ConnectionModel {
	if initial.Kind == "" {
		initial.Kind = "local"
	}
	port := ""
	if initial.Port != 0 {
		port = strconv.Itoa(initial.Port)
	}
	m := &ConnectionModel{Connection: initial, editing: editing, backend: backend, width: 80, height: 24}
	for _, v := range []string{initial.ID, initial.Name, initial.Kind, initial.SSHHost, initial.Binary, initial.ConfigPath, initial.Profile, initial.Host, port, initial.CertPath, initial.SecretPath, initial.SSHBinary, initial.SSHConfig, initial.SSHProfile, initial.SocketPath} {
		m.fields = append(m.fields, input(v))
	}
	if editing {
		m.focus = 1
	}
	return m
}
func (m *ConnectionModel) Init() tea.Cmd { return m.fields[m.focus].Focus() }
func (m *ConnectionModel) Close() {
	m.pressed = nil
	m.generation++
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.testing = false
}
func (m *ConnectionModel) invalidate() { m.Close(); m.testResult = ""; m.Err = nil; m.review = false }
func (m *ConnectionModel) visible() []int {
	out := []int{0, 1, 2}
	switch strings.TrimSpace(m.fields[2].Value()) {
	case "ssh":
		out = append(out, 3, 4, 5, 6)
	case "native":
		out = append(out, 4, 5, 6, 7, 8, 9, 10)
		if m.advanced {
			out = append(out, 14, 3, 11, 12, 13)
		}
	default:
		out = append(out, 4, 5, 6)
		if m.advanced {
			out = append(out, 14)
		}
	}
	return out
}
func (m *ConnectionModel) focusField(index int) tea.Cmd {
	for i := range m.fields {
		m.fields[i].Blur()
	}
	m.focus = index
	return m.fields[index].Focus()
}
func (m *ConnectionModel) Update(msg tea.Msg) (*ConnectionModel, tea.Cmd) {
	if m.Done || m.Cancelled {
		return m, nil
	}
	if cmd, handled := m.mouseUpdate(msg); handled {
		return m, cmd
	}
	switch v := msg.(type) {
	case connectionTestMsg:
		if v.owner != m || v.generation != m.generation {
			return m, nil
		}
		m.testing = false
		m.cancel = nil
		if v.err != nil {
			m.testResult = "Test failed: " + v.err.Error()
		} else {
			m.testResult = fmt.Sprintf("Connected · %d tasks · %d groups", len(v.snapshot.Tasks), len(v.snapshot.Groups))
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = max(1, v.Width), max(1, v.Height)
		m.pressed = nil
		for i := range m.fields {
			m.fields[i].SetWidth(max(1, v.Width-6))
		}
		return m, nil
	case tea.KeyPressMsg:
		m.pressed = nil
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
			if key == "esc" {
				m.chooser = nil
				return m, m.focusField(m.focus)
			}
			if key == "enter" {
				items := m.chooser.visible()
				if len(items) > 0 {
					m.fields[2].SetValue(items[min(m.chooser.index, len(items)-1)].id)
					m.invalidate()
					m.chooser = nil
					return m, m.focusField(2)
				}
				return m, nil
			}
			return m, m.chooser.update(v)
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
			case "down", "pgdown":
				m.reviewOffset++
			case "up", "pgup":
				m.reviewOffset = max(0, m.reviewOffset-1)
			}
			return m, nil
		}
		switch key {
		case "ctrl+t":
			return m, m.test()
		case "ctrl+s":
			c, err := m.draft()
			if err != nil {
				m.Err = err
				return m, nil
			}
			if err := config.ValidateConnection(c); err != nil {
				m.Err = err
				return m, nil
			}
			m.Connection = c
			m.review = true
			m.Err = nil
			m.reviewOffset = 0
			m.fields[m.focus].Blur()
			return m, nil
		case "ctrl+o":
			m.advanced = !m.advanced
			visible := m.visible()
			found := false
			for _, i := range visible {
				if i == m.focus {
					found = true
				}
			}
			if !found {
				return m, m.focusField(2)
			}
			return m, nil
		case "ctrl+p":
			m.chooser = newPicker("connection kind", []choice{{"local", "local · this machine's Pueue"}, {"ssh", "ssh · execute Pueue on an SSH host"}, {"native", "native · Pueue TLS profile / endpoint"}})
			return m, m.chooser.query.Focus()
		case "tab", "shift+tab":
			visible := m.visible()
			position := 0
			for i, index := range visible {
				if index == m.focus {
					position = i
				}
			}
			delta := 1
			if key == "shift+tab" {
				delta = -1
			}
			for {
				position = (position + delta + len(visible)) % len(visible)
				if !(m.editing && visible[position] == 0) {
					break
				}
			}
			return m, m.focusField(visible[position])
		}
	}
	if m.chooser != nil {
		return m, m.chooser.update(msg)
	}
	if m.editing && m.focus == 0 {
		return m, nil
	}
	before := m.fields[m.focus].Value()
	var cmd tea.Cmd
	m.fields[m.focus], cmd = m.fields[m.focus].Update(msg)
	if before != m.fields[m.focus].Value() {
		m.invalidate()
	}
	return m, cmd
}
func (m *ConnectionModel) draft() (core.Connection, error) {
	v := func(i int) string { return strings.TrimSpace(m.fields[i].Value()) }
	c := core.Connection{ID: v(0), Name: v(1), Kind: v(2), Binary: v(4), ConfigPath: v(5), Profile: v(6), Logs: m.Connection.Logs}
	switch c.Kind {
	case "local":
		c.SocketPath = v(14)
	case "ssh":
		c.SSHHost = v(3)
	case "native":
		c.Host = v(7)
		if v(8) != "" {
			port, err := strconv.Atoi(v(8))
			if err != nil {
				return c, errors.New("port must be an integer")
			}
			c.Port = port
		}
		c.CertPath = v(9)
		c.SecretPath = v(10)
		c.SocketPath = v(14)
		c.SSHHost = v(3)
		c.SSHBinary = v(11)
		c.SSHConfig = v(12)
		c.SSHProfile = v(13)
	}
	return c, nil
}
func (m *ConnectionModel) test() tea.Cmd {
	c, err := m.draft()
	if err != nil {
		m.Err = err
		return nil
	}
	if c.ID == "" {
		c.ID = "draft"
	}
	if err := config.ValidateConnection(c); err != nil {
		m.Err = err
		return nil
	}
	if m.backend == nil {
		m.Err = errors.New("backend is unavailable")
		return nil
	}
	m.Close()
	m.testing = true
	m.Err = nil
	m.testResult = "Testing draft connection…"
	generation := m.generation
	backend := m.backend
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	m.cancel = cancel
	return func() tea.Msg {
		defer cancel()
		snapshot, err := backend.Snapshot(ctx, c)
		return connectionTestMsg{m, generation, snapshot, err}
	}
}
func (m *ConnectionModel) Reject(err error) tea.Cmd {
	m.Done = false
	m.review = false
	m.Err = err
	return m.focusField(m.focus)
}
func (m *ConnectionModel) View(w, h int) string {
	if m.Cancelled {
		return ""
	}
	if m.chooser != nil {
		return m.chooser.view(w, h)
	}
	status := m.testResult
	if status == "" {
		status = "Test does not save or switch connections; offline drafts can be saved"
	}
	if m.Err != nil {
		status = m.Err.Error()
	}
	if m.review {
		c := m.Connection
		body := []string{"ID: " + safe(c.ID), "Display name: " + safe(c.DisplayName()), "Kind: " + c.Kind}
		if m.SavePath != "" {
			body = append([]string{"Save to: " + safe(m.SavePath)}, body...)
		}
		if c.SSHHost != "" {
			label := "SSH host: "
			if c.Kind == "native" {
				label = "Optional SSH companion: "
			}
			body = append(body, label+safe(c.SSHHost))
		}
		for _, p := range []struct{ label, value string }{{"Pueue binary", c.Binary}, {"Pueue config", c.ConfigPath}, {"Profile", c.Profile}, {"Native host", c.Host}, {"TLS certificate reference", c.CertPath}, {"Secret file reference", c.SecretPath}, {"Companion binary", c.SSHBinary}, {"Companion config", c.SSHConfig}, {"Companion profile", c.SSHProfile}, {"Socket", c.SocketPath}} {
			if p.value != "" {
				body = append(body, p.label+": "+safe(p.value))
			}
		}
		if c.Port != 0 {
			body = append(body, fmt.Sprintf("Port: %d", c.Port))
		}
		body = append(body, "Save this connection in lazypueue settings.", "Referenced Pueue configuration will remain unchanged.")
		return render("Review connection", wrapLines(body, w), m.reviewOffset, w, h, status, m.formFooter(w, h).text)
	}
	body, focusLine, _ := m.body()
	return render("Connection configuration", body, focusLine, w, h, status, m.formFooter(w, h).text)
}
