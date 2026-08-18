//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func pickFiles(prompt string) ([]string, error) {
	script := "try\n"
	script += "set theFiles to choose file with prompt " + asString(prompt) + " with multiple selections allowed\n"
	script += "set out to \"\"\n"
	script += "repeat with f in theFiles\n"
	script += "set out to out & POSIX path of f & linefeed\n"
	script += "end repeat\n"
	script += "return out\non error\nreturn \"\"\nend try\n"
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, nil
	}
	return splitPaths(string(out)), nil
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
