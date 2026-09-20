package maintenance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/config"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

func (s *Service) pueueArgs(p Plan, args ...string) []string {
	result := []string{p.Resolved, "--color", "never"}
	if p.queue.ConfigPath != "" {
		result = append(result, "--config", p.queue.ConfigPath)
	}
	if p.queue.Profile != "" {
		result = append(result, "--profile", p.queue.Profile)
	}
	return append(result, args...)
}
func (s *Service) pauseArgs(p Plan) []string { return s.pueueArgs(p, "pause", "--all", "--wait") }
func (s *Service) stopArgs(p Plan) []string {
	if p.ServiceKind == "homebrew" {
		return []string{p.ManagerBinary, "services", "stop", p.ServiceName, "--keep"}
	}
	return []string{"systemctl", "--user", "stop", p.ServiceName}
}
func (s *Service) startArgs(p Plan) []string {
	if p.ServiceKind == "homebrew" {
		return []string{p.ManagerBinary, "services", "start", p.ServiceName}
	}
	return []string{"systemctl", "--user", "start", p.ServiceName}
}
func (s *Service) upgradeArgs(p Plan) []string {
	if p.Owner == "homebrew" {
		return []string{p.ManagerBinary, "upgrade", p.InstallRoot}
	}
	return []string{p.ManagerBinary, "install", "--root", p.InstallRoot, "--locked", "--version", p.CandidateVersion, "pueue"}
}

func (s *Service) apply(ctx context.Context, plan Plan, progress io.Writer) (result Result, err error) {
	result = Result{ConnectionID: plan.Connection.ID, Status: "blocked", Steps: []Step{}}
	if progress == nil {
		progress = io.Discard
	}
	if !plan.checked || !plan.CanApply {
		return result, errors.New("maintenance plan cannot be applied: " + plan.Reason)
	}
	if identity(plan) != plan.identity {
		return result, errors.New("the reviewed maintenance plan was modified; check again")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	// Recheck before any scheduling, package, service, receipt or lock mutation.
	fresh, e := s.Check(ctx, plan.Connection)
	if e != nil {
		return result, e
	}
	if !fresh.CanApply {
		return result, errors.New(fresh.Reason)
	}
	if fresh.identity != plan.identity || fresh.CandidateVersion != plan.CandidateVersion || !sameGroups(fresh.Groups, plan.Groups) {
		return result, errors.New("installation, candidate, service, or group state changed after review; check again")
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	packageKey := sha256.Sum256([]byte(plan.Owner + "\x00" + plan.ManagerBinary + "\x00" + plan.InstallRoot))
	lock := path.Join("/tmp", "lazypueue-maintenance-"+plan.uid+"-"+hex.EncodeToString(packageKey[:10]))
	if _, e = s.Runner.Run(ctx, plan.Target, []string{"sh", "-c", `umask 077; mkdir "$1"`, "lock", lock}); e != nil {
		return result, errors.New("another maintenance operation may own this host/package lock; no queue changes were made")
	}
	result.LockPath = lock
	defer func() {
		if result.Unknown && plan.Target.SSHHost != "" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = s.Runner.Run(cleanup, plan.Target, []string{"rmdir", lock})
	}()
	result.ReceiptPath, e = s.newReceipt()
	if e != nil {
		return result, e
	}
	result.Status = "applying"
	if e = s.writeReceipt(plan, result); e != nil {
		return result, e
	}
	defer func() {
		if err != nil {
			result.Status = "partial"
			result.Message = "Maintenance stopped; completed steps were not rolled back. Inspect the receipt before retrying. " + err.Error()
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				result.Unknown = true
			}
			if result.Unknown && plan.Target.SSHHost != "" {
				result.Message += " The remote maintenance lock remains at " + lock + "; verify remote processes before removing it."
			}
		}
		if receiptErr := s.writeReceipt(plan, result); receiptErr != nil {
			if err == nil {
				err = fmt.Errorf("maintenance completed but receipt update failed: %w", receiptErr)
			} else {
				err = fmt.Errorf("%w; receipt update also failed", err)
			}
		}
	}()
	runStep := func(name string, args []string) error {
		result.Steps = append(result.Steps, Step{Name: name, Command: append([]string(nil), args...), State: "pending", At: time.Now()})
		if e := s.writeReceipt(plan, result); e != nil {
			return e
		}
		fmt.Fprintln(progress, name+"…")
		stepctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		_, e := s.Runner.Run(stepctx, plan.Target, args)
		index := len(result.Steps) - 1
		if e != nil {
			result.Steps[index].State = "failed"
			if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) || plan.Target.SSHHost != "" {
				result.Unknown = true
			}
			return fmt.Errorf("%s: %w", name, e)
		}
		result.Steps[index].State = "completed"
		return s.writeReceipt(plan, result)
	}
	// --wait pauses scheduling but never suspends an existing process.
	if err = runStep("Pause new scheduling", s.pauseArgs(plan)); err != nil {
		return result, err
	}
	snapshot, e := s.Backend.Snapshot(ctx, plan.queue)
	if e != nil {
		return result, fmt.Errorf("verify idle queue after pausing: %w", e)
	}
	if active := activeTasks(snapshot); len(active) > 0 {
		return result, fmt.Errorf("new active tasks %v appeared; package and service were left untouched, scheduling remains paused", active)
	}
	if !allPaused(snapshot.Groups) || !sameGroupNames(snapshot.Groups, plan.Groups) {
		return result, errors.New("group state changed during idle check; package and service were left untouched, scheduling remains paused")
	}
	if err = runStep("Stop verified daemon service", s.stopArgs(plan)); err != nil {
		return result, err
	}
	if err = runStep("Upgrade owned Pueue package", s.upgradeArgs(plan)); err != nil {
		return result, err
	}
	result.PackageUpdated = true
	// Resolve the invocation again: Homebrew changes the Cellar path on upgrade.
	updated, e := s.resolve(ctx, plan.Target, plan.Binary)
	if e != nil {
		return result, fmt.Errorf("verify upgraded client path: %w", e)
	}
	data, e := s.read(ctx, plan.Target, updated, "--version")
	if e != nil {
		return result, e
	}
	actual, e := version(data)
	if e != nil || actual != plan.CandidateVersion {
		return result, fmt.Errorf("package upgrade did not produce reviewed version %s; installed version is %s", plan.CandidateVersion, actual)
	}
	data, e = s.read(ctx, plan.Target, path.Join(path.Dir(updated), "pueued"), "--version")
	if e != nil {
		return result, e
	}
	daemon, e := version(data)
	if e != nil || daemon != actual {
		return result, errors.New("installed pueued does not match the reviewed client version; service remains stopped")
	}
	if err = runStep("Start verified daemon service", s.startArgs(plan)); err != nil {
		return result, err
	}
	result.DaemonStarted = true
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot, e = s.Backend.Snapshot(ctx, plan.queue)
		if e == nil {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			return result, fmt.Errorf("service started but the selected queue did not reconnect: %w", e)
		case <-tick.C:
		}
	}
	if len(activeTasks(snapshot)) > 0 {
		return result, errors.New("active work appeared after service startup; scheduling restoration requires inspection")
	}
	if !sameGroupNames(snapshot.Groups, plan.Groups) {
		return result, errors.New("group set changed during maintenance; scheduling restoration requires inspection")
	}
	verified, e := s.Check(ctx, plan.Connection)
	if e != nil {
		return result, fmt.Errorf("verify restarted supervisor: %w", e)
	}
	if !verified.ready || verified.CurrentVersion != plan.CandidateVersion || verified.ServicePID == plan.ServicePID || verified.ServiceKind != plan.ServiceKind || verified.ServiceName != plan.ServiceName {
		return result, errors.New("the reviewed service has not been verified as a new daemon process; scheduling remains paused")
	}
	// The selected supervisor is now active. Use the updated path for group
	// resume; never retain the old Homebrew keg's executable after replacement.
	resumePlan := plan
	resumePlan.Resolved = updated
	for _, g := range plan.Groups {
		if !strings.EqualFold(g.Status, "running") {
			continue
		}
		if err = runStep("Restore scheduling for group "+g.Name, s.pueueArgs(resumePlan, "start", "--group", g.Name)); err != nil {
			return result, err
		}
	}
	result.Status = "updated"
	result.Message = "Pueue " + actual + " installed; the verified daemon reconnected and original group scheduling was restored."
	return result, nil
}

