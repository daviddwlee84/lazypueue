package selfupdate

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"runtime/debug"
	"strings"
)

// Plan binds the release and destination inspected before review. Its private
// identity cannot be reconstructed from untrusted JSON or redirected via PATH.
type Plan struct {
	Result
	Review   []string `json:"review"`
	original installationSnapshot
	release  Release
	checked  bool
}

type ApplyOptions struct{ Force bool }

// UpToDate is a safe no-op even if Go is absent or the destination is not
// writable. Installation ownership and provenance still take precedence.
func (p Plan) UpToDate() bool {
	if p.Status == "source-unavailable" || p.Installation.BuildKind != "release" || upgradePolicy(p.Installation, false) != "" {
		return false
	}
	comparison, err := CompareVersions(p.LatestVersion, p.Installation.Version)
	return err == nil && comparison <= 0
}

var ErrSourceUnavailable = errors.New("no formal stable lazypueue release is available")

func defaultOptions() runOptions {
	return runOptions{inspect: Inspect, latest: latestRelease, lookPath: exec.LookPath, build: buildRelease, inspectCandidate: InspectPath, version: candidateVersion}
}

// Check never writes files, starts Pueue, or reads queue configuration.
func Check(ctx context.Context) (Plan, error) { return check(ctx, defaultOptions()) }
func check(ctx context.Context, opts runOptions) (Plan, error) {
	var plan Plan
	installation, err := opts.inspect()
	if err != nil {
		return plan, err
	}
	plan.Installation = installation
	plan.CurrentVersion = installation.Version
	plan.checked = true
	if installation.IdentityValid && installation.Manager == "" {
		plan.original, err = snapshotInstallation(installation)
		if err != nil {
			return plan, err
		}
	}
	release, err := opts.latest(ctx)
	if errors.Is(err, ErrSourceUnavailable) {
		plan.Status = "source-unavailable"
		plan.Reason = ErrSourceUnavailable.Error()
		plan.Review = []string{plan.Reason, "Publish a reviewed stable source release before using self-update."}
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.release = release
	opts.inspect = func() (Installation, error) { return installation, nil }
	opts.latest = func(context.Context) (Release, error) { return release, nil }
	plan.Result, err = run(ctx, Request{Check: true}, io.Discard, opts)
	plan.Review = []string{"Executable: " + installation.Executable, "Destination: " + installation.ResolvedPath, "Build: " + installation.BuildKind, "Release: " + release.Version, "Use the inspected installation method: verified archive or exact source build.", "Replace only the verified resolved executable; existing invocations continue until exited."}
	if plan.Reason != "" {
		plan.Review = append(plan.Review, plan.Reason)
	}
	return plan, err
}

// Apply uses the captured exact release; it never resolves latest a second time.
func Apply(ctx context.Context, plan Plan, options ApplyOptions, progress io.Writer) (Result, error) {
	return apply(ctx, plan, options, progress, defaultOptions())
}
func apply(ctx context.Context, plan Plan, options ApplyOptions, progress io.Writer, opts runOptions) (Result, error) {
	if !plan.checked {
		return plan.Result, errors.New("check this executable before applying an update")
	}
	if plan.release.Version == "" {
		return plan.Result, ErrSourceUnavailable
	}
	if plan.Installation.IdentityValid && plan.Installation.Manager == "" {
		if err := plan.original.validate(); err != nil {
			return plan.Result, err
		}
	}
	opts.inspect = func() (Installation, error) { return plan.Installation, nil }
	opts.latest = func(context.Context) (Release, error) { return plan.release, nil }
	return run(ctx, Request{Force: options.Force}, progress, opts)
}

// Version keeps source installations truthful without a release workflow's
// linker flags. Provenance inspection remains separate from this display label.
func Version(injected string) string {
	info, _ := debug.ReadBuildInfo()
	return ResolveVersion(injected, info)
}
func ResolveVersion(injected string, info *debug.BuildInfo) string {
	if v := strings.TrimSpace(injected); v != "" && v != "dev" && v != "(devel)" {
		return v
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
