package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNormalizeOriginPicksSchemeByHost(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "http://127.0.0.1:7420"},
		{"192.168.1.5:7420", "http://192.168.1.5:7420"},
		{"10.0.0.9:7420", "http://10.0.0.9:7420"},
		{"172.16.4.4:7420", "http://172.16.4.4:7420"},
		{"172.32.4.4:7420", "https://172.32.4.4:7420"},
		{"100.101.102.103:7420", "http://100.101.102.103:7420"},
		{"169.254.7.7:7420", "http://169.254.7.7:7420"},
		{"127.0.0.1:7420", "http://127.0.0.1:7420"},
		{"[::1]:7420", "http://[::1]:7420"},
		{"localhost:7420", "http://localhost:7420"},
		{"beelz:7420", "http://beelz:7420"},
		{"beelz.local:7420", "http://beelz.local:7420"},
		{"beelz.tail1234.ts.net", "https://beelz.tail1234.ts.net"},
		{"pane.example.com/", "https://pane.example.com"},
		{"8.8.8.8:7420", "https://8.8.8.8:7420"},
		{"http://192.168.1.5:7420", "http://192.168.1.5:7420"},
		{"https://beelz.tail1234.ts.net", "https://beelz.tail1234.ts.net"},
	}
	for _, c := range cases {
		if got := normalizeOrigin(c.raw); got != c.want {
			t.Errorf("normalizeOrigin(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestOpenerArgsNeverUseAShell(t *testing.T) {
	// a URL whose query carries cmd.exe metacharacters must survive as one
	// argument on every platform.
	const u = "https://example.com/?x&calc.exe"
	for _, goos := range []string{"darwin", "windows", "linux"} {
		args := openerArgs(goos, u)
		if args[0] == "cmd" || args[0] == "sh" || args[0] == "bash" {
			t.Errorf("openerArgs(%q) routes through a shell: %v", goos, args)
		}
		if args[len(args)-1] != u {
			t.Errorf("openerArgs(%q) mangled the url: %v", goos, args)
		}
	}
	if got := openerArgs("windows", u); got[0] != "rundll32" || got[1] != "url.dll,FileProtocolHandler" {
		t.Errorf("openerArgs(windows) = %v, want the rundll32 protocol handler", got)
	}
}

func TestOpenableURLRejectsNonWebSchemes(t *testing.T) {
	bad := []string{
		"",
		"   ",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"vbscript:x",
		"ssh://box",
		"x-apple.systempreferences:root=Security",
		"http://",
		"not a url",
	}
	for _, raw := range bad {
		if got, ok := openableURL(raw); ok {
			t.Errorf("openableURL(%q) allowed %q", raw, got)
		}
	}
	if got, ok := openableURL(" https://example.com/a?b&c "); !ok || got != "https://example.com/a?b&c" {
		t.Errorf("openableURL trimmed-https = %q, %v", got, ok)
	}
}

func TestRevealTargetOnlyAcceptsDirectories(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := revealTarget(dir); !ok || got != dir {
		t.Errorf("revealTarget(dir) = %q, %v; want the dir", got, ok)
	}
	bad := []string{
		"",
		"   ",
		file,
		filepath.Join(dir, "missing"),
		"shell:Startup",
		"https://example.com",
		"ssh://box",
	}
	for _, path := range bad {
		if got, ok := revealTarget(path); ok {
			t.Errorf("revealTarget(%q) allowed %q", path, got)
		}
	}
}

func TestAdoptPaneReapsAChildThatExitsOnItsOwn(t *testing.T) {
	a := &App{}
	cmd := exec.Command("sh", "-c", "exit 3")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	a.adoptPane(cmd)
	select {
	case <-a.paneDone:
	case <-time.After(5 * time.Second):
		t.Fatal("adoptPane never reaped the child")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paneCmd != nil {
		t.Error("paneCmd still points at the exited child")
	}
	if a.startedIt {
		t.Error("startedIt still set after the child exited")
	}
}

// shutdown must stop the adopted child and return promptly, waiting on the
// reaper's channel rather than racing it for the exit status.
func TestShutdownStopsTheAdoptedChild(t *testing.T) {
	a := &App{}
	cmd := exec.Command("sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn a child here: %v", err)
	}
	a.adoptPane(cmd)
	done := make(chan struct{})
	go func() {
		a.shutdown(context.TODO())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("shutdown never returned")
	}
	select {
	case <-a.paneDone:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child outlived shutdown")
	}
}

// Only meaningful under -race: without the mutex these accessors tear.
func TestOriginAccessorsAreRaceFree(t *testing.T) {
	a := &App{origin: "http://127.0.0.1:7420"}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if i%2 == 0 {
					a.setOrigin("https://beelz.tail1234.ts.net", true)
				} else {
					_ = a.PaneOrigin()
					_ = a.IsRemote()
				}
			}
		}(i)
	}
	wg.Wait()
}
