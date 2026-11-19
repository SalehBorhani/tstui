//go:build unix

package tsh

import (
	"os/exec"
	"syscall"
)

// setProcessGroup runs the command in its own process group and makes
// cancellation kill the whole group, so children of tsh (or of a wrapper
// script) cannot outlive it and keep its pipes open.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
