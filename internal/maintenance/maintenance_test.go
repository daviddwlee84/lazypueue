package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daviddwlee84/lazypueue/internal/core"
)

type fixture struct {
	t                                                                                   *testing.T
	candidate                                                                           string
	upgraded, stopped, started, paused, active, newActive, missingReceipt, wrongService bool
	fail                                                                                string
	groups                                                                              []core.Group
	writes                                                                              []string
	seenTarget                                                                          Target
	seenQueue                                                                           core.Connection
}

func newFixture(t *testing.T) (*Service, *fixture) {
	f := &fixture{t: t, candidate: "4.0.4", groups: []core.Group{{Name: "default", Status: "Running", Parallel: 1}, {Name: "hold", Status: "Paused", Parallel: 2}}}
	return &Service{Backend: f, Runner: f, ReceiptDir: filepath.Join(t.TempDir(), "receipts")}, f
}
func (f *fixture) Snapshot(ctx context.Context, c core.Connection) (core.Snapshot, error) {
	f.seenQueue = c
	if ctx.Err() != nil {
		return core.Snapshot{}, ctx.Err()
	}
	tasks := []core.Task{{ID: 1, Status: "Queued", Group: "default"}}
	if f.active || f.paused && f.newActive {
		tasks = append(tasks, core.Task{ID: 7, Status: "Running", Group: "default"})
	}
	return core.Snapshot{ConnectionID: c.ID, Tasks: tasks, Groups: append([]core.Group(nil), f.groups...)}, nil
}
func (f *fixture) Log(context.Context, core.Connection, int, int) (string, error) {
	panic("unexpected Log")
}
func (f *fixture) Follow(context.Context, core.Connection, int, int, func(string)) error {
	panic("unexpected Follow")
}
func (f *fixture) Preview(core.Connection, core.Request) (core.Plan, error) {
	panic("unexpected Preview")
}
func (f *fixture) Execute(context.Context, core.Connection, core.Request) (core.Result, error) {
	panic("maintenance must use reviewed typed commands")
}
func (f *fixture) Run(ctx context.Context, target Target, args []string) ([]byte, error) {
	f.seenTarget = target
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	version := "4.0.2"
	if f.upgraded {
		version = f.candidate
	}
	root := "/opt/homebrew/Cellar/pueue/" + version + "/bin/"
	if len(args) >= 3 && args[0] == "sh" && args[2] == resolveScript {
		switch args[len(args)-1] {
		case "pueue":
			return []byte(root + "pueue\n"), nil
		case "/opt/homebrew/bin/brew":
			return []byte("/opt/homebrew/Library/Homebrew/brew.sh\n"), nil
		case "/opt/homebrew/opt/pueue/bin/pueued":
			return []byte(root + "pueued\n"), nil
		}
		return nil, errors.New("unrecognized executable")
	}
	if reflect.DeepEqual(args, []string{"id", "-u"}) {
		return []byte("1000\n"), nil
	}
	if reflect.DeepEqual(args, []string{"id", "-un"}) {
		return []byte("tester\n"), nil
	}
	if len(args) == 2 && args[1] == "--version" {
		name := "pueue"
		if strings.HasSuffix(args[0], "pueued") {
			name = "pueued"
		}
		return []byte(name + " " + version + "\n"), nil
	}
	if args[0] == "cat" && strings.HasSuffix(args[1], "INSTALL_RECEIPT.json") {
		if f.missingReceipt {
			return nil, os.ErrNotExist
		}
		return []byte(`{"source":{"tap":"homebrew/core"}}`), nil
	}
	if len(args) > 2 && args[0] == "sh" && args[2] == environmentScript {
		return []byte("/home/test\n\n\n\nLinux\n"), nil
	}
	if args[0] == "cat" && args[1] == "/home/test/.config/pueue/pueue.yml" {
		return []byte("shared: {}\n"), nil
	}
	if args[0] == "cat" && args[1] == "/home/test/.local/share/pueue/pueue.pid" {
		if f.started {
			return []byte("43\n"), nil
		}
		return []byte("42\n"), nil
	}
	if args[0] == "cat" {
		return nil, os.ErrNotExist
	}
	if len(args) > 1 && args[1] == "info" {
		return []byte(fmt.Sprintf(`{"formulae":[{"name":"pueue","full_name":"pueue","versions":{"stable":%q},"installed":[{"version":%q}]}]}`, f.candidate, version)), nil
	}
	if len(args) > 1 && args[1] == "--cellar" {
		return []byte("/opt/homebrew/Cellar/pueue\n"), nil
	}
	if len(args) > 2 && args[1] == "services" && args[2] == "info" {
		cmd := "/opt/homebrew/opt/pueue/bin/pueued --verbose"
		if f.wrongService {
			cmd += " --config /different/daemon.yml"
		}
		pid := 42
		if f.started {
			pid = 43
		}
		return []byte(fmt.Sprintf(`[{"name":"pueue","service_name":"homebrew.mxcl.pueue","command":%q,"user":"tester","registered":true,"running":true,"pid":%d}]`, cmd, pid)), nil
	}
	if args[0] == "systemctl" {
		return nil, errors.New("no service")
	}
	if len(args) >= 3 && args[0] == "sh" && strings.Contains(args[2], "PUEUE_CONFIG_PATH") {
		return nil, nil
	}
	name := ""
	switch {
	case args[0] == "rmdir":
		name = "unlock"
	case args[0] == "sh" && strings.Contains(args[2], "mkdir"):
		name = "lock"
	case contains(args, "pause"):
		name = "pause"
	case contains(args, "upgrade"):
		name = "upgrade"
	case contains(args, "services") && contains(args, "stop"):
		name = "stop"
	case contains(args, "services") && contains(args, "start"):
		name = "start-service"
	case contains(args, "start") && contains(args, "--group"):
		name = "resume-" + args[len(args)-1]
	}
	if name == "" {
		f.t.Fatalf("unexpected runner args %q", args)
	}
	f.writes = append(f.writes, name)
	if f.fail == name {
		return nil, errors.New("injected " + name + " failure")
	}
	switch name {
	case "pause":
		if !contains(args, "--wait") {
			f.t.Fatal("suspended tasks instead of only scheduling")
		}
		f.paused = true
		for i := range f.groups {
			f.groups[i].Status = "Paused"
		}
	case "stop":
		f.stopped = true
	case "upgrade":
		if !f.stopped {
			f.t.Fatal("package update before stopping verified daemon")
		}
		f.upgraded = true
	case "start-service":
		f.started = true
	case "resume-default":
		f.groups[0].Status = "Running"
	}
	return nil, nil
}
func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestCheckIsReadOnlyAndApplyRestoresOnlyOriginalRunningGroups(t *testing.T) {
	s, f := newFixture(t)
	p, err := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	if err != nil || !p.CanApply {
		t.Fatalf("check: %+v %v", p, err)
	}
	if len(f.writes) > 0 {
		t.Fatalf("check mutated: %v", f.writes)
	}
	if _, err := os.Stat(s.ReceiptDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("check created receipts")
	}
	r, err := s.Apply(context.Background(), p, io.Discard)
	if err != nil || r.Status != "updated" || !r.PackageUpdated || !r.DaemonStarted {
		t.Fatalf("apply: %+v %v", r, err)
	}
	want := []string{"lock", "pause", "stop", "upgrade", "start-service", "resume-default", "unlock"}
	if !reflect.DeepEqual(f.writes, want) {
		t.Fatalf("writes=%v", f.writes)
	}
	data, err := os.ReadFile(r.ReceiptPath)
	if err != nil || !json.Valid(data) {
		t.Fatalf("receipt: %v %s", err, data)
	}
	info, _ := os.Stat(r.ReceiptPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal("receipt permissions")
	}
	if f.groups[1].Status != "Paused" {
		t.Fatal("resumed a previously paused group")
	}
}
func TestActiveQueueNeverChangesScheduling(t *testing.T) {
	s, f := newFixture(t)
	f.active = true
	p, err := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	if err != nil || p.CanApply || len(p.ActiveIDs) != 1 {
		t.Fatalf("active check: %+v %v", p, err)
	}
	_, _ = s.Apply(context.Background(), p, io.Discard)
	if len(f.writes) != 0 {
		t.Fatalf("active writes: %v", f.writes)
	}
}
func TestBusyOrChangedAfterReviewRefusesBeforeWrites(t *testing.T) {
	for _, change := range []string{"active", "candidate", "groups"} {
		t.Run(change, func(t *testing.T) {
			s, f := newFixture(t)
			p, _ := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
			switch change {
			case "active":
				f.active = true
			case "candidate":
				f.candidate = "4.0.5"
			case "groups":
				f.groups[0].Status = "Paused"
			}
			_, err := s.Apply(context.Background(), p, io.Discard)
			if err == nil || len(f.writes) > 0 {
				t.Fatalf("changed review applied: %v %v", err, f.writes)
			}
		})
	}
}
func TestNewRunningTaskAfterPausePreventsServiceStop(t *testing.T) {
	s, f := newFixture(t)
	p, _ := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	f.newActive = true
	r, err := s.Apply(context.Background(), p, io.Discard)
	if err == nil || r.Status != "partial" || f.stopped || f.upgraded {
		t.Fatalf("new task: %+v %v", r, err)
	}
	if !strings.Contains(r.Message, "scheduling remains paused") {
		t.Fatal(r.Message)
	}
}
func TestPartialManagerFailureHasReceiptAndNoFalseRollback(t *testing.T) {
	s, f := newFixture(t)
	p, _ := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	f.fail = "upgrade"
	r, err := s.Apply(context.Background(), p, io.Discard)
	if err == nil || r.Status != "partial" || r.PackageUpdated || f.started || r.ReceiptPath == "" {
		t.Fatalf("partial: %+v %v", r, err)
	}
	if !strings.Contains(r.Message, "not rolled back") {
		t.Fatal(r.Message)
	}
	data, _ := os.ReadFile(r.ReceiptPath)
	if !strings.Contains(string(data), `"state": "failed"`) {
		t.Fatal(string(data))
	}
}
func TestUnknownOwnerAndServiceMismatchNeverApply(t *testing.T) {
	for _, reason := range []string{"receipt", "service"} {
		t.Run(reason, func(t *testing.T) {
			s, f := newFixture(t)
			if reason == "receipt" {
				f.missingReceipt = true
			} else {
				f.wrongService = true
			}
			p, err := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
			if err != nil || p.CanApply || p.Reason == "" {
				t.Fatalf("unsupported: %+v %v", p, err)
			}
			if len(f.writes) > 0 {
				t.Fatal(f.writes)
			}
		})
	}
}
func TestNativeNeedsCompanionAndUsesRemoteQueue(t *testing.T) {
	s, f := newFixture(t)
	p, err := s.Check(context.Background(), core.Connection{ID: "remote", Kind: "native"})
	if err != nil || p.CanApply || !strings.Contains(p.Reason, "SSH companion") {
		t.Fatalf("native: %+v %v", p, err)
	}
	p, err = s.Check(context.Background(), core.Connection{ID: "remote", Kind: "native", SSHHost: "lab"})
	if err != nil || !p.CanApply || f.seenQueue.Kind != "ssh" || f.seenTarget.SSHHost != "lab" {
		t.Fatalf("route: %+v %v %+v", p, err, f.seenQueue)
	}
}
func TestUnknownAndPausedStatusesBlock(t *testing.T) {
	s := core.Snapshot{Tasks: []core.Task{{ID: 1, Status: "Paused"}, {ID: 2, Status: "Queued", Locked: true}, {ID: 3, Status: "FutureState"}, {ID: 4, Status: "Stashed"}, {ID: 5, Status: "Done", Result: "Success"}}}
	if got := activeTasks(s); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatal(got)
	}
}

