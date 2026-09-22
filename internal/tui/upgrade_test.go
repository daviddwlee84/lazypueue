package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/maintenance"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
)

func reviewedBackend(t *testing.T) (*model, *fakeBackend) {
	m, b := fixture(t)
	m.upgradeGeneration = 7
	m.upgrade = &upgradeState{Generation: 7, Backend: &maintenance.Plan{Connection: m.cfg.Connections[0], CanApply: true}, Service: maintenance.New(b), Cancel: func() {}}
	m.overlay = "upgrade-review"
	return m, b
}

func TestUpgradeReviewDefaultsNoAndIgnoresRepeat(t *testing.T) {
	for _, value := range []string{"enter", "n", "esc", "ctrl+c"} {
		t.Run(value, func(t *testing.T) {
			m, _ := reviewedBackend(t)
			cancelled := false
			m.upgrade.Cancel = func() { cancelled = true }
			if cmd := m.upgradeKey(key(value)); cmd != nil {
				t.Fatal("default No dispatched work")
			}
			if m.upgrade != nil || !cancelled || m.states["local"].Writing {
				t.Fatal("cancel did not discard the reviewed operation")
			}
		})
	}
	m, _ := reviewedBackend(t)
	event := key("y")
	event.IsRepeat = true
	if cmd := m.upgradeKey(event); cmd != nil || m.upgrade.Applying {
		t.Fatal("repeated y applied an upgrade")
	}
}

func TestBackendMaintenanceWaitsForAnyConnectionWrite(t *testing.T) {
	m, _ := reviewedBackend(t)
	// This can be another alias of the same daemon; do not infer independence
	// solely because the registered connection IDs differ.
	m.states["lab"].Writing = true
	if cmd := m.upgradeKey(key("y")); cmd != nil {
		t.Fatal("maintenance overlapped an existing alias operation")
	}
	if m.upgrade.Applying || m.states["local"].Writing || m.overlay != "upgrade-review" {
		t.Fatal("blocked apply changed state")
	}
	if !strings.Contains(m.status, "pending operation") {
		t.Fatal(m.status)
	}
}

func TestBackendMaintenanceBlocksNewMutationsUntilCompletion(t *testing.T) {
	m, b := reviewedBackend(t)
	if cmd := m.upgradeKey(key("y")); cmd == nil {
		t.Fatal("reviewed apply did not dispatch")
	}
	if !m.backendMaintenanceActive() || !m.states["local"].Writing {
		t.Fatal("maintenance lease is absent")
	}
	cancelled := false
	m.upgrade.Cancel = func() { cancelled = true }
	m.upgradeKey(key("esc"))
	if m.overlay != "" || !m.backendMaintenanceActive() || cancelled {
		t.Fatal("Back released/cancelled a running maintenance operation")
	}
	alias, _ := m.connection("lab")
	effects(m, m.execute(alias, core.Request{Operation: "restart", IDs: []int{7}}, false))
	m.openTask("new")
	if b.writes != 0 || m.taskForm != nil || !m.states["local"].Writing {
		t.Fatal("a second connection could mutate while backend maintenance was applying")
	}
	m.overlay = "upgrade-running"
	m.upgradeKey(key("ctrl+c"))
	if !cancelled || !m.backendMaintenanceActive() {
		t.Fatal("cancellation must retain the lease until the actual result")
	}
	m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 7, Connection: "local", Message: "finished"})
	if m.backendMaintenanceActive() || m.states["local"].Writing {
		t.Fatal("matching completion did not release the lease")
	}
}

func TestStaleUpgradeRepliesCannotUnlockOrReplaceCurrentModal(t *testing.T) {
	m, _ := reviewedBackend(t)
	m.upgrade.Applying = true
	m.states["local"].Writing = true
	m.states["lab"].Writing = true
	m.overlay = "upgrade-running"
	m.status = "current"
	m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 6, Connection: "local", Message: "stale"})
	m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 7, Connection: "lab", Message: "wrong target"})
	m.acceptUpgradeCheck(upgradeCheckedMsg{Generation: 7, Self: &selfupdate.Plan{}})
	if !m.upgrade.Applying || !m.states["local"].Writing || !m.states["lab"].Writing || m.overlay != "upgrade-running" || m.status != "current" || len(m.events) != 0 {
		t.Fatal("stale reply mutated current upgrade state")
	}
	m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 7, Connection: "local", Message: "accepted"})
	if m.upgrade.Applying || m.states["local"].Writing || !m.states["lab"].Writing || m.overlay != "events" {
		t.Fatal("matching result changed the wrong target")
	}
	m.overlay = "help"
	m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 7, Connection: "local", Message: "duplicate"})
	if m.overlay != "help" || len(m.events) != 1 {
		t.Fatal("duplicate result stole the current modal")
	}
}

func TestDoubleClickUpgradeYesCannotImmediatelyCancelMaintenance(t *testing.T) {
	m, _ := reviewedBackend(t)
	m.width, m.height, m.mouse = 120, 36, true
	click := tea.MouseClickMsg{X: 7, Y: 35, Button: tea.MouseLeft}
	release := tea.MouseReleaseMsg{X: 7, Y: 35, Button: tea.MouseLeft}
	m.mouseEvent(click)
	if cmd := m.mouseEvent(release); cmd == nil || !m.backendMaintenanceActive() {
		t.Fatal("first Yes click did not schedule reviewed maintenance")
	}
	cancelled := false
	m.upgrade.Cancel = func() { cancelled = true }
	m.mouseEvent(click)
	if cmd := m.mouseEvent(release); cmd != nil || cancelled || !m.backendMaintenanceActive() {
		t.Fatal("second click cancelled or repeated maintenance")
	}
}

