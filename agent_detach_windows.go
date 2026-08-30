//go:build windows

package main

import "os/exec"

// Windows does not SIGHUP a child when the parent exits. #61
func detachAgent(cmd *exec.Cmd) {}
