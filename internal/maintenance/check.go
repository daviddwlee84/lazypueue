package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/daviddwlee84/lazypueue/internal/core"
	"github.com/daviddwlee84/lazypueue/internal/selfupdate"
	"github.com/pelletier/go-toml/v2"
)

var versionRE = regexp.MustCompile(`(?m)^pueued?\s+(\d+\.\d+\.\d+)\s*$`)

func version(data []byte) (string, error) {
	m := versionRE.FindSubmatch(data)
	if m == nil {
		return "", errors.New("cannot determine installed Pueue version")
	}
	return string(m[1]), nil
}
func supported(v string) bool { return strings.HasPrefix(v, "4.") && selfupdate.StableVersion("v"+v) }

func maintenanceRoute(conn core.Connection) (core.Connection, Target, error) {
	c := conn
	if conn.Kind == "native" {
		if conn.SSHHost == "" {
			return c, Target{}, errors.New("native-only connections cannot administer packages or services; configure an SSH companion")
		}
		c = core.Connection{ID: conn.ID, Name: conn.Name, Kind: "ssh", SSHHost: conn.SSHHost, Binary: conn.SSHBinary, ConfigPath: conn.SSHConfig, Profile: conn.SSHProfile}
	}
	if c.Kind == "ssh" {
		if c.SSHBinary != "" {
			c.Binary = c.SSHBinary
		}
		if c.SSHConfig != "" {
			c.ConfigPath = c.SSHConfig
		}
		if c.SSHProfile != "" {
			c.Profile = c.SSHProfile
		}
		if c.SSHHost == "" || strings.HasPrefix(c.SSHHost, "-") || strings.ContainsAny(c.SSHHost, " \t\r\n\x00") {
			return c, Target{}, errors.New("an SSH alias is required")
		}
		return c, Target{SSHHost: c.SSHHost}, nil
	}
	if c.Kind != "local" && c.Kind != "" {
		return c, Target{}, errors.New("unsupported maintenance transport")
	}
	return c, Target{}, nil
}

