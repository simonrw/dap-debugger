//go:build !unix

package ui

import "os/exec"

func ownGroup(cmd *exec.Cmd) {}
