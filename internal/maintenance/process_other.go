//go:build !darwin && !linux

package maintenance

import "os/exec"

func configureCommand(cmd *exec.Cmd) {}