func (s *Service) Check(ctx context.Context, conn core.Connection) (Plan, error) {
	p := Plan{Connection: conn, CheckedAt: time.Now(), ActiveIDs: []int{}, Groups: []core.Group{}, Owner: "unknown", checked: true}
	var err error
	p.queue, p.Target, err = maintenanceRoute(conn)
	if err != nil {
		p.Reason = err.Error()
		return p, nil
	}
	if s.Backend == nil || s.Runner == nil {
		return p, errors.New("maintenance dependencies unavailable")
	}
	p.Binary = p.queue.Binary
	if p.Binary == "" {
		p.Binary = "pueue"
	}
	p.Resolved, err = s.resolve(ctx, p.Target, p.Binary)
	if err != nil {
		p.Reason = "Cannot find the selected Pueue executable; install or repair it manually."
		return p, nil
	}
	p.DaemonBinary = path.Join(path.Dir(p.Resolved), "pueued")
	data, err := s.read(ctx, p.Target, p.Resolved, "--version")
	if err != nil {
		return p, err
	}
	p.CurrentVersion, err = version(data)
	if err != nil {
		return p, err
	}
	if !supported(p.CurrentVersion) {
		p.Reason = "Only Pueue 4.x maintenance is supported."
		return p, nil
	}
	if d, e := s.read(ctx, p.Target, p.DaemonBinary, "--version"); e != nil {
		p.Reason = "The matching pueued binary is missing; package ownership is incomplete."
		return p, nil
	} else if v, e := version(d); e != nil || v != p.CurrentVersion {
		p.Reason = "Installed pueue and pueued versions differ; repair the installation manually."
		return p, nil
	}
	uid, err := s.read(ctx, p.Target, "id", "-u")
	if err != nil {
		return p, err
	}
	p.uid = strings.TrimSpace(string(uid))
	if _, e := strconv.Atoi(p.uid); e != nil {
		return p, errors.New("cannot identify the maintenance user")
	}
	if ok, e := s.detectBrew(ctx, &p); e != nil {
		return p, e
	} else if !ok {
		if e := s.detectCargo(ctx, &p); e != nil {
			return p, e
		}
	}
	if p.Owner == "unknown" {
		p.Reason = "Package ownership is unknown; use the package manager that owns the resolved executable. No files will be overwritten."
		return p, nil
	}
	if p.ServiceKind == "" {
		if err := s.detectSystemd(ctx, &p); err != nil {
			return p, err
		}
	}
	if p.ServiceKind == "" {
		p.Reason = "No matching Homebrew or systemd user service was verified. Manage this daemon with its existing supervisor; no automatic service action is available."
	} else if e := s.bindDaemon(ctx, &p); e != nil {
		p.Reason = e.Error()
	}
	env, e := s.read(ctx, p.Target, "sh", "-c", `printf '%s' "${PUEUE_CONFIG_PATH-}"`)
	if e != nil {
		return p, e
	}
	if p.queue.ConfigPath == "" && len(env) > 0 {
		p.Reason = "PUEUE_CONFIG_PATH selects a custom daemon; configure its path explicitly before reviewing service maintenance."
	}
	if p.queue.SocketPath != "" {
		p.Reason = "An explicit socket override cannot be matched to a service automatically; use its supervisor manually."
	}
	snapshot, e := s.Backend.Snapshot(ctx, p.queue)
	if e != nil {
		p.Reason = "Cannot verify the queue is idle: " + e.Error()
	} else {
		p.Groups = append([]core.Group(nil), snapshot.Groups...)
		if len(p.Groups) == 0 {
			p.Reason = "The daemon returned no groups; queue scheduling cannot be verified."
		}
		for _, g := range p.Groups {
			if !strings.EqualFold(g.Status, "running") && !strings.EqualFold(g.Status, "paused") {
				p.Reason = "Unknown group state prevents safe maintenance: " + g.Name
			}
		}
		p.ActiveIDs = activeTasks(snapshot)
		if len(p.ActiveIDs) > 0 {
			p.Reason = fmt.Sprintf("Tasks %v are running, paused, locked, or unknown. Finish them before maintenance; nothing was changed.", p.ActiveIDs)
		}
	}
	if p.CandidateVersion == "" {
		candidate := s.Candidate
		if candidate == nil {
			candidate = latestPueue
		}
		p.CandidateVersion, err = candidate(ctx)
		if err != nil {
			return p, err
		}
	}
	if !supported(p.CandidateVersion) {
		p.Reason = "The available Pueue version is outside supported 4.x; upgrade manually after checking compatibility."
	} else {
		cmp, _ := selfupdate.CompareVersions("v"+p.CandidateVersion, "v"+p.CurrentVersion)
		p.UpdateAvailable = cmp > 0
		p.ready = p.Reason == "" && p.ServiceKind != ""
		if !p.UpdateAvailable && p.Reason == "" {
			p.Reason = "The package is already current; no upgrade is needed."
		}
	}
	p.CanApply = p.Reason == "" && p.UpdateAvailable && p.ServiceKind != ""
	p.identity = identity(p)
	host := "this machine"
	if p.Target.SSHHost != "" {
		host = "SSH " + p.Target.SSHHost
	}
	p.Review = []string{"Target: " + host, "Pueue: " + p.Resolved, "Package owner: " + p.Owner, "Version: " + p.CurrentVersion + " → " + p.CandidateVersion, "Service: " + p.ServiceKind + " " + p.ServiceName, "Require an idle queue, pause scheduling without suspending jobs, then recheck before stopping the daemon.", "Update the package, start the same service, verify the queue, and restore only previously running groups.", "External clients must not force-start work during maintenance. No queue reset or cleanup is performed."}
	commands := [][]string{s.upgradeArgs(p)}
	if p.ServiceKind != "" {
		commands = [][]string{s.pauseArgs(p), s.stopArgs(p), s.upgradeArgs(p), s.startArgs(p)}
	}
	for _, args := range commands {
		p.Review = append(p.Review, join(args))
	}
	if p.Reason != "" {
		p.Review = append(p.Review, p.Reason)
	}
	return p, nil
}

