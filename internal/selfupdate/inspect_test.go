package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func releaseBuildInfo() *debug.BuildInfo {
	return &debug.BuildInfo{
		Path: PackagePath,
		Main: debug.Module{Path: ModulePath, Version: "v0.1.1", Sum: "h1:UoCo+7is+GwxDp9Y/C3TGa+ZvAxXoYWgarcRurpBNyI="},
	}
}

func TestBuildProvenance(t *testing.T) {
	tests := []struct {
		name     string
		change   func(*debug.BuildInfo)
		kind     string
		identity bool
	}{
		{"release", func(*debug.BuildInfo) {}, "release", true},
		{"devel", func(b *debug.BuildInfo) { b.Main.Version = "(devel)" }, "development", true},
		{"empty version", func(b *debug.BuildInfo) { b.Main.Version = "" }, "development", true},
		{"pseudo version", func(b *debug.BuildInfo) { b.Main.Version = "v0.0.0-20260920120000-abcdef123456" }, "development", true},
		{"tagged dirty", func(b *debug.BuildInfo) { b.Main.Version = "v0.1.1+dirty" }, "development", true},
		{"prerelease", func(b *debug.BuildInfo) { b.Main.Version = "v0.1.2-beta.1" }, "development", true},
		{"missing checksum", func(b *debug.BuildInfo) { b.Main.Sum = "" }, "development", true},
		{"malformed checksum", func(b *debug.BuildInfo) { b.Main.Sum = "h1:invalid" }, "development", true},
		{"clean local vcs", func(b *debug.BuildInfo) { b.Settings = []debug.BuildSetting{{Key: "vcs", Value: "git"}} }, "development", true},
		{"dirty vcs", func(b *debug.BuildInfo) { b.Settings = []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}} }, "development", true},
		{"dependency replacement", func(b *debug.BuildInfo) {
			b.Deps = []*debug.Module{{Path: "example.com/dependency", Replace: &debug.Module{Path: "../dependency"}}}
		}, "development", true},
		{"main replacement", func(b *debug.BuildInfo) { b.Main.Replace = &debug.Module{Path: "../fork"} }, "unknown", false},
		{"wrong main package", func(b *debug.BuildInfo) { b.Path = ModulePath + "/other" }, "unknown", false},
		{"wrong module", func(b *debug.BuildInfo) { b.Main.Path = "example.com/lazypueue" }, "unknown", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := releaseBuildInfo()
			test.change(info)
			got := classifyBuild("/chosen/lazypueue", "/resolved/lazypueue", info)
			if got.BuildKind != test.kind || got.IdentityValid != test.identity {
				t.Fatalf("got %+v; want kind=%s identity=%v", got, test.kind, test.identity)
			}
			if got.IdentityValid && got.Method != "go-install" {
				t.Fatalf("recognized app has unsupported method: %+v", got)
			}
			if got.BuildKind != "release" && got.Reason == "" {
				t.Fatalf("missing explanation: %+v", got)
			}
		})
	}
	if got := classifyBuild("/original", "/resolved", nil); got.IdentityValid || got.BuildKind != "unknown" {
		t.Fatal(got)
	}
}

func TestReleaseProvenanceSurvivesLocationAndEnvironmentChanges(t *testing.T) {
	t.Setenv("GOBIN", "/new/go/bin")
	t.Setenv("GOPATH", "/new/gopath")
	t.Setenv("PATH", "/another/copy")
	for _, path := range []string{"/home/user/go/bin/lazypueue", "/custom/gobin/lazypueue", "/moved/release/lazypueue", "/opt/homebrew/bin/lazypueue"} {
		got := classifyBuild(path, path, releaseBuildInfo())
		if !got.IdentityValid || got.BuildKind != "release" || got.Executable != path || got.ResolvedPath != path {
			t.Fatalf("path/env changed metadata identification: %+v", got)
		}
	}
}

func TestBuildPlatformMetadata(t *testing.T) {
	info := releaseBuildInfo()
	info.Settings = []debug.BuildSetting{{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "arm64"}}
	got := classifyBuild("/original", "/resolved", info)
	if got.GOOS != "linux" || got.GOARCH != "arm64" || got.BuildKind != "release" {
		t.Fatalf("platform metadata not retained: %+v", got)
	}
	info.Settings = nil
	got = classifyBuild("/original", "/resolved", info)
	if got.GOOS != "" || got.GOARCH != "" {
		t.Fatalf("missing platform metadata was invented: %+v", got)
	}
}

