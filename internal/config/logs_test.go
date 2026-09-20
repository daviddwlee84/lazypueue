package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

func TestLogDefaultsInheritanceAndValidation(t *testing.T) {
	cfg := Default()
	if cfg.TUI.Mouse == nil || !*cfg.TUI.Mouse || cfg.Logs != DefaultLogs() {
		t.Fatal("defaults missing")
	}
	cfg.Logs.PollInterval = "20s"
	c := core.Connection{Logs: core.LogSettings{MultiMode: "manual", TailLines: 300}}
	s := cfg.EffectiveLogs(c)
	if s.SingleMode != "auto" || s.MultiMode != "manual" || s.PollInterval != "20s" || s.TailLines != 300 {
		t.Fatalf("bad inheritance %#v", s)
	}
	for _, bad := range []core.LogSettings{{MultiMode: "auto"}, {SingleMode: "stream"}, {PollInterval: "0s"}, {PollInterval: "500ms"}, {PollInterval: "garbage"}, {TailLines: 10001}, {TailLines: -1}} {
		if ValidateLogSettings(bad) == nil {
			t.Errorf("accepted %#v", bad)
		}
	}
}
func TestPreserveGlobalAndNestedLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	raw := `default_connection="all"
[logs]
single_mode="auto" # keep single
multi_mode="poll"
poll_interval="10s"
tail_lines=200
future="keep global"
[tui]
refresh_seconds=2
background_seconds=5
mouse=false # keep mouse
[[connections]]
id="one"
kind="local"
[connections.logs]
multi_mode="manual" # keep override
poll_interval="30s"
future="keep nested"
[[connections]]
id="two"
kind="local"
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Logs.TailLines = 500
	cfg.Connections[0].Logs.PollInterval = "1m"
	cfg.Connections[1].Logs = core.LogSettings{SingleMode: "poll", PollInterval: "2s"}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	for _, want := range []string{"# keep single", "# keep mouse", "# keep override", `future="keep global"`, `future="keep nested"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("lost %q:\n%s", want, out)
		}
	}
	read, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if read.Logs.TailLines != 500 || read.Connections[0].Logs.PollInterval != "1m" || read.Connections[1].Logs.PollInterval != "2s" || *read.TUI.Mouse {
		t.Fatalf("save drift %#v", read)
	}
}
func TestWatchAndMouseStateRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	mouse := false
	stamp := time.Now().Round(0)
	state := State{Scope: "all", Mouse: &mouse, Watches: []WatchState{{Connection: "lab", TaskID: 7, CreatedAt: stamp, Mode: "poll", Interval: "10s", Lines: 500, Paused: true}}}
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	read, err := LoadState()
	if err != nil || len(read.Watches) != 1 || !read.Watches[0].CreatedAt.Equal(stamp) || read.Mouse == nil || *read.Mouse {
		t.Fatalf("roundtrip %#v %v", read, err)
	}
}