func activeTasks(s core.Snapshot) []int {
	ids := []int{}
	for _, t := range s.Tasks {
		state := t.State()
		if t.Locked || state == "running" || state == "paused" || state == "locked" || (state != "queued" && state != "stashed" && !t.Terminal()) {
			ids = append(ids, t.ID)
		}
	}
	return ids
}
func identity(p Plan) string {
	groups := append([]core.Group(nil), p.Groups...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	groupData, _ := json.Marshal(groups)
	sum := sha256.Sum256([]byte(strings.Join([]string{p.Target.SSHHost, p.Binary, p.Resolved, p.DaemonBinary, p.CurrentVersion, p.CandidateVersion, p.Owner, p.ManagerBinary, p.InstallRoot, p.ServiceKind, p.ServiceName, strconv.Itoa(p.ServicePID), p.PIDPath, p.queue.ConfigPath, p.queue.Profile, string(groupData)}, "\x00")))
	return hex.EncodeToString(sum[:])
}

type brewFormula struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Versions struct {
		Stable string `json:"stable"`
	} `json:"versions"`
	Installed []struct {
		Version string `json:"version"`
	} `json:"installed"`
}

func (s *Service) detectBrew(ctx context.Context, p *Plan) (bool, error) {
	if !strings.Contains(p.Resolved, "/Cellar/") {
		return false, nil
	}
	parts := strings.SplitN(p.Resolved, "/Cellar/", 2)
	rest := strings.Split(parts[1], "/")
	if len(rest) != 4 || rest[0] != "pueue" || rest[2] != "bin" || rest[3] != "pueue" {
		return false, nil
	}
	if receipt, err := s.read(ctx, p.Target, "cat", path.Join(parts[0], "Cellar", rest[0], rest[1], "INSTALL_RECEIPT.json")); err != nil || !json.Valid(receipt) {
		return false, nil
	}
	brew, err := s.resolve(ctx, p.Target, path.Join(parts[0], "bin", "brew"))
	if err != nil {
		return false, nil
	}
	data, err := s.read(ctx, p.Target, brew, "info", "--json=v2", "pueue")
	if err != nil {
		return false, err
	}
	var info struct {
		Formulae []brewFormula `json:"formulae"`
	}
	if json.Unmarshal(data, &info) != nil {
		return false, errors.New("invalid Homebrew package metadata")
	}
	cellar, e := s.read(ctx, p.Target, brew, "--cellar", "pueue")
	if e != nil || strings.TrimSpace(string(cellar)) != path.Join(parts[0], "Cellar", "pueue") {
		return false, nil
	}
	for _, f := range info.Formulae {
		if f.Name != "pueue" {
			continue
		}
		for _, installed := range f.Installed {
			if installed.Version == p.CurrentVersion && installed.Version == rest[1] {
				p.Owner = "homebrew"
				p.ManagerBinary = brew
				p.InstallRoot = f.FullName
				if p.InstallRoot == "" {
					p.InstallRoot = "pueue"
				}
				p.CandidateVersion = f.Versions.Stable
			}
		}
	}
	if p.Owner != "homebrew" {
		return false, nil
	}
	data, err = s.read(ctx, p.Target, brew, "services", "info", p.InstallRoot, "--json")
	if err != nil {
		return true, nil
	}
	// Homebrew uses snake_case for these two fields.
	var raw []struct {
		Name        string `json:"name"`
		ServiceName string `json:"service_name"`
		Command     string `json:"command"`
		User        string `json:"user"`
		Running     bool   `json:"running"`
		Registered  bool   `json:"registered"`
		PID         int    `json:"pid"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return true, nil
	}
	username, _ := s.read(ctx, p.Target, "id", "-un")
	for _, service := range raw {
		if service.Name == "pueue" && service.Registered && service.User == strings.TrimSpace(string(username)) && service.Running && s.matchesService(ctx, *p, service.Command) {
			p.ServiceKind = "homebrew"
			p.ServiceName = p.InstallRoot
			p.ServicePID = service.PID
			p.WasRunning = true
		}
	}
	return true, nil
}
func (s *Service) detectCargo(ctx context.Context, p *Plan) error {
	if path.Base(path.Dir(p.Resolved)) != "bin" {
		return nil
	}
	root := path.Dir(path.Dir(p.Resolved))
	data, err := s.read(ctx, p.Target, "cat", path.Join(root, ".crates.toml"))
	if err != nil {
		return nil
	}
	var receipt struct {
		V1 map[string][]string `toml:"v1"`
	}
	if toml.Unmarshal(data, &receipt) != nil {
		return nil
	}
	for key, bins := range receipt.V1 {
		parts := strings.Fields(key)
		if len(parts) < 3 || parts[0] != "pueue" || parts[1] != p.CurrentVersion || (!strings.Contains(key, "registry+https://github.com/rust-lang/crates.io-index") && !strings.Contains(key, "registry+https://index.crates.io/")) {
			continue
		}
		hasClient, hasDaemon := false, false
		for _, b := range bins {
			hasClient = hasClient || b == "pueue"
			hasDaemon = hasDaemon || b == "pueued"
		}
		if !hasClient || !hasDaemon {
			continue
		}
		cargo, e := s.commandPath(ctx, p.Target, "cargo")
		if e != nil {
			p.Reason = "Cargo owns this installation but cargo is unavailable."
			return nil
		}
		p.Owner = "cargo"
		p.InstallRoot = root
		p.ManagerBinary = cargo
		return nil
	}
	return nil
}
func (s *Service) detectSystemd(ctx context.Context, p *Plan) error {
	data, err := s.read(ctx, p.Target, "systemctl", "--user", "show", "pueued.service", "--property=LoadState,ActiveState,MainPID,ExecStart", "--no-pager")
	if err != nil {
		return nil
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			fields[key] = value
		}
	}
	if fields["LoadState"] != "loaded" || fields["ActiveState"] != "active" {
		return nil
	}
	command := fields["ExecStart"]
	_, tail, ok := strings.Cut(command, "argv[]=")
	if !ok {
		return nil
	}
	command, _, _ = strings.Cut(tail, " ;")
	if s.matchesService(ctx, *p, command) {
		p.ServiceKind = "systemd-user"
		p.ServiceName = "pueued.service"
		p.ServicePID, _ = strconv.Atoi(fields["MainPID"])
		p.WasRunning = true
	}
	return nil
}

func (s *Service) matchesService(ctx context.Context, p Plan, command string) bool {
	args, err := splitCommand(command)
	if err != nil || len(args) == 0 {
		return false
	}
	binary, err := s.resolve(ctx, p.Target, args[0])
	if err != nil || binary != p.DaemonBinary {
		return false
	}
	config, profile := "", ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-v", "-vv", "-vvv", "--verbose":
		case "-c", "--config":
			i++
			if i >= len(args) {
				return false
			}
			config = args[i]
		case "-p", "--profile":
			i++
			if i >= len(args) {
				return false
			}
			profile = args[i]
		default:
			if strings.HasPrefix(args[i], "--config=") {
				config = strings.TrimPrefix(args[i], "--config=")
			} else if strings.HasPrefix(args[i], "--profile=") {
				profile = strings.TrimPrefix(args[i], "--profile=")
			} else {
				return false
			}
		}
	}
	return config == p.queue.ConfigPath && profile == p.queue.Profile
}
func splitCommand(text string) ([]string, error) {
	var args []string
	var b strings.Builder
	var q rune
	escape, started := false, false
	for _, r := range text {
		if escape {
			b.WriteRune(r)
			escape = false
			started = true
			continue
		}
		if r == '\\' && q != '\'' {
			escape = true
			continue
		}
		if q != 0 {
			if r == q {
				q = 0
			} else {
				b.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			q = r
			started = true
		} else if r == ' ' || r == '\t' {
			if started {
				args = append(args, b.String())
				b.Reset()
				started = false
			}
		} else {
			b.WriteRune(r)
			started = true
		}
	}
	if q != 0 || escape {
		return nil, errors.New("invalid service command")
	}
	if started {
		args = append(args, b.String())
	}
	return args, nil
}

func latestPueue(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/Nukesor/pueue/releases/latest", nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lazypueue-maintenance")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", errors.New("Pueue release lookup failed; check network/proxy settings")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("Pueue release lookup returned HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Draft      bool   `json:"draft"`
	}
	if json.Unmarshal(data, &release) != nil || release.Prerelease || release.Draft || !selfupdate.StableVersion(release.Tag) {
		return "", errors.New("no valid stable Pueue release was returned")
	}
	return strings.TrimPrefix(release.Tag, "v"), nil
}
