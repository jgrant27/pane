//go:build linux

package main

import (
	"os/exec"
	"strings"
)

func pickFolder(prompt, def string) (string, error) {
	if _, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--directory", "--title=" + prompt}
		if def != "" {
			args = append(args, "--filename="+def+"/")
		}
		out, err := exec.Command("zenity", args...).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err := exec.LookPath("kdialog"); err == nil {
		args := []string{"--getexistingdirectory"}
		if def != "" {
			args = append(args, def)
		} else {
			args = append(args, ".")
		}
		out, err := exec.Command("kdialog", args...).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	}
	return "", nil
}
