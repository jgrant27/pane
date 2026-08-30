//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// detachAgent puts grok agent serve in its own process group so pane
// exit does not SIGHUP it. #57 #61
func detachAgent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
