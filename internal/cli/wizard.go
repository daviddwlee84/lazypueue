package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/form"
	"github.com/spf13/cobra"
)

type wizardResultMsg struct {
	result core.Result
	err    error
}
type wizardHost struct {
	task                     *form.TaskModel
	connection               *form.ConnectionModel
	submit                   func() (core.Result, error)
	cancel                   context.CancelFunc
	width, height            int
	pending, done, cancelled bool
	result                   core.Result
	finalErr                 error
}

func (m *wizardHost) Init() tea.Cmd {
	if m.task != nil {
		return m.task.Init()
	}
	return m.connection.Init()
}
func (m *wizardHost) View() tea.View {
	text := ""
	if m.task != nil {
		text = m.task.View(m.width, m.height)
	} else {
		text = m.connection.View(m.width, m.height)
	}
	if m.pending {
		text += "\nApplying… Ctrl+C stops waiting; an accepted operation may still complete."
	}
	v := tea.NewView(text)
	v.AltScreen = true
	return v
}
func (m *wizardHost) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.width, m.height = size.Width, size.Height
	}
	if result, ok := msg.(wizardResultMsg); ok {
		m.pending = false
		if result.err != nil {
			failure := result.err
			if result.result.Message != "" && !strings.Contains(failure.Error(), result.result.Message) {
				failure = fmt.Errorf("%s: %w", result.result.Message, failure)
			}
			if result.result.Unknown {
				m.finalErr = failure
				return m, tea.Quit
			}
			if m.task != nil {
				return m, m.task.Reject(failure)
			}
			return m, m.connection.Reject(failure)
		}
		m.result = result.result
		m.done = true
		return m, tea.Quit
	}
	if m.pending {
		if key, ok := msg.(tea.KeyPressMsg); ok && (key.String() == "ctrl+c" || key.String() == "esc") {
			m.cancel()
			m.cancelled = true
			m.finalErr = &ExitError{130, errors.New("stopped waiting; the operation may already have completed—check the queue before retrying")}
			return m, tea.Quit
		}
		return m, nil
	}
	var command tea.Cmd
	var done, cancelled bool
	if m.task != nil {
		m.task, command = m.task.Update(msg)
		done, cancelled = m.task.Done, m.task.Cancelled
	} else {
		m.connection, command = m.connection.Update(msg)
		done, cancelled = m.connection.Done, m.connection.Cancelled
	}
	if cancelled {
		m.cancelled = true
		return m, tea.Quit
	}
	if done {
		m.pending = true
		return m, func() tea.Msg { result, err := m.submit(); return wizardResultMsg{result, err} }
	}
	return m, command
}
func (a *app) runWizard(cmd *cobra.Command, m *wizardHost) error {
	_, err := tea.NewProgram(m, tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()), tea.WithContext(cmd.Context())).Run()
	if err != nil {
		if cmd.Context().Err() != nil {
			return cmd.Context().Err()
		}
		return err
	}
	if m.finalErr != nil {
		return m.finalErr
	}
	if m.cancelled {
		return &ExitError{130, errors.New("cancelled; no operation was submitted")}
	}
	if !m.done {
		return errors.New("wizard ended before completion")
	}
	return nil
}
func (a *app) runTaskWizard(cmd *cobra.Command, cfg config.Config, model *form.TaskModel) (core.Result, core.AddRequest, string, error) {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	defer model.Close()
	host := &wizardHost{task: model, width: 80, height: 24, cancel: cancel}
	host.submit = func() (core.Result, error) {
		c, err := cfg.Connection(model.ConnectionID)
		if err != nil {
			return core.Result{}, err
		}
		return a.options.Backend.Execute(ctx, c, core.Request{Operation: "add", Add: &model.Request, Guards: model.Guards})
	}
	err := a.runWizard(cmd, host)
	return host.result, model.Request, model.ConnectionID, err
}
func (a *app) runConnectionWizard(cmd *cobra.Command, cfg config.Config, path string, initial core.Connection, editing bool) (core.Connection, error) {
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()
	_ = ctx
	model := form.NewConnection(initial, editing, a.options.Backend)
	model.SavePath = path
	defer model.Close()
	host := &wizardHost{connection: model, width: 80, height: 24, cancel: cancel}
	host.submit = func() (core.Result, error) {
		next := cfg
		next.Connections = append([]core.Connection(nil), cfg.Connections...)
		if err := replaceConnection(&next, initial.ID, model.Connection, editing); err != nil {
			return core.Result{}, err
		}
		if err := config.Save(path, next); err != nil {
			return core.Result{}, fmt.Errorf("save %s: %w", path, err)
		}
		return core.Result{ConnectionID: model.Connection.ID, Message: "Saved connection " + model.Connection.ID}, nil
	}
	err := a.runWizard(cmd, host)
	return model.Connection, err
}
