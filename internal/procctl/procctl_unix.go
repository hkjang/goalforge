//go:build unix

// Package procctl provides platform-specific process-group control so the
// orchestrator can signal an AI CLI process together with every child it
// spawned.
package procctl

import (
	"os/exec"
	"syscall"
)

// SetGroup places cmd in its own process group so the whole tree can be signalled.
func SetGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillGroup forcibly terminates cmd's process group.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// InterruptGroup delivers SIGINT to cmd's process group for a graceful stop.
func InterruptGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
}
