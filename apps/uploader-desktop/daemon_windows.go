//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// detach starts the daemon detached from the UI: DETACHED_PROCESS gives it no
// console and CREATE_NEW_PROCESS_GROUP puts it in its own group, so the engine
// keeps running after the app window is closed or the app exits.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windowsDetachedProcess | windowsNewProcessGroup,
	}
}

const (
	windowsDetachedProcess = 0x00000008 // DETACHED_PROCESS
	windowsNewProcessGroup = 0x00000200 // CREATE_NEW_PROCESS_GROUP
)
