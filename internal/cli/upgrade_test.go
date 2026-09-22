package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
)

func TestSelfUpgradeCheckSkipsQueueAndConfig(t *testing.T) {
	f := setup(t)
	t.Setenv("LAZYPUEUE_CONFIG", "/does/not/exist")
	for _, flag := range []string{"--check", "--dry-run"} {
		var out, stderr bytes.Buffer
		applied := false
		root := NewRoot(Options{Backend: f, Output: &out, ErrorOutput: &stderr, Input: strings.NewReader(""), IsTerminal: func() bool { return false }, SelfCheck: func(context.Context) (selfupdate.Plan, error) {
			return selfupdate.Plan{Result: selfupdate.Result{Status: "source-unavailable", Reason: "no release"}}, nil
		}, SelfApply: func(context.Context, selfupdate.Plan, selfupdate.ApplyOptions, io.Writer) (selfupdate.Result, error) {
			applied = true
			return selfupdate.Result{}, nil
		}})
		root.SetArgs([]string{"upgrade", flag, "--json"})
		if err := root.ExecuteContext(context.Background()); err != nil || applied || f.reads+f.writes != 0 {
			t.Fatalf("check used queue/config: %v %s", err, &out)
		}
	}
}
func TestSelfUpgradeRequiresApprovalAndPreservesExactPlan(t *testing.T) {
	f := setup(t)
	applied := 0
	plan := selfupdate.Plan{Result: selfupdate.Result{CanUpgrade: true, LatestVersion: "v1.2.3", Installation: selfupdate.Installation{ResolvedPath: "/temporary/lazypueue"}}}
	for _, yes := range []bool{false, true} {
		var out, stderr bytes.Buffer
		root := NewRoot(Options{Backend: f, Output: &out, ErrorOutput: &stderr, Input: strings.NewReader(""), IsTerminal: func() bool { return false }, SelfCheck: func(context.Context) (selfupdate.Plan, error) { return plan, nil }, SelfApply: func(ctx context.Context, p selfupdate.Plan, o selfupdate.ApplyOptions, w io.Writer) (selfupdate.Result, error) {
			if p.LatestVersion != plan.LatestVersion || p.Installation.ResolvedPath != plan.Installation.ResolvedPath {
				t.Fatal("plan changed")
			}
			applied++
			return selfupdate.Result{Status: "updated", LatestVersion: p.LatestVersion}, nil
		}})
		args := []string{"upgrade", "--json"}
		if yes {
			args = append(args, "--yes")
		}
		root.SetArgs(args)
		err := root.ExecuteContext(context.Background())
		if !yes && ExitCode(err) != 2 {
			t.Fatalf("missing approval: %v", err)
		}
		if yes && err != nil {
			t.Fatal(err)
		}
	}
	if applied != 1 {
		t.Fatalf("applied %d times", applied)
	}
}
func TestBackendAllApplyRejectedBeforeHostDiscovery(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	root := NewRoot(Options{Backend: f, Input: strings.NewReader(""), Output: &out, ErrorOutput: io.Discard, IsTerminal: func() bool { return false }})
	root.SetArgs([]string{"backend", "upgrade", "--connection", "all", "--yes"})
	err := root.ExecuteContext(context.Background())
	if ExitCode(err) != 2 || f.reads+f.writes != 0 {
		t.Fatalf("all touched hosts: %v", err)
	}
}

func TestCurrentSelfReleaseDoesNotRequireGoOrApproval(t *testing.T) {
	f := setup(t)
	var out bytes.Buffer
	applied := false
	root := NewRoot(Options{Backend: f, Output: &out, ErrorOutput: io.Discard, Input: strings.NewReader(""), IsTerminal: func() bool { return false }, SelfCheck: func(context.Context) (selfupdate.Plan, error) {
		return selfupdate.Plan{Result: selfupdate.Result{LatestVersion: "v1.0.0", Reason: "Go is unavailable", Installation: selfupdate.Installation{Version: "v1.0.0", BuildKind: "release", IdentityValid: true, Method: "go-install"}}}, nil
	}, SelfApply: func(context.Context, selfupdate.Plan, selfupdate.ApplyOptions, io.Writer) (selfupdate.Result, error) {
		applied = true
		return selfupdate.Result{}, nil
	}})
	root.SetArgs([]string{"upgrade", "--json"})
	if err := root.ExecuteContext(context.Background()); err != nil || applied || !strings.Contains(out.String(), `"status": "up-to-date"`) {
		t.Fatalf("no-op failed: %v %s", err, &out)
	}
}

