package maintenance

import (
	"context"
	"errors"
	"path"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const environmentScript = `printf '%s\n%s\n%s\n%s\n' "$HOME" "${XDG_CONFIG_HOME-}" "${XDG_DATA_HOME-}" "${XDG_RUNTIME_DIR-}"; uname -s`

// Match the selected queue's PID file to the supervisor's live process. A
// shared binary/config-looking argv alone cannot identify a daemon instance.
func (s *Service) bindDaemon(ctx context.Context, p *Plan) error {
	if p.ServicePID <= 0 {
		return errors.New("supervisor did not identify a running daemon PID")
	}
	data, err := s.read(ctx, p.Target, "sh", "-c", environmentScript)
	if err != nil {
		return err
	}
	env := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(env) != 5 || !path.IsAbs(env[0]) {
		return errors.New("cannot resolve daemon configuration directories")
	}
	home, configDir, dataDir, runtimeDir, platform := env[0], env[1], env[2], env[3], env[4]
	if platform == "Darwin" {
		configDir = path.Join(home, "Library/Application Support")
		dataDir = configDir
		runtimeDir = ""
	} else if platform == "Linux" {
		if !path.IsAbs(configDir) {
			configDir = path.Join(home, ".config")
		}
		if !path.IsAbs(dataDir) {
			dataDir = path.Join(home, ".local/share")
		}
		if runtimeDir != "" && !path.IsAbs(runtimeDir) {
			return errors.New("relative XDG_RUNTIME_DIR cannot safely identify the daemon")
		}
	} else {
		return errors.New("service maintenance supports macOS and Linux only")
	}
	configPath := p.queue.ConfigPath
	if configPath == "" {
		configPath = path.Join(configDir, "pueue/pueue.yml")
	}
	if !path.IsAbs(configPath) {
		return errors.New("maintenance requires an absolute explicit Pueue config path")
	}
	data, err = s.read(ctx, p.Target, "cat", configPath)
	if err != nil {
		return errors.New("cannot read the selected Pueue configuration to identify its daemon")
	}
	var cfg map[string]any
	if yaml.Unmarshal(data, &cfg) != nil {
		return errors.New("cannot parse the selected Pueue configuration")
	}
	if p.queue.Profile != "" {
		profiles, _ := cfg["profiles"].(map[string]any)
		selected, ok := profiles[p.queue.Profile].(map[string]any)
		if !ok {
			return errors.New("selected Pueue profile does not exist")
		}
		cfg = selected
	}
	shared, _ := cfg["shared"].(map[string]any)
	if unix, ok := shared["use_unix_socket"].(bool); ok && !unix {
		host, _ := shared["host"].(string)
		if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
			return errors.New("selected Pueue config targets a different TCP host; its local service cannot be maintained")
		}
	}
	expand := func(value string) string {
		if value == "~" {
			return home
		}
		if strings.HasPrefix(value, "~/") {
			return path.Join(home, value[2:])
		}
		return value
	}
	get := func(key string) string { value, _ := shared[key].(string); return expand(value) }
	pueueDir := get("pueue_directory")
	if pueueDir == "" {
		pueueDir = path.Join(dataDir, "pueue")
	}
	if value := get("runtime_directory"); value != "" {
		runtimeDir = value
	}
	if runtimeDir == "" {
		runtimeDir = pueueDir
	}
	pidPath := get("pid_path")
	if pidPath == "" {
		pidPath = path.Join(runtimeDir, "pueue.pid")
	}
	if !path.IsAbs(pidPath) {
		return errors.New("relative daemon PID paths cannot be maintained safely")
	}
	data, err = s.read(ctx, p.Target, "cat", pidPath)
	if err != nil {
		return errors.New("selected daemon PID file is unavailable; service identity is unverified")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != p.ServicePID {
		return errors.New("selected queue's daemon PID differs from the service; refusing to stop another queue")
	}
	p.PIDPath = pidPath
	return nil
}
