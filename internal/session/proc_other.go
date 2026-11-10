//go:build !unix

package session

import "os/exec"

func detach(cmd *exec.Cmd) {}

func signalGroup(cmd *exec.Cmd, kill bool) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
