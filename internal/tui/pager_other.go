//go:build !windows

package tui

import "os/exec"

func stopPagerSource(cmd *exec.Cmd) bool  { return cmd.Process != nil && cmd.Process.Kill() == nil }
func intentionallyStoppedPager(bool) bool { return false }
