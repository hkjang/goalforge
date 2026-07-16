//go:build windows

// Package procctl provides platform-specific process-group control so the
// orchestrator can signal an AI CLI process together with every child it
// spawned.
package procctl

import (
	"os/exec"
	"strconv"
	"syscall"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// SetGroup starts cmd in a new process group so console control events target
// the whole tree instead of the orchestrator itself.
func SetGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// KillGroup forcibly terminates cmd's process tree. taskkill /T covers child
// processes that a plain Process.Kill would orphan.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	if err := kill.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// InterruptGroup sends CTRL_BREAK to cmd's process group, the closest Windows
// analogue of SIGINT for a graceful stop.
func InterruptGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	r, _, err := procGenerateConsoleCtrlEvent.Call(uintptr(syscall.CTRL_BREAK_EVENT), uintptr(cmd.Process.Pid))
	if r == 0 {
		return err
	}
	return nil
}
