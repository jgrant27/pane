//go:build unix

package main

import (
	"os/exec"
	"testing"
)

func TestDetachAgentSetsPgid(t *testing.T) {
	cmd := exec.Command("true")
	detachAgent(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatal("#57: detachAgent must Setpgid so pane exit does not SIGHUP the agent")
	}
}