func allPaused(groups []core.Group) bool {
	for _, g := range groups {
		if !strings.EqualFold(g.Status, "paused") {
			return false
		}
	}
	return true
}
func sameGroups(a, b []core.Group) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]core.Group{}
	for _, g := range a {
		m[g.Name] = g
	}
	for _, g := range b {
		if got, ok := m[g.Name]; !ok || got.Status != g.Status || got.Parallel != g.Parallel {
			return false
		}
	}
	return true
}
func sameGroupNames(a, b []core.Group) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, g := range a {
		m[g.Name] = g.Parallel
	}
	for _, g := range b {
		if n, ok := m[g.Name]; !ok || n != g.Parallel {
			return false
		}
	}
	return true
}
func (s *Service) newReceipt() (string, error) {
	dir := s.ReceiptDir
	if dir == "" {
		state, e := config.StatePath()
		if e != nil {
			return "", e
		}
		dir = filepath.Join(filepath.Dir(state), "maintenance")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	filename := filepath.Join(dir, time.Now().UTC().Format("20060102T150405Z")+"-"+hex.EncodeToString(random[:])+".json")
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	err = f.Close()
	return filename, err
}
func (s *Service) writeReceipt(p Plan, r Result) error {
	if r.ReceiptPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(struct {
		Plan   Plan   `json:"plan"`
		Result Result `json:"result"`
	}{p, r}, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(r.ReceiptPath), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), r.ReceiptPath)
}
