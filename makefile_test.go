package main

import (
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	autoTailnet = false
	os.Exit(m.Run())
}

func TestMakefileRemoteTarget(t *testing.T) {
	b, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if strings.Contains(src, "\nphone:") {
		t.Fatal("Makefile target is remote, not phone")
	}
	i := strings.Index(src, "\nremote:")
	if i < 0 {
		t.Fatal("Makefile must have a remote target")
	}
	recipe := src[i:]
	if j := strings.Index(recipe[1:], "\nicon:"); j >= 0 {
		recipe = recipe[:j+1]
	}
	if strings.Contains(recipe, "funnel") {
		t.Fatal("remote target must not use tailscale funnel")
	}
	if !strings.Contains(recipe, "brew install --cask tailscale") {
		t.Fatal("remote target must install Tailscale on macOS when missing")
	}
	if !strings.Contains(recipe, "tailscale.com/install.sh") {
		t.Fatal("remote target must install Tailscale on Linux when missing")
	}
	if !strings.Contains(recipe, "open -a Tailscale") {
		t.Fatal("remote target must start the Tailscale app")
	}
	if !strings.Contains(recipe, `"$$ts" up`) {
		t.Fatal("remote target must run tailscale up")
	}
	if !strings.Contains(recipe, "serve --bg") {
		t.Fatal("remote target must tailscale serve pane")
	}
	if !strings.Contains(recipe, `url="https://$$dns/"`) {
		t.Fatal("remote target must open the MagicDNS URL a phone browser uses")
	}
	if !strings.Contains(recipe, `open "$$url"`) {
		t.Fatal("remote target must pop the remote URL in a browser tab")
	}
	if strings.Contains(recipe, "-tailscale") {
		t.Fatal("remote target must start pane with default tailnet serve, not strict -tailscale")
	}
	if !strings.Contains(recipe, "exec ./$(BIN)") {
		t.Fatal("remote target must exec pane when :7420 is free")
	}
	if !strings.Contains(src, "-local") {
		t.Fatal("make run must pass -local so a local pane stays loopback-only")
	}
}

func TestMakefileIOSLaunchesSimulator(t *testing.T) {
	b, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "\nIOS_SIM")
	if i < 0 {
		i = strings.Index(src, "\nios:")
	}
	if i < 0 {
		t.Fatal("Makefile must have an ios target")
	}
	recipe := src[i:]
	if j := strings.Index(recipe[1:], "\nandroid:"); j >= 0 {
		recipe = recipe[:j+1]
	}
	if !strings.Contains(recipe, "open -a Simulator") {
		t.Fatal("ios must open Simulator.app")
	}
	if !strings.Contains(recipe, "simctl bootstatus") {
		t.Fatal("ios must boot the simulator and wait until it is ready")
	}
	if !strings.Contains(recipe, "simctl install") {
		t.Fatal("ios must install GrokPane on the simulator")
	}
	if !strings.Contains(recipe, "simctl launch --terminate-running-process") {
		t.Fatal("ios must launch GrokPane, not stop at xcodebuild build")
	}
	if !strings.Contains(recipe, "Debug-iphonesimulator/GrokPane.app") {
		t.Fatal("ios must install the built GrokPane.app")
	}
	if !strings.Contains(recipe, "SIMCTL_CHILD_PANE_URL=") {
		t.Fatal("ios must pass PANE_URL into the sim via SIMCTL_CHILD_")
	}
	if !strings.Contains(recipe, "http://127.0.0.1:7420") {
		t.Fatal("ios must load the host pane on loopback, not a connect prompt")
	}
	if !strings.Contains(recipe, "com.jgrant27.grokpane") {
		t.Fatal("ios must launch the GrokPane bundle id")
	}
	if !strings.Contains(recipe, "lsof -nP -iTCP:7420") {
		t.Fatal("ios must start pane when :7420 is free")
	}
	if !strings.Contains(recipe, "./$(BIN) -no-open") {
		t.Fatal("ios must start pane with -no-open when :7420 is free")
	}
	if !strings.Contains(recipe, "derivedDataPath") {
		t.Fatal("ios must pin derivedDataPath so install can find GrokPane.app")
	}
}

func TestTailscaleExe(t *testing.T) {
	oldPath, oldApp := lookPath, lookTailscaleApp
	t.Cleanup(func() { lookPath, lookTailscaleApp = oldPath, oldApp })

	lookPath = func(string) (string, error) { return "/usr/bin/tailscale", nil }
	lookTailscaleApp = func() string { return "/Applications/Tailscale.app/Contents/MacOS/Tailscale" }
	p, err := tailscaleExe()
	if err != nil || p != "/usr/bin/tailscale" {
		t.Fatalf("PATH wins: %s %v", p, err)
	}

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	p, err = tailscaleExe()
	if err != nil || p != "/Applications/Tailscale.app/Contents/MacOS/Tailscale" {
		t.Fatalf("app fallback: %s %v", p, err)
	}

	lookTailscaleApp = func() string { return "" }
	if _, err := tailscaleExe(); err == nil {
		t.Fatal("expected missing")
	}

	if p := oldApp(); p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(p, err)
		}
	}
}
