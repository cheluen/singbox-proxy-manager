//go:build !linux && !darwin && !freebsd

package services

import "os/exec"

// configureSysProcAttr is a no-op on platforms without parent-death signals
// (Pdeathsig is Linux-only); those platforms rely on the manager's signal
// handler to stop the child.
func configureSysProcAttr(_ *exec.Cmd) (*commandProcessGuard, error) {
	return newCommandProcessGuard(), nil
}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
