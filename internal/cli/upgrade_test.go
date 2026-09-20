package cli

import (
	"bytes"
	"context"
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
