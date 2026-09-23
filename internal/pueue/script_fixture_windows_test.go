//go:build windows

package pueue

import (
	"io"
	"os"
	"os/exec"
	"testing"
)

// The product launches a real native executable on Windows. Only the fixture
// translates its existing POSIX protocol script through Git's test shell.
func TestMain(m *testing.M) {
	self, err := os.Executable()
	if err == nil {
		if _, err := os.Stat(self + ".fixture.sh"); err == nil {
			cmd := exec.Command("sh", append([]string{self + ".fixture.sh"}, os.Args[1:]...)...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					os.Exit(e.ExitCode())
				}
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}
func writeFakeScript(t *testing.T, path, script string) string {
	t.Helper()
	path += ".exe"
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err := os.WriteFile(path+".fixture.sh", []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
