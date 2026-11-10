//go:build unix

package ui

import (
	"os/exec"
	"syscall"
)

// ownGroup runs a task in its own process group, so cancelling it also stops
// the processes it starts, such as a server behind a shell.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}
