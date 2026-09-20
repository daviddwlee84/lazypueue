package maintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type commandRunner struct{}

const pathPrelude = `export PATH="$HOME/.cargo/bin:$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:/home/linuxbrew/.linuxbrew/bin:$PATH"; export HOMEBREW_NO_AUTO_UPDATE=1; `

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func join(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = quote(a)
	}
	return strings.Join(parts, " ")
}
func (commandRunner) Run(ctx context.Context, target Target, args []string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("empty maintenance command")
	}
	var cmd *exec.Cmd
	if target.SSHHost != "" {
		cmd = exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "--", target.SSHHost, pathPrelude+"exec "+join(args))
	} else {
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")
	}
	var stdout limitedBuffer
	stdout.limit = 4 << 20
	var stderr limitedBuffer
	stderr.limit = 64 << 10
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second
	configureCommand(cmd)
	err := cmd.Run()
	if ctx.Err() != nil {
		return stdout.Bytes(), ctx.Err()
	}
	if err != nil {
		return stdout.Bytes(), fmt.Errorf("%s failed; inspect the package/service manager on the target host: %w", filepath.Base(args[0]), err)
	}
	return stdout.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("maintenance output limit exceeded")
	}
	return b.Buffer.Write(p)
}

const resolveScript = `set -eu
case "$1" in '~/'*) candidate="$HOME/${1#\~/}";; '$HOME/'*) candidate="$HOME/${1#\$HOME/}";; *) candidate="$1";; esac
case "$candidate" in /*) p="$candidate";; *) p=$(command -v "$candidate") || exit 1;; esac
n=0
while [ -L "$p" ]; do
 n=$((n+1)); [ "$n" -lt 64 ] || exit 1
 base=$(CDPATH= cd -P -- "$(dirname -- "$p")" && pwd)
 link=$(readlink "$p")
 case "$link" in /*) p="$link";; *) p="$base/$link";; esac
done
[ -f "$p" ] && [ -x "$p" ] || exit 1
base=$(CDPATH= cd -P -- "$(dirname -- "$p")" && pwd)
printf '%s/%s\n' "$base" "$(basename -- "$p")"`

const commandPathScript = `set -eu; p=$(command -v "$1"); case "$p" in /*) ;; *) p="$PWD/$p";; esac; [ -f "$p" ] && [ -x "$p" ]; printf '%s\n' "$p"`

func (s *Service) commandPath(ctx context.Context, target Target, name string) (string, error) {
	data, err := s.read(ctx, target, "sh", "-c", commandPathScript, "find", name)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(data))
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\r\n\x00") {
		return "", errors.New("cannot identify command invocation path")
	}
	return p, nil
}

func (s *Service) read(ctx context.Context, target Target, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return s.Runner.Run(ctx, target, args)
}
func (s *Service) resolve(ctx context.Context, target Target, binary string) (string, error) {
	if binary == "" || strings.HasPrefix(binary, "-") || strings.ContainsAny(binary, "\n\r\x00") {
		return "", errors.New("invalid executable reference")
	}
	data, err := s.read(ctx, target, "sh", "-c", resolveScript, "resolve", binary)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(string(data))
	if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\n\r\x00") {
		return "", errors.New("could not resolve an absolute executable")
	}
	return p, nil
}
