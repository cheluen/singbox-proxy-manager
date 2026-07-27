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
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
