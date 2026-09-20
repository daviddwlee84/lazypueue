package pueue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/daviddwlee84/lazypueue/internal/core"
)

const maxOutput = 64 << 20
const readTimeout = 10 * time.Second
const writeTimeout = 15 * time.Second

// Error is safe to present as a diagnostic. Unknown marks a dispatched write
// whose response was lost; callers must reconcile instead of blindly retrying.
type Error struct {
	Kind    string
	Detail  string
	Unknown bool
	Cause   error
}

func (e *Error) Error() string { return e.Detail }
func (e *Error) Unwrap() error { return e.Cause }

type Client struct {
	mu       sync.Mutex
	dir      string
	sshHosts map[string]bool
	closed   bool
	ctx      context.Context
	cancel   context.CancelFunc
}

func New() *Client {
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{ctx: ctx, cancel: cancel}
}

func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	dir := c.dir
	hosts := make([]string, 0, len(c.sshHosts))
	for host := range c.sshHosts {
		hosts = append(hosts, host)
	}
	c.mu.Unlock()
	c.cancel()
	for _, host := range hosts {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = exec.CommandContext(ctx, "ssh", "-o", "ControlPath="+filepath.Join(dir, "ssh-%C"), "-O", "exit", "--", host).Run()
		cancel()
	}
	if dir != "" {
		return os.RemoveAll(dir)
	}
	return nil
}

// ShellQuote quotes exactly one argument for a POSIX remote shell.
func ShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
func shellQuote(value string) string { return strings.TrimSuffix(ShellQuote(value), " ") }
func remotePath(path string) string {
	if path == "~" || path == "$HOME" {
		return `"$HOME"`
	}
	if strings.HasPrefix(path, "~/") {
		return `"$HOME"/` + shellQuote(path[2:])
	}
	if strings.HasPrefix(path, "$HOME/") {
		return `"$HOME"/` + shellQuote(path[6:])
	}
	return shellQuote(path)
}
func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func remoteJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "--working-directory=") {
			quoted[i] = shellQuote("--working-directory=") + remotePath(strings.TrimPrefix(arg, "--working-directory="))
		} else if i > 0 && (args[i-1] == "--working-directory" || args[i-1] == "--config") {
			quoted[i] = remotePath(arg)
		} else {
			quoted[i] = shellQuote(arg)
		}
	}
	return strings.Join(quoted, " ")
}

func validateConnection(conn core.Connection) error {
	switch conn.Kind {
	case "", "local", "native":
	case "ssh":
		if conn.SSHHost == "" {
			return fmt.Errorf("SSH connection requires a host")
		}
	default:
		return fmt.Errorf("unknown connection kind %q", conn.Kind)
	}
	if strings.HasPrefix(conn.SSHHost, "-") || strings.ContainsAny(conn.SSHHost, "\r\n\x00") {
		return fmt.Errorf("invalid SSH host")
	}
	if conn.Port < 0 || conn.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	for _, field := range []string{conn.Binary, conn.SSHBinary, conn.ConfigPath, conn.SSHConfig, conn.Profile, conn.SSHProfile} {
		if strings.ContainsRune(field, 0) {
			return fmt.Errorf("connection fields cannot contain NUL bytes")
		}
	}
	return nil
}

func remoteConfig(conn core.Connection) (string, string) {
	config, profile := conn.SSHConfig, conn.SSHProfile
	if conn.Kind == "ssh" {
		if config == "" {
			config = conn.ConfigPath
		}
		if profile == "" {
			profile = conn.Profile
		}
	}
	return config, profile
}

func (c *Client) sshArgs(conn core.Connection, args []string, interactive bool) ([]string, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if conn.SSHHost == "" {
		return nil, fmt.Errorf("this connection has no SSH host")
	}
	dir, err := c.tempDir()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.sshHosts == nil {
		c.sshHosts = map[string]bool{}
	}
	c.sshHosts[conn.SSHHost] = true
	c.mu.Unlock()
	mode := "yes"
	if interactive {
		mode = "no"
	}
	result := []string{"-o", "BatchMode=" + mode, "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-o", "ControlMaster=auto", "-o", "ControlPersist=120", "-o", "ControlPath=" + filepath.Join(dir, "ssh-%C"), "--", conn.SSHHost}
	binary := conn.SSHBinary
	if binary == "" && conn.Kind == "ssh" {
		binary = conn.Binary
	}
	if binary == "" {
		binary = "pueue"
	}
	command := remotePath(binary) + " --color never"
	config, profile := remoteConfig(conn)
	if config != "" {
		command += " --config " + remotePath(config)
	}
	if profile != "" {
		command += " --profile " + shellQuote(profile)
	}
	command += " " + remoteJoin(args)
	return append(result, command), nil
}

// AuthenticateCommand is intended for an explicit terminal handoff. Ordinary
// reads always use BatchMode; no hidden password or host-key prompt can hang UI.
func (c *Client) AuthenticateCommand(conn core.Connection) (*exec.Cmd, error) {
	args, err := c.sshArgs(conn, []string{"--version"}, true)
	if err != nil {
		return nil, err
	}
	return exec.Command("ssh", args...), nil
}

