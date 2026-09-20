// Package config owns lazypueue preferences and private UI state. Native Pueue
// configuration is referenced by connections and is never written here.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Connections       []core.Connection `toml:"connections" json:"connections"`
	DefaultConnection string            `toml:"default_connection,omitempty" json:"default_connection,omitempty"`
	TUI               TUIConfig         `toml:"tui" json:"tui"`
	Logs              core.LogSettings  `toml:"logs" json:"logs"`
	loaded            bool
	loadedPath        string
	original          []byte
	existed           bool
}
type TUIConfig struct {
	RefreshSeconds    int   `toml:"refresh_seconds" json:"refresh_seconds"`
	BackgroundSeconds int   `toml:"background_seconds" json:"background_seconds"`
	Mouse             *bool `toml:"mouse,omitempty" json:"mouse,omitempty"`
}

var ErrConflict = errors.New("configuration changed since it was loaded; reload it before saving")
var saveMu sync.Mutex
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func Default() Config {
	mouse := true
	return Config{Connections: []core.Connection{{ID: "local", Name: "Local", Kind: "local"}}, DefaultConnection: "all", TUI: TUIConfig{RefreshSeconds: 2, BackgroundSeconds: 5, Mouse: &mouse}, Logs: DefaultLogs()}
}

func DefaultLogs() core.LogSettings {
	return core.LogSettings{SingleMode: "auto", MultiMode: "poll", PollInterval: "10s", TailLines: 200}
}
func MergeLogs(parent, override core.LogSettings) core.LogSettings {
	if override.SingleMode != "" {
		parent.SingleMode = override.SingleMode
	}
	if override.MultiMode != "" {
		parent.MultiMode = override.MultiMode
	}
	if override.PollInterval != "" {
		parent.PollInterval = override.PollInterval
	}
	if override.TailLines != 0 {
		parent.TailLines = override.TailLines
	}
	return parent
}
func (cfg Config) EffectiveLogs(c core.Connection) core.LogSettings {
	return MergeLogs(MergeLogs(DefaultLogs(), cfg.Logs), c.Logs)
}
func ValidateLogSettings(s core.LogSettings) error {
	if s.SingleMode != "" && s.SingleMode != "auto" && s.SingleMode != "live" && s.SingleMode != "poll" && s.SingleMode != "manual" {
		return errors.New("single_mode must be auto, live, poll, or manual")
	}
	if s.MultiMode != "" && s.MultiMode != "live" && s.MultiMode != "poll" && s.MultiMode != "manual" {
		return errors.New("multi_mode must be live, poll, or manual")
	}
	if s.PollInterval != "" {
		d, err := time.ParseDuration(s.PollInterval)
		if err != nil || d < time.Second {
			return errors.New("poll_interval must be a duration of at least 1s")
		}
	}
	if s.TailLines < 0 || s.TailLines > 10000 {
		return errors.New("tail_lines must be 1–10000, or 0 to inherit")
	}
	return nil
}

func xdgPath(variable, fallback, filename string) (string, error) {
	base := os.Getenv(variable)
	if !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", errors.New("cannot determine an absolute home directory")
		}
		base = filepath.Join(home, fallback)
	}
	return filepath.Join(base, "lazypueue", filename), nil
}

// ResolvePath is independent of parsing so a malformed file can still be edited.
func ResolvePath(path string) (string, error) {
	if path == "" {
		path = os.Getenv("LAZYPUEUE_CONFIG")
	}
	if path == "" {
		return xdgPath("XDG_CONFIG_HOME", ".config", "config.toml")
	}
	return filepath.Abs(path)
}

func Load(path string) (Config, error) {
	explicit := path != "" || os.Getenv("LAZYPUEUE_CONFIG") != ""
	abs, err := ResolvePath(path)
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	data, err := os.ReadFile(abs)
	if err != nil && !(errors.Is(err, os.ErrNotExist) && !explicit) {
		return Config{}, fmt.Errorf("read config %s: %w", abs, err)
	}
	cfg.loaded, cfg.loadedPath, cfg.existed = true, abs, err == nil
	if err == nil {
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", abs, err)
		}
		cfg.original = append([]byte(nil), data...)
	}
	for i := range cfg.Connections {
		if cfg.Connections[i].Kind == "" {
			cfg.Connections[i].Kind = "local"
		}
	}
	if err := Validate(cfg); err != nil {
		return Config{}, fmt.Errorf("config %s: %w", abs, err)
	}
	return cfg, nil
}

func (cfg Config) Connection(id string) (core.Connection, error) {
	if id == "" {
		id = cfg.DefaultConnection
	}
	if id == "" || id == "all" {
		if len(cfg.Connections) > 0 {
			return cfg.Connections[0], nil
		}
	}
	for _, c := range cfg.Connections {
		if c.ID == id {
			return c, nil
		}
	}
	return core.Connection{}, fmt.Errorf("connection %q does not exist", id)
}

func Validate(cfg Config) error {
	if err := ValidateLogSettings(cfg.Logs); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	seen := map[string]bool{}
	for _, c := range cfg.Connections {
		if err := ValidateConnection(c); err != nil {
			return fmt.Errorf("connection %q: %w", c.ID, err)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate connection ID %q", c.ID)
		}
		seen[c.ID] = true
	}
	if cfg.DefaultConnection != "" && cfg.DefaultConnection != "all" && !seen[cfg.DefaultConnection] {
		return fmt.Errorf("default connection %q does not exist", cfg.DefaultConnection)
	}
	if cfg.TUI.RefreshSeconds < 1 || cfg.TUI.BackgroundSeconds < 1 {
		return errors.New("refresh_seconds and background_seconds must be positive")
	}
	return nil
}

