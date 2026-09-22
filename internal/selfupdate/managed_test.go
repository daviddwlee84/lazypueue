package selfupdate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type managedFixture struct {
	t                          *testing.T
	rack, opt, brew, old, link string
	opts                       runOptions
	calls                      [][]string
	upgrades                   int
	onUpgrade                  func(context.Context) error
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &managedFixture{t: t, rack: filepath.Join(root, "Cellar", "actual-formula"), opt: filepath.Join(root, "opt", "actual-formula"), brew: filepath.Join(root, "bin", "brew"), link: filepath.Join(root, "bin", "lazypueue")}
	f.write(f.brew, "fake brew")
	f.old = f.install("1.0.0", "lazypueue version v1.0.0")
	f.symlink(f.old, f.link)
	f.opts = runOptions{
		inspect: func() (Installation, error) {
			return Installation{Executable: f.link, ResolvedPath: f.old, Version: "dev", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, BuildKind: "development", Method: "package-manager", Manager: "homebrew", IdentityValid: true}, nil
		},
		latest: func(context.Context) (Release, error) {
			t.Fatal("managed update consulted GitHub latest")
			return Release{}, nil
		},
		lookPath: func(name string) (string, error) {
			if name != "brew" {
				t.Fatalf("managed update requested %s", name)
			}
			return f.brew, nil
		},
		build: func(context.Context, string, string, string) error {
			t.Fatal("managed update built a source candidate")
			return nil
		},
		download: func(context.Context, Release, string) error {
			t.Fatal("managed update downloaded an archive")
			return nil
		},
		inspectCandidate: func(path string) (Installation, error) {
			data, err := os.ReadFile(path)
			return Installation{IdentityValid: strings.HasPrefix(string(data), "lazypueue version "), Version: "dev", BuildKind: "development", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, err
		},
		version: func(_ context.Context, path string) (string, error) {
			data, err := os.ReadFile(path)
			return string(data), err
		},
		managerRun: func(ctx context.Context, program string, args []string, progress io.Writer) (string, error) {
			if program != f.brew || len(args) != 2 || args[1] != "acme/tools/actual-formula" {
				t.Fatalf("unexpected manager command: %s %q", program, args)
			}
			f.calls = append(f.calls, append([]string(nil), args...))
			switch args[0] {
			case "--cellar":
				return f.rack, nil
			case "--prefix":
				return f.opt, nil
			case "upgrade":
				f.upgrades++
				fmt.Fprintln(progress, "brew progress")
				if f.onUpgrade != nil {
					return "", f.onUpgrade(ctx)
				}
				return "", nil
			default:
				t.Fatalf("unexpected manager action: %q", args)
				return "", nil
			}
		},
	}
	return f
}

func (f *managedFixture) write(path, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *managedFixture) symlink(target, path string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		f.t.Skipf("symlink fixture unavailable: %v", err)
	}
}

func (f *managedFixture) install(keg, output string) string {
	f.t.Helper()
	path := filepath.Join(f.rack, keg, "bin", "lazypueue")
	f.write(path, output)
	f.write(filepath.Join(f.rack, keg, "INSTALL_RECEIPT.json"), `{"source":{"tap":"acme/tools"}}`)
	f.symlink(filepath.Join(f.rack, keg), f.opt)
	return path
}

func TestManagedCheckPlansExactOwnerWithoutGitHubOrUpgrade(t *testing.T) {
	f := newManagedFixture(t)
	plan, err := check(context.Background(), f.opts)
	if err != nil || !plan.CanUpgrade || plan.UpToDate() || plan.LatestVersion != "" || plan.CurrentVersion != "v1.0.0" || f.upgrades != 0 {
		t.Fatalf("plan=%+v err=%v upgrades=%d", plan, err, f.upgrades)
	}
	if !reflect.DeepEqual(plan.ManagerCommand, []string{f.brew, "upgrade", "acme/tools/actual-formula"}) || !reflect.DeepEqual(f.calls, [][]string{{"--cellar", "acme/tools/actual-formula"}, {"--prefix", "acme/tools/actual-formula"}}) {
		t.Fatalf("wrong command: %v calls=%v", plan.ManagerCommand, f.calls)
	}
	data, err := os.ReadFile(f.old)
	if err != nil || string(data) != "lazypueue version v1.0.0" {
		t.Fatal("check changed installed binary", err)
	}
	if !strings.Contains(strings.Join(plan.Review, "\n"), f.opt) {
		t.Fatal("review omits stable target")
	}
}

