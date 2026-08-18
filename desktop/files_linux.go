//go:build linux

package main

import (
	"os/exec"
	"strings"
)

func pickFiles(prompt string) ([]string, error) {
	if _, err := exec.LookPath("zenity"); err == nil {
		out, err := exec.Command("zenity", "--file-selection", "--multiple", "--separator=\n", "--title="+prompt).Output()
		if err != nil {
			return nil, nil
		}
		return splitPaths(string(out)), nil
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		out, err := exec.Command("kdialog", "--getopenfilename", "--multiple", ".").Output()
		if err != nil {
			return nil, nil
		}
		return splitPaths(strings.ReplaceAll(string(out), " ", "\n")), nil
	}
	return nil, nil
}

func splitPaths(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
