//go:build linux

package services

import (
	"os/exec"
	"syscall"
)

// configureSysProcAttr asks the Linux kernel to deliver SIGKILL to the
// sing-box child when the manager process dies, covering paths where no
// cleanup code can run (kill -9, OOM kill, panics in cgo, ...).
func configureSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGKILL,
		Setpgid:   true,
	}
}

// terminateProcess kills the complete process group. This also reaps helper
// processes spawned by wrappers and prevents background children from
// surviving a sing-box restart.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
