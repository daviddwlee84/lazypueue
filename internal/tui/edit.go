package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/form"
)

func (m *model) openEdit() tea.Cmd {
	r, ok := m.task()
	if !ok {
		return nil
	}
	return m.openEditRow(r)
}
func (m *model) openEditRow(r taskRow) tea.Cmd {
	if m.backendMaintenanceActive() {
		m.note("Wait for backend maintenance before editing tasks.")
		return nil
	}
	if r.Task.Locked {
		m.note("Task is locked; use Recover to stash before editing.")
		return nil
	}
	if s := m.states[r.Connection.ID]; s != nil && s.Writing {
		m.note("Wait for the current operation before editing.")
		return nil
	}
	m.stopLog()
	m.editConnection = r.Connection
	m.editForm = form.NewEdit(r.Task, r.Connection, m.backend)
	m.editForm.Update(tea.WindowSizeMsg{Width: m.width, Height: max(1, m.height-5)})
	m.formUnknown = false
	m.formPending = false
	m.overlay = ""
	return tea.Batch(m.editForm.Init(), m.syncLogs())
}
func (m *model) updateEdit(msg tea.Msg, cmds []tea.Cmd) tea.Cmd {
	if m.formUnknown {
		m.status = "Outcome unknown. Inspect the task; Recover to stash is available if it remains Locked. Esc returns."
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "esc" || k.String() == "ctrl+c") {
			m.editForm.Close()
			m.editForm = nil
			m.formUnknown = false
		}
		return tea.Batch(cmds...)
	}
	if m.formPending {
		if k, ok := msg.(tea.KeyPressMsg); ok && (k.String() == "ctrl+c" || k.String() == "esc") {
			if s := m.states[m.editConnection.ID]; s != nil && s.WriteCancel != nil {
				s.WriteCancel()
			}
			m.note("Cancelling edit wait; check status for partial effects or Locked tasks.")
		}
		return tea.Batch(cmds...)
	}
	var cmd tea.Cmd
	m.editForm, cmd = m.editForm.Update(msg)
	cmds = append(cmds, cmd)
	if m.editForm.Cancelled {
		m.editForm.Close()
		m.editForm = nil
		cmds = append(cmds, m.syncLogs())
	} else if m.editForm.Done {
		if s := m.states[m.editConnection.ID]; s == nil || s.Writing {
			cmds = append(cmds, m.editForm.Reject(fmt.Errorf("connection has another operation pending")))
		} else {
			m.formPending = true
			m.editSubmitting = true
			cmds = append(cmds, m.execute(m.editConnection, m.editForm.Request, false))
		}
	}
	return tea.Batch(cmds...)
}
