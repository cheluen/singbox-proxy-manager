//go:build !linux

package services

import "os/exec"

// configureSysProcAttr is a no-op on platforms without parent-death signals
// (Pdeathsig is Linux-only); those platforms rely on the manager's signal
// handler to stop the child.
func configureSysProcAttr(cmd *exec.Cmd) {}
