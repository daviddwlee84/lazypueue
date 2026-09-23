package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func TestXDGDefaultsDoNotCreateDirectories(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("LAZYPUEUE_CONFIG", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultConnection != "all" || cfg.Connections[0].ID != "local" || cfg.TUI.BackgroundSeconds != 5 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	path, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created directory: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("explicit missing file did not fail")
	}
	state, err := LoadState()
	if err != nil || state.Scope != "all" || state.LastUsed == nil {
		t.Fatalf("state defaults: %#v %v", state, err)
	}
	statePath, _ := StatePath()
	if _, err := os.Stat(filepath.Dir(statePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state read created directory")
	}
}
func TestRelativeXDGIsIgnored(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	t.Setenv("LAZYPUEUE_CONFIG", "")
	path, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "lazypueue", "config.toml")
	if path != want {
		t.Fatalf("got %q want %q", path, want)
	}
}
func TestSavePreservesCommentsUnknownValuesAndDetectsConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `# user's settings
default_connection = "local" # keep root note
custom = "keep me"

[tui]
refresh_seconds = 3 # slow link
background_seconds = 7
future_color = "pink"

[[connections]]
id = "local"
kind = "local"
name = "Laptop" # name note
future_option = ["hello", "世界"]

[[connections]]
id = "server"
kind = "ssh"
ssh_host = "lab"
name = "Server"

[unrelated]
enabled = true
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	stale := cfg
	cfg.Connections[0].Name = "Work laptop"
	cfg.TUI.RefreshSeconds = 2
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"# user's settings", "# keep root note", "# slow link", "# name note", `custom = "keep me"`, `future_color = "pink"`, `future_option = ["hello", "世界"]`, `[unrelated]`, `enabled = true`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("lost %q:\n%s", want, data)
		}
	}
	if err := Save(path, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	updated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Connections[0].Name != "Work laptop" || updated.TUI.RefreshSeconds != 2 {
		t.Fatalf("save failed: %#v", updated)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %v", info.Mode())
	}
}
func TestSaveNewAndRemoveReorderConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	cfg := Default()
	cfg.Connections = append(cfg.Connections, core.Connection{ID: "remote", Kind: "ssh", SSHHost: "box"})
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Connections = []core.Connection{cfg.Connections[1]}
	cfg.DefaultConnection = "remote"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || len(cfg.Connections) != 1 || cfg.Connections[0].ID != "remote" {
		t.Fatalf("remove failed: %#v %v", cfg, err)
	}
}
func TestSaveRefusesUnsupportedLayoutAndExternalChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `connections = [{id="local",kind="local"}]` + "\n"
	os.WriteFile(path, []byte(raw), 0600)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(path, cfg); err == nil {
		t.Fatal("rewrote inline array")
	}
	data, _ := os.ReadFile(path)
	if string(data) != raw {
		t.Fatal("file changed after rejected save")
	}
	os.WriteFile(path, []byte(raw+"# external writer\n"), 0600)
	if err := Save(path, cfg); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict got %v", err)
	}
}
func TestStateStoresOnlyNavigationAndSuccessfulAddDefaults(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := State{Scope: "remote", LastUsed: map[string]LastUsed{"remote": {Directory: "/workspace/專案", Group: "training"}}, Views: map[string]ViewState{"remote//0": {Selected: "7@created", Index: 2, Offset: 1, Query: "train", State: "queued"}}}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	read, err := LoadState()
	if err != nil || read.LastUsed["remote"] != s.LastUsed["remote"] {
		t.Fatalf("state mismatch %#v %v", read, err)
	}
	if read.Views["remote//0"] != s.Views["remote//0"] {
		t.Fatalf("view state mismatch %#v", read.Views)
	}
}
func TestConnectionValidation(t *testing.T) {
	valid := []core.Connection{{ID: "local", Kind: "local"}, {ID: "lab", Kind: "ssh", SSHHost: "user@lab"}, {ID: "tls", Kind: "native", ConfigPath: "/private/pueue.yml", Profile: "server"}, {ID: "default-config-profile", Kind: "native", Profile: "server"}, {ID: "tls", Kind: "native", Host: "example.com", Port: 6924, CertPath: "/cert", SecretPath: "/secret", SSHHost: "lab"}}
	for _, c := range valid {
		if err := ValidateConnection(c); err != nil {
			t.Errorf("valid %#v: %v", c, err)
		}
	}
	if err := ValidateConnection(core.Connection{ID: "socket", Kind: "native", SocketPath: "/tmp/pueue.sock", SecretPath: "/tmp/secret"}); err != nil {
		t.Fatalf("native Unix socket: %v", err)
	}
	invalid := []core.Connection{{ID: "all", Kind: "local"}, {ID: "lab", Kind: "ssh", SSHHost: "-oProxyCommand=bad"}, {ID: "tls", Kind: "native", Host: "host", Port: 1}, {ID: "tls", Kind: "native", ConfigPath: "/config", Host: "host"}, {ID: "local", Kind: "local", SSHHost: "remote"}}
	for _, c := range invalid {
		if err := ValidateConnection(c); err == nil {
			t.Errorf("accepted invalid %#v", c)
		}
	}
	if err := ValidateConnection(core.Connection{ID: "socket", Kind: "native", SocketPath: "/tmp/pueue.sock", SecretPath: "/tmp/secret", Host: "remote"}); err == nil {
		t.Fatal("ambiguous Unix/TCP endpoint accepted")
	}
}
