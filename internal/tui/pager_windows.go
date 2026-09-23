//go:build windows

package tui

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"path/filepath"
	"syscall"
)

func stopPagerSource(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return false
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return cmd.Process.Kill() == nil
	}
	kill := exec.Command(filepath.Join(system, "taskkill.exe"), "/PID", fmt.Sprint(cmd.Process.Pid), "/T", "/F")
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return kill.Run() == nil
}
func intentionallyStoppedPager(killed bool) bool { return killed }