func TestReceiptDiscoversCustomCellarBeforeStandaloneFallback(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid-receipt=%t", invalid), func(t *testing.T) {
			f := newManagedFixture(t)
			f.rack = filepath.Join(filepath.Dir(filepath.Dir(f.rack)), "custom storage", "packages", "actual-formula")
			f.old = f.install("1.0.0", "lazypueue version v1.0.0")
			f.symlink(f.old, f.link)
			inspect := f.opts.inspect
			f.opts.inspect = func() (Installation, error) {
				installation, err := inspect()
				installation.Manager, installation.Method = "", "go-install"
				return installation, err
			}
			if invalid {
				f.write(filepath.Join(f.rack, "1.0.0", "INSTALL_RECEIPT.json"), "invalid JSON")
			}
			plan, err := check(context.Background(), f.opts)
			if err != nil || plan.Installation.Manager != "homebrew" || plan.CanUpgrade == invalid {
				t.Fatalf("custom receipt escaped ownership: plan=%+v err=%v", plan, err)
			}
			if invalid {
				if _, err := apply(context.Background(), plan, ApplyOptions{Force: true}, nil, f.opts); err == nil {
					t.Fatal("invalid receipt fell through to source with force")
				}
			}
		})
	}
}

func TestManagedApplyReportsVerifiedStableTargetAndManagerNoOp(t *testing.T) {
	for _, unchanged := range []bool{false, true} {
		t.Run(fmt.Sprintf("unchanged=%t", unchanged), func(t *testing.T) {
			f := newManagedFixture(t)
			plan, err := check(context.Background(), f.opts)
			if err != nil {
				t.Fatal(err)
			}
			wantVersion, wantPath, wantStatus := "v1.0.0", f.old, "managed-unchanged"
			if !unchanged {
				wantVersion, wantStatus = "v1.1.0", "managed-updated"
				f.onUpgrade = func(context.Context) error {
					wantPath = f.install("1.1.0", "lazypueue version v1.1.0")
					return os.RemoveAll(filepath.Join(f.rack, "1.0.0"))
				}
			}
			// The preview is public data, not authority to select a different package.
			plan.ManagerCommand[2] = "another-formula"
			var progress bytes.Buffer
			result, err := apply(context.Background(), plan, ApplyOptions{Force: true}, &progress, f.opts)
			if err != nil || result.Status != wantStatus || result.InstalledVersion != wantVersion || result.LatestVersion != "" || result.CurrentVersion != "v1.0.0" || result.InstalledResolvedPath != wantPath || result.InstalledPath != filepath.Join(f.opt, "bin", "lazypueue") || result.Installation.ResolvedPath != f.old || f.upgrades != 1 {
				t.Fatalf("result=%+v err=%v upgrades=%d", result, err, f.upgrades)
			}
			if !strings.Contains(progress.String(), "brew progress") {
				t.Fatal("manager output lost")
			}
			if unchanged && !strings.Contains(result.Message, "may lag GitHub") {
				t.Fatal("no-op claimed latest", result.Message)
			}
		})
	}
}

func TestManagedHEADVersionDoesNotBypassStandaloneDevelopmentPolicy(t *testing.T) {
	for _, version := range []string{"HEAD-abcdef", "dev"} {
		t.Run(version, func(t *testing.T) {
			f := newManagedFixture(t)
			f.old = f.install("HEAD", "lazypueue version "+version)
			f.symlink(f.old, f.link)
			plan, err := check(context.Background(), f.opts)
			if err != nil || !plan.CanUpgrade || plan.CurrentVersion != version {
				t.Fatalf("verified Homebrew development version rejected: %+v %v", plan, err)
			}
			result, err := apply(context.Background(), plan, ApplyOptions{}, nil, f.opts)
			if err != nil || result.Status != "managed-unchanged" || result.InstalledVersion != version || f.upgrades != 1 {
				t.Fatalf("Homebrew development apply: %+v %v", result, err)
			}

			installation, opts := updateFixture(t)
			installation.BuildKind, installation.Version = "development", version
			opts.inspect = func() (Installation, error) { return installation, nil }
			plan, err = check(context.Background(), opts)
			if err != nil || plan.CanUpgrade || !strings.Contains(plan.Reason, "development build") {
				t.Fatalf("standalone development policy changed: %+v %v", plan, err)
			}
			if _, err := apply(context.Background(), plan, ApplyOptions{}, nil, opts); err == nil {
				t.Fatal("managed version allowance bypassed standalone development protection")
			}
			requireOriginal(t, installation)
		})
	}
}