type runnerFunc func(context.Context, Target, []string) ([]byte, error)

func (f runnerFunc) Run(ctx context.Context, target Target, args []string) ([]byte, error) {
	return f(ctx, target, args)
}
func TestCargoOwnershipAndSystemdIdentity(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprint("private=", private), func(t *testing.T) {
			s, f := newFixture(t)
			s.Candidate = func(context.Context) (string, error) { return "4.0.4", nil }
			root := "/home/test/.cargo"
			s.Runner = runnerFunc(func(ctx context.Context, target Target, args []string) ([]byte, error) {
				if len(args) > 2 && args[0] == "sh" && args[2] == commandPathScript {
					return []byte(root + "/bin/cargo\n"), nil
				}
				if len(args) > 2 && args[0] == "sh" && args[2] == resolveScript {
					switch args[len(args)-1] {
					case "pueue", root + "/bin/pueue":
						return []byte(root + "/bin/pueue\n"), nil
					case "cargo":
						return []byte(root + "/bin/cargo\n"), nil
					case root + "/bin/pueued":
						return []byte(root + "/bin/pueued\n"), nil
					}
				}
				if args[0] == "cat" && args[1] == root+"/.crates.toml" {
					registry := "https://github.com/rust-lang/crates.io-index"
					if private {
						registry = "https://private.example/registry"
					}
					return []byte("[v1]\n\"pueue 4.0.2 (registry+" + registry + ")\" = [\"pueue\", \"pueued\"]\n"), nil
				}
				if args[0] == "systemctl" {
					return []byte("LoadState=loaded\nActiveState=active\nMainPID=42\nExecStart={ path=" + root + "/bin/pueued ; argv[]=" + root + "/bin/pueued --verbose ; ignore_errors=no ; }\n"), nil
				}
				return f.Run(ctx, target, args)
			})
			p, err := s.Check(context.Background(), core.Connection{ID: "lab", Kind: "ssh", SSHHost: "lab"})
			if err != nil {
				t.Fatal(err)
			}
			if private {
				if p.CanApply || p.Owner != "unknown" {
					t.Fatalf("private registry replaced: %+v", p)
				}
				return
			}
			if !p.CanApply || p.Owner != "cargo" || p.ServiceKind != "systemd-user" {
				t.Fatalf("cargo check: %+v", p)
			}
			want := []string{root + "/bin/cargo", "install", "--root", root, "--locked", "--version", "4.0.4", "pueue"}
			if !reflect.DeepEqual(s.upgradeArgs(p), want) {
				t.Fatalf("wrong install root: %q", s.upgradeArgs(p))
			}
		})
	}
}