func TestForceDoesNotPromptForPackageOwnedSelfUpdate(t *testing.T) {
	f := setup(t)
	var stderr bytes.Buffer
	applied := false
	root := NewRoot(Options{Backend: f, Output: io.Discard, ErrorOutput: &stderr, Input: strings.NewReader("yes\n"), IsTerminal: func() bool { return true }, SelfCheck: func(context.Context) (selfupdate.Plan, error) {
		return selfupdate.Plan{Result: selfupdate.Result{LatestVersion: "v1.0.0", Reason: "Homebrew owns this executable", Installation: selfupdate.Installation{Version: "v1.0.0", BuildKind: "release", IdentityValid: true, Method: "package-manager", Manager: "homebrew"}}}, nil
	}, SelfApply: func(context.Context, selfupdate.Plan, selfupdate.ApplyOptions, io.Writer) (selfupdate.Result, error) {
		applied = true
		return selfupdate.Result{}, nil
	}})
	root.SetArgs([]string{"upgrade", "--force"})
	if err := root.ExecuteContext(context.Background()); err == nil || applied || strings.Contains(stderr.String(), "[y/N]") {
		t.Fatalf("package-owned copy prompted/applied: %v %s", err, &stderr)
	}
}

func TestManagedUpgradePreservesCheckApprovalAndJSON(t *testing.T) {
	for _, mode := range []string{"check", "dry-run", "unapproved", "approved", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			var out, stderr bytes.Buffer
			calls := 0
			plan := selfupdate.Plan{Result: selfupdate.Result{CanUpgrade: true, CurrentVersion: "v1.0.0", ManagerCommand: []string{"/owned/brew", "upgrade", "acme/tools/lazypueue"}, Installation: selfupdate.Installation{Manager: "homebrew", Method: "package-manager", IdentityValid: true, Version: "v1.0.0"}}}
			root := NewRoot(Options{Backend: f, Output: &out, ErrorOutput: &stderr, Input: strings.NewReader("yes\n"), IsTerminal: func() bool { return true }, SelfCheck: func(context.Context) (selfupdate.Plan, error) { return plan, nil }, SelfApply: func(ctx context.Context, p selfupdate.Plan, o selfupdate.ApplyOptions, progress io.Writer) (selfupdate.Result, error) {
				calls++
				if !o.Force || strings.Join(p.ManagerCommand, " ") != "/owned/brew upgrade acme/tools/lazypueue" {
					t.Fatal("lost reviewed manager plan")
				}
				io.WriteString(progress, "raw manager progress")
				r := p.Result
				r.Status, r.InstalledVersion, r.Message = "managed-unchanged", "v1.0.0", "Homebrew kept v1.0.0; its formula may lag GitHub releases."
				if mode == "cancelled" {
					r.Status = "managed-failed"
					return r, context.Canceled
				}
				return r, nil
			}})
			args := []string{"upgrade", "--json", "--force"}
			switch mode {
			case "check":
				args = append(args, "--check")
			case "dry-run":
				args = append(args, "--dry-run")
			case "approved", "cancelled":
				args = append(args, "--yes")
			}
			root.SetArgs(args)
			err := root.ExecuteContext(context.Background())
			if mode == "unapproved" {
				if ExitCode(err) != 2 || calls != 0 {
					t.Fatalf("unapproved apply: calls=%d err=%v", calls, err)
				}
				return
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("lost cancellation", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if err := json.Unmarshal(out.Bytes(), &result); err != nil {
				t.Fatalf("stdout not JSON: %s %v", &out, err)
			}
			if mode == "check" || mode == "dry-run" {
				if calls != 0 {
					t.Fatal("check upgraded")
				}
			} else if calls != 1 || result["installed_version"] != "v1.0.0" {
				t.Fatalf("wrong result: %v calls=%d", result, calls)
			}
			if result["latest_version"] != nil || strings.Contains(out.String()+stderr.String(), "raw manager progress") || strings.Contains(stderr.String(), "[y/N]") || f.reads+f.writes != 0 {
				t.Fatalf("managed JSON/check leaked or touched queue: %s %s", &out, &stderr)
			}
		})
	}
}

func TestManagedTerminalPromptNamesExactCommandAndActualOutcome(t *testing.T) {
	f := setup(t)
	var out, stderr bytes.Buffer
	root := NewRoot(Options{Backend: f, Output: &out, ErrorOutput: &stderr, Input: strings.NewReader("yes\n"), IsTerminal: func() bool { return true }, SelfCheck: func(context.Context) (selfupdate.Plan, error) {
		return selfupdate.Plan{Result: selfupdate.Result{CanUpgrade: true, ManagerCommand: []string{"/owned/brew", "upgrade", "acme/tools/custom-name"}, Installation: selfupdate.Installation{Manager: "homebrew"}}}, nil
	}, SelfApply: func(_ context.Context, _ selfupdate.Plan, _ selfupdate.ApplyOptions, progress io.Writer) (selfupdate.Result, error) {
		io.WriteString(progress, "brew progress")
		return selfupdate.Result{Status: "managed-unchanged", InstalledVersion: "v1.0.0", Message: "Homebrew kept lazypueue v1.0.0; its formula may lag GitHub releases."}, nil
	}})
	root.SetArgs([]string{"upgrade"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "/owned/brew upgrade acme/tools/custom-name [y/N]") || !strings.Contains(stderr.String(), "brew progress") || strings.Contains(out.String(), "brew progress") || !strings.Contains(out.String(), "kept lazypueue v1.0.0") {
		t.Fatalf("wrong output: stdout=%s stderr=%s", &out, &stderr)
	}
}
