//go:build darwin

package main

import (
	"os/exec"
	"strings"
)

func pickFolder(prompt, def string) (string, error) {
	script := "try\n"
	script += "set theFolder to choose folder with prompt " + asString(prompt)
	if def != "" {
		script += " default location POSIX file " + asString(def)
	}
	script += "\nreturn POSIX path of theFolder\non error\nreturn \"\"\nend try\n"
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		// user cancel is an error with empty stdout
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func asString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return `"` + s + `"`
}
