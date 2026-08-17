//go:build darwin || freebsd

package services

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func configureSysProcAttr(cmd *exec.Cmd) (*commandProcessGuard, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create parent-lifetime pipe: %w", err)
	}
	guard := newCommandProcessGuard(reader, writer)

	originalArgs := append([]string(nil), cmd.Args...)
	guardFD := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, reader)
	cmd.Path = "/bin/sh"
	cmd.Args = append([]string{
		"sh",
		"-c",
		fmt.Sprintf(parentLifetimeWatchdogScript, guardFD),
		"sbpm-parent-watchdog",
	}, originalArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return guard, nil
}

const parentLifetimeWatchdogScript = `
watchdog_group=$$
"$@" &
child_pid=$!
(
  while IFS= read -r _ <&%d; do :; done
  kill -KILL -"$watchdog_group" 2>/dev/null || kill -KILL "$child_pid" 2>/dev/null || true
) &
pipe_watcher=$!
wait "$child_pid"
status=$?
kill "$pipe_watcher" 2>/dev/null || true
wait "$pipe_watcher" 2>/dev/null || true
exit "$status"
`

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
