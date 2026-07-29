//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon in its own session so it survives the UI process:
// Setsid detaches it from the app's controlling terminal and process group, so
// quitting the app (or the app being killed) never takes the engine down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
