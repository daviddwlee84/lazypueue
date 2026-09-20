package maintenance

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This optional test connects only to a disposable loopback sshd. It does not
// use the user's SSH configuration, known_hosts, daemons, or package managers.
func TestDisposableSSHTransport(t *testing.T) {
	if os.Getenv("LAZYPUEUE_SSH_INTEGRATION") != "1" {
		t.Skip("set LAZYPUEUE_SSH_INTEGRATION=1 for a disposable localhost sshd")
	}
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		sshd = "/usr/sbin/sshd"
		if _, err = os.Stat(sshd); err != nil {
			t.Skip("sshd is unavailable")
		}
	}
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen unavailable")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	if account.Uid == "0" {
		t.Skip("fixture intentionally does not enable root login")
	}
	dir := t.TempDir()
	for _, name := range []string{"host", "user"} {
		cmd := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create fixture keys: %v %s", err, out)
		}
	}
	key, _ := os.ReadFile(filepath.Join(dir, "user.pub"))
	if err = os.WriteFile(filepath.Join(dir, "authorized_keys"), key, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	serverConfig := fmt.Sprintf("Port %d\nListenAddress 127.0.0.1\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nPermitRootLogin no\nAllowUsers %s\nUseDNS no\nLogLevel ERROR\n", port, filepath.Join(dir, "host"), filepath.Join(dir, "sshd.pid"), filepath.Join(dir, "authorized_keys"), account.Username)
	serverPath := filepath.Join(dir, "sshd_config")
	if err = os.WriteFile(serverPath, []byte(serverConfig), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, sshd, "-D", "-e", "-f", serverPath)
	var output bytes.Buffer
	server.Stdout = &output
	server.Stderr = &output
	if err = server.Start(); err != nil {
		t.Skipf("cannot start user-owned sshd: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	exited := false
	t.Cleanup(func() {
		if !exited {
			_ = server.Process.Kill()
			<-done
		}
	})
	ready := false
	for i := 0; i < 30; i++ {
		select {
		case err := <-done:
			exited = true
			t.Skipf("local sshd cannot run in this environment: %v: %s", err, output.String())
		default:
		}
		conn, e := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
		if e == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatal("fixture sshd did not listen")
	}
	hostKey, _ := os.ReadFile(filepath.Join(dir, "host.pub"))
	fields := strings.Fields(string(hostKey))
	known := filepath.Join(dir, "known_hosts")
	if err = os.WriteFile(known, []byte(fmt.Sprintf("[127.0.0.1]:%d %s %s\n", port, fields[0], fields[1])), 0600); err != nil {
		t.Fatal(err)
	}
	clientConfig := fmt.Sprintf("Host fixture\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n UserKnownHostsFile %s\n StrictHostKeyChecking yes\n ControlMaster no\n", port, account.Username, filepath.Join(dir, "user"), known)
	clientPath := filepath.Join(dir, "ssh_config")
	if err = os.WriteFile(clientPath, []byte(clientConfig), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	_ = os.Mkdir(bin, 0700)
	wrapper := "#!/bin/sh\nexec " + quote(ssh) + " -F " + quote(clientPath) + " \"$@\"\n"
	if err = os.WriteFile(filepath.Join(bin, "ssh"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	marker := filepath.Join(dir, "not-executed")
	payload := "quoted ' \" $HOME $(touch " + marker + ")\n中文"
	runner := commandRunner{}
	got, err := runner.Run(ctx, Target{SSHHost: "fixture"}, []string{"sh", "-c", `printf '%s' "$1"`, "probe", payload})
	if err != nil {
		t.Fatalf("loopback SSH authentication/transport: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("argv changed: %q", got)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("payload was evaluated")
	}
	if _, err = runner.Run(ctx, Target{SSHHost: "fixture"}, []string{"sh", "-c", "exit 7"}); err == nil {
		t.Fatal("remote failure was lost")
	}
}