func TestPackageManagerOwnership(t *testing.T) {
	tests := []struct {
		path    string
		env     managerEnv
		manager string
	}{
		{"/opt/homebrew/Cellar/lazypueue/0.1.1/bin/lazypueue", managerEnv{}, "homebrew"},
		{"/usr/local/Cellar/lazypueue/0.1.1/libexec/bin/lazypueue", managerEnv{}, "homebrew"},
		{"/home/linuxbrew/.linuxbrew/Cellar/lazypueue/0.1.1/bin/lazypueue", managerEnv{}, "homebrew"},
		{"/custom/cellar/lazypueue/0.1.1/bin/lazypueue", managerEnv{HomebrewCellar: "/custom/cellar"}, "homebrew"},
		{"/opt/homebrew/bin/lazypueue", managerEnv{}, ""},
		{"/tmp/homebrew/lazypueue", managerEnv{}, ""},
		{"/opt/homebrew/Cellar-other/lazypueue/0.1.1/bin/lazypueue", managerEnv{}, ""},
		{"/opt/homebrew/Cellar/lazypueue", managerEnv{}, ""},
		{"/tmp/Cellar/lazypueue/0.1.1/bin/lazypueue", managerEnv{}, ""},
		{"/nix/store/0123456789-lazypueue-0.1.1/bin/lazypueue", managerEnv{}, "nix"},
		{"/nix/store-other/lazypueue", managerEnv{}, ""},
		{"/tmp/nix/store/0123456789-lazypueue/bin/lazypueue", managerEnv{}, ""},
		{"/home/user/.local/share/mise/installs/lazypueue/0.1.1/bin/lazypueue", managerEnv{Home: "/home/user"}, "mise"},
		{"/custom/mise/installs/go-lazypueue/0.1.1/bin/lazypueue", managerEnv{MiseDataDir: "/custom/mise"}, "mise"},
		{"/custom/tools/go-lazypueue/0.1.1/bin/lazypueue", managerEnv{MiseInstallsDir: "/custom/tools"}, "mise"},
		{"/custom/tools-other/go-lazypueue/0.1.1/bin/lazypueue", managerEnv{MiseInstallsDir: "/custom/tools"}, ""},
		{"/data/mise/installs/lazypueue/0.1.1/bin/lazypueue", managerEnv{XDGDataHome: "/data"}, "mise"},
		{"/tmp/mise/installs/lazypueue/0.1.1/bin/lazypueue", managerEnv{}, ""},
		{"/home/user/.local/share/mise/installs-other/lazypueue/0.1.1/bin/lazypueue", managerEnv{Home: "/home/user"}, ""},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			got, evidence := ownedByManager(test.path, test.env)
			if got != test.manager {
				t.Fatalf("manager=%q; want %q (%s)", got, test.manager, evidence)
			}
			if got != "" && evidence == "" {
				t.Fatal("ownership has no evidence")
			}
		})
	}
}

func TestCustomHomebrewReceipt(t *testing.T) {
	keg := filepath.Join(t.TempDir(), "custom", "Cellar", "lazypueue", "0.1.1")
	if err := os.MkdirAll(filepath.Join(keg, "bin"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(keg, "bin", "lazypueue")
	if got, _ := ownedByManager(path, managerEnv{}); got != "" {
		t.Fatalf("custom Cellar was assumed without evidence: %s", got)
	}
	if err := os.WriteFile(filepath.Join(keg, "INSTALL_RECEIPT.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, _ := ownedByManager(path, managerEnv{}); got != "homebrew" {
		t.Fatalf("custom receipt not recognized: %s", got)
	}
}

func TestInspectPathAndSymlink(t *testing.T) {
	// An actual tiny binary tests debug/buildinfo and symlink resolution without
	// downloading a release, invoking the product or relying on a tracked fixture.
	root := t.TempDir()
	module := filepath.Join(root, "module")
	if err := os.MkdirAll(module, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte("module "+ModulePath+"\n\ngo 1.25.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "main.go"), []byte("package main\nfunc main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "lazypueue")
	command := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	command.Dir = module
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}
	link := filepath.Join(root, "linked-lazypueue")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBIN", filepath.Join(root, "wrong-copy"))
	t.Setenv("PATH", filepath.Join(root, "other-copy"))
	t.Setenv("HOMEBREW_CELLAR", "")
	t.Setenv("MISE_DATA_DIR", "")
	t.Setenv("MISE_INSTALLS_DIR", "")
	wantResolved, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := InspectPath(link)
	if err != nil || got.Executable != link || got.ResolvedPath != wantResolved || !got.IdentityValid || got.BuildKind != "development" || got.GOOS != runtime.GOOS || got.GOARCH != runtime.GOARCH {
		t.Fatalf("InspectPath = %+v, %v", got, err)
	}
	after, err := os.ReadDir(root)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("inspection changed directory contents: %v", err)
	}
	current, err := Inspect()
	self, selfErr := os.Executable()
	if err != nil || selfErr != nil || current.Executable != self {
		t.Fatalf("Inspect followed PATH/GOBIN rather than current executable: %+v, %v, %v", current, err, selfErr)
	}
}

func TestInspectUnknownAndManaged(t *testing.T) {
	root := t.TempDir()
	cellar := filepath.Join(root, "Cellar")
	keg := filepath.Join(cellar, "lazypueue", "0.1.1", "bin")
	if err := os.MkdirAll(keg, 0700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(keg, "lazypueue")
	if err := os.WriteFile(unknown, []byte("not a Go executable\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOMEBREW_CELLAR", cellar)
	got, err := InspectPath(unknown)
	if err != nil || got.IdentityValid || got.Manager != "homebrew" || got.Method != "package-manager" || !strings.Contains(got.Reason, "brew upgrade") {
		t.Fatalf("managed ownership did not take precedence: %+v, %v", got, err)
	}
	if _, err := InspectPath(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing executable was accepted")
	}
	if _, err := InspectPath(root); err == nil {
		t.Fatal("directory accepted as executable")
	}
}