func (c *Client) command(ctx context.Context, conn core.Connection, args []string, submission bool) (*exec.Cmd, error) {
	if err := validateConnection(conn); err != nil {
		return nil, err
	}
	if conn.Kind == "ssh" || (conn.Kind == "native" && submission && conn.SSHHost != "") {
		argv, err := c.sshArgs(conn, args, false)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "ssh", argv...)
		cmd.WaitDelay = 2 * time.Second
		return cmd, nil
	}
	cfg, err := c.effectiveConfig(conn)
	if err != nil {
		return nil, err
	}
	binary := conn.Binary
	if binary == "" {
		binary = "pueue"
	}
	argv := append([]string{"--color", "never", "--config", cfg}, args...)
	cmd := exec.CommandContext(ctx, expandLocal(binary), argv...)
	cmd.WaitDelay = 2 * time.Second
	return cmd, nil
}

type boundedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining < n {
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		b.overflow = true
		return n, nil
	}
	return b.Buffer.Write(p)
}

func (c *Client) operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(c.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

func classify(err error, stderr string, ctx context.Context, started, mutation bool) *Error {
	detail := strings.TrimSpace(ansi.Strip(stderr))
	if len(detail) > 8192 {
		detail = detail[:8192] + "…"
	}
	kind := "command"
	if errors.Is(err, exec.ErrNotFound) {
		kind = "binary-missing"
	}
	if ctx.Err() != nil {
		kind = "cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = "timeout"
		}
		detail = "Pueue operation " + kind
	}
	if strings.Contains(detail, "Permission denied") || strings.Contains(detail, "Host key verification failed") {
		kind = "ssh-auth"
	}
	if strings.Contains(detail, "Did you start") || strings.Contains(detail, "connecting to daemon") {
		kind = "daemon-unavailable"
	}
	if strings.Contains(detail, "Failed to initialize tls") || strings.Contains(detail, "certificate") {
		kind = "tls"
	}
	if strings.Contains(detail, "correct secret") {
		kind = "authentication"
	}
	if strings.Contains(detail, "command not found") {
		kind = "binary-missing"
	}
	if detail == "" {
		detail = err.Error()
	}
	// An SSH connection may disappear after the remote daemon accepted a write.
	var exitErr *exec.ExitError
	unknown := mutation && started && (ctx.Err() != nil || (errors.As(err, &exitErr) && exitErr.ExitCode() == 255) || errors.Is(err, exec.ErrWaitDelay))
	if mutation && started {
		for _, text := range []string{"Connection went away", "Failed to receive", "failed to fill whole buffer", "Broken pipe", "Connection reset", "unexpected EOF"} {
			if strings.Contains(detail, text) {
				unknown = true
			}
		}
	}
	if unknown {
		detail += "; outcome unknown — refresh before retrying"
	}
	return &Error{Kind: kind, Detail: detail, Unknown: unknown, Cause: err}
}

func (c *Client) run(parent context.Context, conn core.Connection, args []string, mutation, submission bool) ([]byte, error) {
	timeout := readTimeout
	if mutation {
		timeout = writeTimeout
	}
	ctx, cancel := c.operationContext(parent, timeout)
	defer cancel()
	cmd, err := c.command(ctx, conn, args, submission)
	if err != nil {
		return nil, err
	}
	output := &boundedBuffer{limit: maxOutput}
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stdout = output
	cmd.Stderr = stderr
	if mutation {
		cmd.Stdin = strings.NewReader("y\n")
	}
	if err = cmd.Start(); err != nil {
		return nil, classify(err, stderr.String(), ctx, false, mutation)
	}
	err = cmd.Wait()
	if err != nil {
		return nil, classify(err, stderr.String(), ctx, true, mutation)
	}
	if output.overflow {
		return nil, &Error{Kind: "output-too-large", Detail: "Pueue output exceeds the 64 MiB limit", Unknown: mutation}
	}
	return output.Bytes(), nil
}

func (c *Client) Follow(parent context.Context, conn core.Connection, id, lines int, onData func(string)) error {
	if id < 0 || lines < 1 {
		return fmt.Errorf("task ID must be nonnegative and line count positive")
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	defer cancel()
	cmd, err := c.command(ctx, conn, []string{"follow", strconv.Itoa(id), "--lines", strconv.Itoa(lines)}, false)
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return classify(err, "", ctx, false, false)
	}
	buf := make([]byte, 16*1024)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 && onData != nil {
			onData(string(buf[:n]))
		}
		if readErr != nil {
			if readErr != io.EOF && ctx.Err() == nil {
				cancel()
				_ = cmd.Wait()
				return fmt.Errorf("read task output: %w", readErr)
			}
			break
		}
	}
	if err = cmd.Wait(); err != nil {
		return classify(err, stderr.String(), ctx, true, false)
	}
	return nil
}