func ValidateConnection(c core.Connection) error {
	if err := ValidateLogSettings(c.Logs); err != nil {
		return fmt.Errorf("logs: %w", err)
	}
	if !validID.MatchString(c.ID) || c.ID == "all" {
		return errors.New("ID must contain letters, digits, dots, underscores or hyphens and cannot be 'all'")
	}
	for _, v := range []string{c.Name, c.Binary, c.ConfigPath, c.Profile, c.SSHHost, c.SSHBinary, c.SSHConfig, c.SSHProfile, c.Host, c.CertPath, c.SecretPath, c.SocketPath} {
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return errors.New("connection fields must not contain control characters")
		}
	}
	if c.Port < 0 || c.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if c.SSHHost != "" && (strings.HasPrefix(c.SSHHost, "-") || strings.ContainsAny(c.SSHHost, " \t\r\n")) {
		return errors.New("SSH host must be an alias or user@host without spaces or leading hyphens")
	}
	switch c.Kind {
	case "local":
		if c.SSHHost != "" || c.Host != "" || c.Port != 0 {
			return errors.New("local connections cannot specify a remote host")
		}
	case "ssh":
		if c.SSHHost == "" {
			return errors.New("SSH host alias is required")
		}
		if c.Host != "" || c.Port != 0 || c.CertPath != "" || c.SecretPath != "" {
			return errors.New("SSH connections use remote Pueue config/profile, not native endpoint fields")
		}
	case "native":
		if c.SocketPath != "" && (c.Host != "" || c.Port != 0) {
			return errors.New("choose a native Unix socket or a TCP host/port, not both")
		}
		if c.ConfigPath != "" || c.Profile != "" {
			if c.Host != "" || c.Port != 0 || c.CertPath != "" || c.SecretPath != "" || c.SocketPath != "" {
				return errors.New("choose either a native config/profile reference or native endpoint fields")
			}
		} else if c.SocketPath != "" {
			if c.SecretPath == "" {
				return errors.New("native Unix socket connections require a secret file path")
			}
		} else if c.Host == "" || c.Port == 0 || c.CertPath == "" || c.SecretPath == "" {
			return errors.New("native connection needs a config/profile reference, Unix socket and secret file, or host, port, certificate and secret file paths")
		}
		if strings.ContainsAny(c.Host, " /\t\r\n") || strings.HasPrefix(c.Host, "-") {
			return errors.New("native host must be a hostname or IP address")
		}
	default:
		return errors.New("kind must be local, ssh, or native")
	}
	return nil
}

// Save preserves unknown fields/comments and rejects an intervening writer.
// Reload after a successful save before applying a subsequent edit.
func Save(path string, cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	abs, err := ResolvePath(path)
	if err != nil {
		return err
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	current, err := os.ReadFile(abs)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if cfg.loaded {
		if cfg.loadedPath != abs || cfg.existed != exists || !bytes.Equal(cfg.original, current) {
			return ErrConflict
		}
	} else if exists {
		return errors.New("load the existing configuration before saving it")
	}
	data, err := preserve(current, cfg)
	if err != nil {
		return err
	}
	var check Config
	if err := toml.Unmarshal(data, &check); err != nil {
		return fmt.Errorf("cannot preserve this TOML layout safely: %w", err)
	}
	if err := Validate(check); err != nil {
		return err
	}
	return writeAtomic(abs, data, func() error {
		latest, e := os.ReadFile(abs)
		if (e == nil) != exists || (e != nil && !errors.Is(e, os.ErrNotExist)) || !bytes.Equal(latest, current) {
			return ErrConflict
		}
		return nil
	})
}

func writeAtomic(path string, data []byte, check func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("another writer may be editing %s (lock exists): %w", path, err)
	}
	lock.Close()
	defer os.Remove(path + ".lock")
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".lazypueue-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if check != nil {
		if err := check(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if dir, e := os.Open(filepath.Dir(path)); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

type LastUsed struct {
	Directory string `json:"directory"`
	Group     string `json:"group"`
}
type ViewState struct {
	Selected string `json:"selected"`
	Index    int    `json:"index"`
	Offset   int    `json:"offset"`
	Query    string `json:"query"`
	State    string `json:"state"`
}
type State struct {
	Scope    string               `json:"scope"`
	LastUsed map[string]LastUsed  `json:"last_used"`
	Views    map[string]ViewState `json:"views"`
	Watches  []WatchState         `json:"watches,omitempty"`
	Mouse    *bool                `json:"mouse,omitempty"`
}
type WatchState struct {
	Connection string    `json:"connection"`
	TaskID     int       `json:"task_id"`
	CreatedAt  time.Time `json:"created_at"`
	Mode       string    `json:"mode"`
	Interval   string    `json:"interval"`
	Lines      int       `json:"lines"`
	Paused     bool      `json:"paused"`
}

func StatePath() (string, error) { return xdgPath("XDG_STATE_HOME", ".local/state", "state.json") }
func LoadState() (State, error) {
	s := State{Scope: "all", LastUsed: map[string]LastUsed{}, Views: map[string]ViewState{}}
	path, err := StatePath()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("parse UI state: %w", err)
	}
	if s.Scope == "" {
		s.Scope = "all"
	}
	if s.LastUsed == nil {
		s.LastUsed = map[string]LastUsed{}
	}
	if s.Views == nil {
		s.Views = map[string]ViewState{}
	}
	return s, nil
}
func SaveState(s State) error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	return writeAtomic(path, append(data, '\n'), nil)
}
