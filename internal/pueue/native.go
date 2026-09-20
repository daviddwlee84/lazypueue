package pueue

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"go.yaml.in/yaml/v3"
)

func expandLocal(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func defaultConfigPath() string {
	if path := os.Getenv("PUEUE_CONFIG_PATH"); path != "" {
		return path
	}
	if dir, err := os.UserConfigDir(); err == nil {
		path := filepath.Join(dir, "pueue", "pueue.yml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if _, err := os.Stat("pueue.yml"); err == nil {
		return "pueue.yml"
	}
	return ""
}

func object(value any) map[string]any {
	if v, ok := value.(map[string]any); ok && v != nil {
		return v
	}
	return map[string]any{}
}

// effectiveConfig produces a private, temporary client-only configuration.
// Profiles replace all three sections upstream; flatten before applying our
// noninteractive/client-log overrides. Never change the user's source file.
func (c *Client) effectiveConfig(conn core.Connection) (string, error) {
	source := conn.ConfigPath
	if source == "" && !(conn.Kind == "native" && conn.Profile == "" && (conn.Host != "" || conn.SocketPath != "")) {
		source = defaultConfigPath()
	}
	settings := map[string]any{}
	if source != "" {
		data, err := os.ReadFile(expandLocal(source))
		if err != nil {
			return "", fmt.Errorf("read Pueue config: %w", err)
		}
		if err = yaml.Unmarshal(data, &settings); err != nil {
			return "", fmt.Errorf("invalid Pueue config: %w", err)
		}
	}
	if conn.Profile != "" {
		profiles := object(settings["profiles"])
		selected, ok := profiles[conn.Profile]
		if !ok {
			return "", fmt.Errorf("Pueue profile %q does not exist", conn.Profile)
		}
		if _, ok := selected.(map[string]any); !ok {
			return "", fmt.Errorf("Pueue profile %q must be a mapping", conn.Profile)
		}
		settings = object(selected)
	}
	for _, section := range []string{"client", "shared"} {
		if value, exists := settings[section]; exists && value != nil {
			if _, ok := value.(map[string]any); !ok {
				return "", fmt.Errorf("Pueue %s settings must be a mapping", section)
			}
		}
	}
	client := object(settings["client"])
	shared := object(settings["shared"])
	if conn.Kind == "native" {
		client["read_local_logs"] = false
		if conn.SocketPath != "" {
			shared["use_unix_socket"] = true
			shared["unix_socket_path"] = expandLocal(conn.SocketPath)
		} else if conn.Host != "" {
			shared["use_unix_socket"] = false
			shared["host"] = conn.Host
			port := conn.Port
			if port == 0 {
				port = 6924
			}
			shared["port"] = fmt.Sprint(port)
		}
		if conn.CertPath != "" {
			shared["daemon_cert"] = expandLocal(conn.CertPath)
		}
		if conn.SecretPath != "" {
			shared["shared_secret_path"] = expandLocal(conn.SecretPath)
		}
		if source == "" && conn.Host == "" && conn.SocketPath == "" {
			return "", fmt.Errorf("native connection requires a Pueue config/profile, host, or socket")
		}
	}
	if conn.Kind != "native" && conn.SocketPath != "" {
		shared["use_unix_socket"] = true
		shared["unix_socket_path"] = expandLocal(conn.SocketPath)
	}
	// All app mutations have already been reviewed by the caller.
	client["show_confirmation_questions"] = false
	client["edit_mode"] = "toml"
	data, err := yaml.Marshal(map[string]any{"client": client, "shared": shared})
	if err != nil {
		return "", fmt.Errorf("prepare client config: %w", err)
	}
	dir, err := c.tempDir()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	path := filepath.Join(dir, fmt.Sprintf("client-%x.yml", digest[:12]))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", fmt.Errorf("Pueue client is closed")
	}
	if _, err = os.Stat(path); os.IsNotExist(err) {
		err = os.WriteFile(path, data, 0o600)
	}
	if err != nil {
		return "", fmt.Errorf("write temporary client config: %w", err)
	}
	return path, nil
}

func (c *Client) tempDir() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", fmt.Errorf("Pueue client is closed")
	}
	if c.dir != "" {
		return c.dir, nil
	}
	base := ""
	// OpenSSH control sockets have a short path limit, including on macOS.
	if runtime.GOOS != "windows" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "lazypq-")
	if err != nil {
		return "", err
	}
	c.dir = dir
	return dir, nil
}
