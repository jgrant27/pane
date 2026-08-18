//go:build windows

package main

import (
	"os/exec"
	"strings"
)

func pickFiles(prompt string) ([]string, error) {
	ps := `Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.OpenFileDialog; $d.Title = '` +
		strings.ReplaceAll(prompt, "'", "''") + `'; $d.Multiselect = $true; $d.Filter = 'All files|*.*'; if ($d.ShowDialog() -eq 'OK') { $d.FileNames -join "` + "`n" + `" }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
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