func TestCancelledUpgradeCheckCannotReopenReview(t *testing.T) {
	m, _ := reviewedBackend(t)
	m.overlay = "upgrade-loading"
	m.upgrade.Backend = nil
	m.upgradeKey(key("esc"))
	m.overlay = "help"
	m.acceptUpgradeCheck(upgradeCheckedMsg{Generation: 7, Backend: &maintenance.Plan{CanApply: true}})
	if m.upgrade != nil || m.overlay != "help" {
		t.Fatal("cancelled check reopened a modal")
	}
}

func TestUpgradeNilPlansAndChangedConnectionDoNotApply(t *testing.T) {
	for _, both := range []bool{false, true} {
		t.Run(map[bool]string{false: "nil", true: "ambiguous"}[both], func(t *testing.T) {
			m, _ := reviewedBackend(t)
			m.upgrade.Backend = nil
			if both {
				m.upgrade.Backend = &maintenance.Plan{CanApply: true}
				m.upgrade.Self = &selfupdate.Plan{Result: selfupdate.Result{CanUpgrade: true}}
			}
			if cmd := m.upgradeKey(key("y")); cmd != nil || m.upgrade.Applying {
				t.Fatal("invalid plans dispatched")
			}
		})
	}
	m, _ := reviewedBackend(t)
	m.cfg.Connections[0].Binary = "changed-after-review"
	if cmd := m.upgradeKey(key("y")); cmd != nil || m.upgrade.Applying || m.states["local"].Writing {
		t.Fatal("changed connection dispatched")
	}
	if !strings.Contains(m.status, "changed after upgrade review") {
		t.Fatal(m.status)
	}
}

func TestSelfUpdateDoesNotClaimBackendMaintenanceLease(t *testing.T) {
	m, _ := fixture(t)
	m.upgrade = &upgradeState{Applying: true, Self: &selfupdate.Plan{}}
	if m.backendMaintenanceActive() {
		t.Fatal("updating the dashboard incorrectly blocks queues")
	}
	current := selfupdate.Plan{Result: selfupdate.Result{LatestVersion: "v1.0.0", Installation: selfupdate.Installation{Version: "v1.0.0", BuildKind: "release", IdentityValid: true, Method: "go-install"}, Reason: "Go unavailable"}}
	m.upgrade = &upgradeState{Generation: 1, Self: &current}
	m.overlay = "upgrade-review"
	if cmd := m.upgradeKey(key("y")); cmd != nil || m.upgrade != nil {
		t.Fatal("current executable required an update")
	}
}

func TestInvalidUpgradeCheckCannotReachApply(t *testing.T) {
	m, _ := fixture(t)
	m.upgrade = &upgradeState{Generation: 3}
	m.overlay = "upgrade-loading"
	m.acceptUpgradeCheck(upgradeCheckedMsg{Generation: 3})
	if m.upgrade != nil || m.overlay != "" || !strings.Contains(m.status, "one valid plan") {
		t.Fatal("invalid check was accepted")
	}
}

func TestManagedSelfUpgradeUsesReviewAndCancellationWithoutBackendLease(t *testing.T) {
	for _, action := range []string{"enter", "n", "esc", "y"} {
		t.Run(action, func(t *testing.T) {
			m, _ := fixture(t)
			cancelled := false
			m.upgrade = &upgradeState{Generation: 4, Cancel: func() { cancelled = true }}
			m.overlay = "upgrade-loading"
			plan := selfupdate.Plan{Result: selfupdate.Result{CanUpgrade: true, ManagerCommand: []string{"/owned/brew", "upgrade", "acme/tools/lazypueue"}, Installation: selfupdate.Installation{Manager: "homebrew", Method: "package-manager"}}, Review: []string{"Command: /owned/brew upgrade acme/tools/lazypueue", "Verify after upgrade: /owned/opt/lazypueue/bin/lazypueue"}}
			m.acceptUpgradeCheck(upgradeCheckedMsg{Generation: 4, Self: &plan})
			if !strings.Contains(strings.Join(m.upgrade.Lines, "\n"), "acme/tools/lazypueue") || m.overlay != "upgrade-review" {
				t.Fatal("manager review missing")
			}
			cmd := m.upgradeKey(key(action))
			if action != "y" {
				if cmd != nil || m.upgrade != nil || !cancelled {
					t.Fatal("default-no applied manager upgrade")
				}
				return
			}
			if cmd == nil || !m.upgrade.Applying || m.backendMaintenanceActive() || m.overlay != "upgrade-running" {
				t.Fatal("approved manager update did not dispatch correctly")
			}
			cancelled = false
			cancel := m.upgrade.Cancel
			m.upgrade.Cancel = func() { cancelled = true; cancel() }
			m.upgradeKey(key("ctrl+c"))
			if !cancelled || !m.upgrade.Applying {
				t.Fatal("cancel must wait for actual completion")
			}
			m.acceptUpgradeApply(upgradeAppliedMsg{Generation: 4, Message: "Homebrew kept lazypueue v1.0.0; its formula may lag GitHub releases."})
			if m.upgrade.Applying || !strings.Contains(m.status, "may lag GitHub") {
				t.Fatal("lost actual manager outcome", m.status)
			}
		})
	}
}