func TestManagedFailureAndCancellationNeverFallBack(t *testing.T) {
	for _, failure := range []string{"failure", "cancel", "wrong-product", "wrong-version"} {
		t.Run(failure, func(t *testing.T) {
			f := newManagedFixture(t)
			plan, err := check(context.Background(), f.opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.onUpgrade = func(context.Context) error {
				switch failure {
				case "failure":
					return errors.New("manager failed")
				case "cancel":
					cancel()
					return context.Canceled
				case "wrong-product":
					f.install("1.1.0", "another product v1.1.0")
				case "wrong-version":
					f.install("1.1.0", "lazypueue version invalid version")
				}
				return nil
			}
			result, err := apply(ctx, plan, ApplyOptions{Force: true}, nil, f.opts)
			if err == nil || result.Status != "managed-failed" || result.InstalledVersion != "" || result.LatestVersion != "" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation", err)
			}
			if data, err := os.ReadFile(f.old); err != nil || string(data) != "lazypueue version v1.0.0" {
				t.Fatal("direct replacement after manager failure", err)
			}
		})
	}
}

func TestUnverifiedManagersRemainGuidanceEvenWithForce(t *testing.T) {
	for _, manager := range []string{"nix", "mise", "homebrew"} {
		t.Run(manager, func(t *testing.T) {
			installation, opts := updateFixture(t)
			installation.Manager, installation.Method = manager, "package-manager"
			opts.inspect = func() (Installation, error) { return installation, nil }
			opts.latest = func(context.Context) (Release, error) {
				t.Fatal("manager guidance queried GitHub")
				return Release{}, nil
			}
			opts.lookPath = func(string) (string, error) { t.Fatal("unverified ownership ran a command"); return "", nil }
			plan, err := check(context.Background(), opts)
			if err != nil || plan.CanUpgrade || plan.Reason == "" {
				t.Fatalf("plan=%+v err=%v", plan, err)
			}
			if _, err := apply(context.Background(), plan, ApplyOptions{Force: true}, nil, opts); err == nil {
				t.Fatal("force bypassed package ownership")
			}
			requireOriginal(t, installation)
		})
	}
}

func TestManagedVersionRequiresNativeIdentityAndMatchingMetadata(t *testing.T) {
	for _, mismatch := range []string{"identity", "platform", "version", "version-prefix", "version-control", "version-long", "version-empty", "version-utf8"} {
		t.Run(mismatch, func(t *testing.T) {
			_, opts := updateFixture(t)
			if mismatch == "version-prefix" {
				opts.version = func(context.Context, string) (string, error) { return "v0.1.2", nil }
			}
			for kind, output := range map[string]string{
				"version-control": "lazypueue version HEAD\x1b",
				"version-long":    "lazypueue version " + strings.Repeat("x", 129),
				"version-empty":   "lazypueue version \n",
				"version-utf8":    "lazypueue version HEAD\xff",
			} {
				if mismatch == kind {
					opts.version = func(context.Context, string) (string, error) { return output, nil }
				}
			}
			opts.inspectCandidate = func(string) (Installation, error) {
				info := Installation{IdentityValid: true, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Version: "v0.1.2"}
				if strings.HasPrefix(mismatch, "version-") {
					info.Version = "dev"
				}
				switch mismatch {
				case "identity":
					info.IdentityValid = false
				case "platform":
					info.GOARCH = "wrong"
				case "version":
					info.Version = "v9.0.0"
				}
				return info, nil
			}
			if _, err := inspectManagedVersion(context.Background(), "candidate", opts); err == nil {
				t.Fatal("accepted invalid managed executable")
			}
		})
	}
}