func TestRemoteUncertainWriteRetainsHostLock(t *testing.T) {
	s, f := newFixture(t)
	p, err := s.Check(context.Background(), core.Connection{ID: "lab", Kind: "ssh", SSHHost: "lab"})
	if err != nil {
		t.Fatal(err)
	}
	f.fail = "upgrade"
	r, err := s.Apply(context.Background(), p, io.Discard)
	if err == nil || !r.Unknown || r.LockPath == "" || contains(f.writes, "unlock") {
		t.Fatalf("lost remote protection: %+v %v %v", r, err, f.writes)
	}
	data, _ := os.ReadFile(r.ReceiptPath)
	if !strings.Contains(string(data), "remote maintenance lock remains") {
		t.Fatal(string(data))
	}
}

func TestDaemonPIDMismatchCannotStopAnotherQueue(t *testing.T) {
	s, f := newFixture(t)
	s.Runner = runnerFunc(func(ctx context.Context, target Target, args []string) ([]byte, error) {
		if args[0] == "cat" && strings.HasSuffix(args[1], "/pueue.pid") {
			return []byte("999\n"), nil
		}
		return f.Run(ctx, target, args)
	})
	p, err := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	if err != nil || p.CanApply || !strings.Contains(p.Reason, "PID differs") {
		t.Fatalf("wrong daemon was accepted: %+v %v", p, err)
	}
	if len(f.writes) != 0 {
		t.Fatal(f.writes)
	}
}
func TestModifiedCapturedPlanCannotRedirectPackageCommands(t *testing.T) {
	s, f := newFixture(t)
	p, err := s.Check(context.Background(), core.Connection{ID: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p.Target.SSHHost = "different-host"
	_, err = s.Apply(context.Background(), p, io.Discard)
	if err == nil || len(f.writes) > 0 {
		t.Fatalf("modified plan applied: %v %v", err, f.writes)
	}
}
