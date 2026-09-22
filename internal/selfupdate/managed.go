package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daviddwlee84/lazypueue/internal/brewupgrade"
)

func checkManaged(ctx context.Context, plan Plan, opts runOptions) (Plan, error) {
	unmanaged := plan
	plan.Status = "checked"
	plan.Reason = managerInstruction(plan.Installation.Manager)
	plan.Review = []string{"Executable: " + plan.Installation.Executable, "Destination: " + plan.Installation.ResolvedPath, plan.Reason}
	if plan.Installation.Manager != "" && plan.Installation.Manager != "homebrew" {
		return plan, nil
	}
	managed, err := brewupgrade.Prepare(ctx, plan.Installation.Executable, "lazypueue", brewupgrade.Options{
		LookPath: opts.lookPath,
		Run:      opts.managerRun,
		Inspect: func(ctx context.Context, path string) (string, error) {
			return inspectManagedVersion(ctx, path, opts)
		},
	})
	if errors.Is(err, brewupgrade.ErrNotManaged) && unmanaged.Installation.Manager == "" {
		return unmanaged, nil
	}
	plan.Installation.Manager, plan.Installation.Method = "homebrew", "package-manager"
	plan.Reason = managerInstruction("homebrew")
	plan.Installation.Reason = plan.Reason
	if err != nil {
		if ctx.Err() != nil {
			return plan, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return plan, err
		}
		plan.Reason += "; automatic delegation unavailable: " + err.Error()
		plan.Review[len(plan.Review)-1] = plan.Reason
		return plan, nil
	}
	plan.brew = &managed
	plan.CurrentVersion = managed.CurrentVersion
	plan.Installation.Version = managed.CurrentVersion
	plan.CanUpgrade = true
	plan.Reason = "Homebrew selects the available formula version during apply; GitHub latest is not used."
	plan.ManagerCommand = managed.Command()
	plan.Review = []string{
		"Executable: " + managed.CurrentPath,
		"Installed: " + managed.CurrentVersion,
		"Homebrew formula: " + managed.Formula,
		"Command: " + strings.Join(managed.Command(), " "),
		"Verify after upgrade: " + managed.StablePath,
		plan.Reason,
		"Homebrew owns this transaction; start a new invocation after it finishes.",
	}
	return plan, nil
}

func inspectManagedVersion(ctx context.Context, path string, opts runOptions) (string, error) {
	info, err := opts.inspectCandidate(path)
	if err != nil || !info.IdentityValid || info.GOOS != runtime.GOOS || info.GOARCH != runtime.GOARCH {
		return "", errors.New("Homebrew executable does not identify the native lazypueue package and module")
	}
	output, err := opts.version(ctx, path)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", err
		}
		return "", errors.New("Homebrew executable failed its version check")
	}
	output = strings.TrimSpace(output)
	version := strings.TrimPrefix(output, "lazypueue version ")
	// Proven Homebrew ownership permits truthful HEAD/development labels. This
	// does not change the standalone updater's stable-release policy.
	if !strings.HasPrefix(output, "lazypueue version ") || !utf8.ValidString(version) || version == "" || len(version) > 128 || strings.IndexFunc(version, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || (StableVersion(info.Version) && info.Version != version) {
		return "", errors.New("Homebrew executable reported an unexpected lazypueue version")
	}
	return version, nil
}

func applyManaged(ctx context.Context, plan Plan, progress io.Writer) (Result, error) {
	result := plan.Result
	result.ManagerCommand = plan.brew.Command()
	result.Status = "managed-failed"
	result.Message = "Homebrew upgrade did not complete verification; inspect the formula before retrying."
	outcome, err := plan.brew.Apply(ctx, progress)
	if err != nil {
		return result, err
	}
	result.InstalledVersion = outcome.Version
	result.InstalledPath = outcome.Path
	result.InstalledResolvedPath = outcome.ResolvedPath
	result.Status = "managed-updated"
	result.Message = fmt.Sprintf("Homebrew installed lazypueue %s at %s; start a new invocation to use it.", outcome.Version, outcome.Path)
	if !outcome.Changed {
		result.Status = "managed-unchanged"
		result.Message = fmt.Sprintf("Homebrew kept lazypueue %s at %s; its formula may lag GitHub releases.", outcome.Version, outcome.Path)
	}
	return result, nil
}
