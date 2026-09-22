package tui

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/maintenance"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
)

type upgradeState struct {
	Self       *selfupdate.Plan
	Backend    *maintenance.Plan
	Service    *maintenance.Service
	Cancel     context.CancelFunc
	Generation uint64
	Applying   bool
	Lines      []string
}
type upgradeCheckedMsg struct {
	Generation uint64
	Self       *selfupdate.Plan
	Backend    *maintenance.Plan
	Err        error
}
type upgradeAppliedMsg struct {
	Generation uint64
	Connection string
	Message    string
	Err        error
}

// Backend maintenance changes a host installation shared by connection aliases.
// Conservatively serialize all app mutations while that maintenance is active.
func (m *model) backendMaintenanceActive() bool {
	return m.upgrade != nil && m.upgrade.Applying && m.upgrade.Backend != nil
}

func (m *model) checkUpgrade(c *core.Connection) tea.Cmd {
	if m.upgrade != nil && m.upgrade.Applying {
		m.note("An upgrade is already in progress.")
		return nil
	}
	if m.upgrade != nil && m.upgrade.Cancel != nil {
		m.upgrade.Cancel()
	}
	if c != nil {
		if s := m.states[c.ID]; s != nil && s.Writing {
			m.note("Wait for the pending task operation before checking maintenance.")
			return nil
		}
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.upgradeGeneration++
	gen := m.upgradeGeneration
	m.upgrade = &upgradeState{Cancel: cancel, Generation: gen, Service: maintenance.New(m.backend)}
	m.overlay = "upgrade-loading"
	m.menuIndex = 0
	if c == nil {
		return func() tea.Msg {
			p, e := selfupdate.Check(ctx)
			return upgradeCheckedMsg{Generation: gen, Self: &p, Err: e}
		}
	}
	connection := *c
	service := m.upgrade.Service
	return func() tea.Msg {
		p, e := service.Check(ctx, connection)
		return upgradeCheckedMsg{Generation: gen, Backend: &p, Err: e}
	}
}
func (m *model) acceptUpgradeCheck(v upgradeCheckedMsg) tea.Cmd {
	if m.upgrade == nil || m.upgrade.Generation != v.Generation || m.upgrade.Applying {
		return nil
	}
	if v.Err == nil && (v.Self == nil) == (v.Backend == nil) {
		v.Err = fmt.Errorf("upgrade check did not return one valid plan")
	}
	if v.Err != nil {
		m.note("Upgrade check: " + v.Err.Error())
		if m.upgrade.Cancel != nil {
			m.upgrade.Cancel()
		}
		m.upgrade = nil
		m.upgradeGeneration++
		m.overlay = ""
		return nil
	}
	m.upgrade.Self = v.Self
	m.upgrade.Backend = v.Backend
	if v.Self != nil {
		m.upgrade.Lines = append([]string{"Upgrade lazypueue"}, v.Self.Review...)
		if v.Self.UpToDate() {
			m.upgrade.Lines = append(m.upgrade.Lines, "Already current; no replacement is needed.")
		} else if !v.Self.CanUpgrade {
			m.upgrade.Lines = append(m.upgrade.Lines, "Cannot apply here: "+v.Self.Reason)
			if v.Self.Installation.BuildKind == "development" && v.Self.Installation.Manager == "" {
				m.upgrade.Lines = append(m.upgrade.Lines, "Development builds require an explicit CLI upgrade --force.")
			}
		}
	}
	if v.Backend != nil {
		m.upgrade.Lines = append([]string{"Upgrade Pueue on " + v.Backend.Connection.DisplayName()}, v.Backend.Review...)
		if !v.Backend.CanApply {
			m.upgrade.Lines = append(m.upgrade.Lines, "Cannot apply: "+v.Backend.Reason)
		}
	}
	m.overlay = "upgrade-review"
	m.menuIndex = 0
	return nil
}
func (m *model) upgradeKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	u := m.upgrade
	if u == nil {
		m.overlay = ""
		return nil
	}
	if key == "esc" || key == "n" || key == "enter" {
		m.overlay = ""
		if !u.Applying {
			if u.Cancel != nil {
				u.Cancel()
			}
			m.upgrade = nil
			m.upgradeGeneration++
		}
		return nil
	}
	if key == "ctrl+c" {
		if u.Cancel != nil {
			u.Cancel()
		}
		if !u.Applying {
			m.upgrade = nil
			m.overlay = ""
		} else {
			m.note("Cancelling upgrade; inspect its result/receipt for completed steps.")
		}
		return nil
	}
	if key == "down" || key == "j" {
		m.menuIndex++
		return nil
	}
	if key == "up" || key == "k" {
		m.menuIndex = max(0, m.menuIndex-1)
		return nil
	}
	if key != "y" || k.IsRepeat || m.overlay != "upgrade-review" || u.Applying {
		return nil
	}
	if (u.Self == nil) == (u.Backend == nil) {
		m.note("Check an upgrade before applying it.")
		return nil
	}
	if u.Self != nil && u.Self.UpToDate() {
		m.note("lazypueue is already current; nothing was changed.")
		if u.Cancel != nil {
			u.Cancel()
		}
		m.upgrade = nil
		m.upgradeGeneration++
		m.overlay = "events"
		return nil
	}
	if u.Self != nil && !u.Self.CanUpgrade {
		m.note(u.Self.Reason)
		return nil
	}
	if u.Backend != nil && !u.Backend.CanApply {
		m.note(u.Backend.Reason)
		return nil
	}
	if u.Backend != nil {
		if u.Service == nil {
			m.note("The maintenance service is unavailable; check again.")
			return nil
		}
		current, ok := m.connection(u.Backend.Connection.ID)
		if !ok || !reflect.DeepEqual(current, u.Backend.Connection) {
			m.note("Connection changed after upgrade review; check again.")
			return nil
		}
		if m.states[current.ID] == nil {
			m.note("Connection state is unavailable; check again.")
			return nil
		}
		for id, state := range m.states {
			if state != nil && state.Writing {
				m.note("Wait for the pending operation on " + id + " before backend maintenance.")
				return nil
			}
		}
	}
	if u.Cancel != nil {
		u.Cancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	u.Cancel = cancel
	u.Applying = true
	m.overlay = "upgrade-running"
	m.status = "Upgrade applying; Esc returns to dashboard, Ctrl+C cancels waiting."
	gen := u.Generation
	if u.Backend != nil {
		p := *u.Backend
		service := u.Service
		m.states[p.Connection.ID].Writing = true
		return func() tea.Msg {
			defer cancel()
			r, e := service.Apply(ctx, p, io.Discard)
			message := r.Message
			if r.ReceiptPath != "" {
				message += " · receipt: " + r.ReceiptPath
			}
			return upgradeAppliedMsg{gen, p.Connection.ID, message, e}
		}
	}
	p := *u.Self
	return func() tea.Msg {
		defer cancel()
		r, e := selfupdate.Apply(ctx, p, selfupdate.ApplyOptions{}, io.Discard)
		message := r.Message
		if message == "" {
			message = fmt.Sprintf("%s · %s · %s; start a new invocation to use the updated executable", r.Status, r.LatestVersion, r.Installation.ResolvedPath)
		}
		return upgradeAppliedMsg{Generation: gen, Message: message, Err: e}
	}
}
func (m *model) acceptUpgradeApply(v upgradeAppliedMsg) tea.Cmd {
	u := m.upgrade
	if u == nil || u.Generation != v.Generation || !u.Applying {
		return nil
	}
	if (u.Self == nil) == (u.Backend == nil) {
		return nil
	}
	if u.Backend != nil {
		if v.Connection != u.Backend.Connection.ID {
			return nil
		}
		if s := m.states[v.Connection]; s != nil {
			s.Writing = false
		}
	} else if v.Connection != "" {
		return nil
	}
	u.Applying = false
	if u.Cancel != nil {
		u.Cancel()
	}
	message := v.Message
	if v.Err != nil {
		message += " · " + v.Err.Error()
	}
	m.note(message)
	if strings.HasPrefix(m.overlay, "upgrade-") {
		m.overlay = "events"
		m.menuIndex = 0
	}
	if c, ok := m.connection(v.Connection); ok {
		return m.refresh(c, true)
	}
	return nil
}
