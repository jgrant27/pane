//go:build windows

package main

import (
	"os/exec"
	"strings"
)

func pickFolder(prompt, def string) (string, error) {
	ps := `Add-Type -AssemblyName System.Windows.Forms; $d = New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description = '` +
		strings.ReplaceAll(prompt, "'", "''") + `'; $d.ShowNewFolderButton = $true;`
	if def != "" {
		ps += ` $d.SelectedPath = '` + strings.ReplaceAll(def, "'", "''") + `';`
	}
	ps += ` if ($d.ShowDialog() -eq 'OK') { $d.SelectedPath }`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}
