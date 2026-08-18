package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx        context.Context
	origin     string
	remote     bool
	paneCmd    *exec.Cmd
	startedIt  bool
	defaultCwd string
	picking    atomic.Bool
}

func NewApp() *App {
	origin := strings.TrimSpace(os.Getenv("PANE_URL"))
	if origin == "" {
		origin = readPaneURL()
	}
	if origin == "" {
		origin = "http://127.0.0.1:7420"
	}
	origin = normalizeOrigin(origin)
	return &App{origin: origin, remote: !localOrigin(origin)}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	setDockIcon(icon)
	runtime.EventsOn(ctx, "request-open-project", func(_ ...interface{}) {
		go a.OpenProject()
	})
	if a.remote {
		if !healthy(a.origin) {
			runtime.LogError(ctx, "remote pane not reachable: "+a.origin)
		}
		return
	}
	if err := a.ensureServer(); err != nil {
		runtime.LogError(ctx, err.Error())
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.startedIt && a.paneCmd != nil && a.paneCmd.Process != nil {
		_ = a.paneCmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_, _ = a.paneCmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = a.paneCmd.Process.Kill()
		}
	}
}

func (a *App) PaneOrigin() string { return a.origin }

func (a *App) IsRemote() bool { return a.remote }

func (a *App) SetPaneOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "local") {
		a.origin = "http://127.0.0.1:7420"
		a.remote = false
		_ = os.Remove(paneURLFile())
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "pane-origin", a.origin)
		}
		go func() {
			if err := a.ensureServer(); err != nil && a.ctx != nil {
				runtime.LogError(a.ctx, err.Error())
			}
		}()
		return a.origin
	}
	origin := normalizeOrigin(raw)
	if !healthy(origin) {
		return "error: not reachable: " + origin
	}
	a.origin = origin
	a.remote = !localOrigin(origin)
	_ = writePaneURL(origin)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "pane-origin", a.origin)
	}
	return a.origin
}

func (a *App) beginPick() bool {
	return a.picking.CompareAndSwap(false, true)
}

func (a *App) endPick() {
	a.picking.Store(false)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "picker-done")
	}
}

func (a *App) OpenProject() string {
	if !a.beginPick() {
		return ""
	}
	defer a.endPick()
	if a.remote {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "request-remote-cwd")
		}
		return ""
	}
	path, err := pickFolder("Open project", a.defaultCwd)
	if err != nil {
		if a.ctx != nil {
			runtime.LogError(a.ctx, "open project: "+err.Error())
		}
		return ""
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return ""
	}
	a.defaultCwd = path
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "project", path)
	}
	return path
}

func (a *App) PickFiles() []string {
	if !a.beginPick() {
		return []string{}
	}
	defer a.endPick()
	paths, err := pickFiles("Attach files or images")
	if err != nil || len(paths) == 0 {
		return []string{}
	}
	return paths
}

func (a *App) NewSession() {
	runtime.EventsEmit(a.ctx, "new-session")
}

func (a *App) CloseSession() {
	runtime.EventsEmit(a.ctx, "close-session")
}

func (a *App) CopyText(s string) {
	if s == "" {
		return
	}
	_ = runtime.ClipboardSetText(a.ctx, s)
}

func (a *App) OpenURL(raw string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return
	}
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u.String())
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", u.String())
	default:
		cmd = exec.Command("xdg-open", u.String())
	}
	_ = cmd.Start()
}

func (a *App) Reveal(path string) {
	if path == "" {
		path = a.defaultCwd
	}
	if path == "" {
		return
	}
	var cmd *exec.Cmd
	switch goruntime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("explorer", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	_ = cmd.Start()
}

func (a *App) ensureServer() error {
	if healthy(a.origin) {
		return nil
	}
	bin := findPane()
	if bin == "" {
		return fmt.Errorf("pane server is not running on %s and no pane binary was found — start `pane` first", a.origin)
	}
	cmd := exec.Command(bin, "-no-open")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pane: %w", err)
	}
	a.paneCmd = cmd
	a.startedIt = true
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(a.origin) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("pane started but %s never became healthy", a.origin)
}

func healthy(origin string) bool {
	wait := 400 * time.Millisecond
	if !localOrigin(origin) {
		wait = 2500 * time.Millisecond
	}
	c := &http.Client{Timeout: wait}
	res, err := c.Get(origin + "/healthz")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, res.Body)
	return res.StatusCode == 200
}

func findPane() string {
	var cands []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(dir, "pane"),
			filepath.Join(dir, "..", "Resources", "pane"),
			filepath.Join(dir, "..", "..", "pane"),
			filepath.Join(dir, "..", "..", "..", "pane"),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		for i := 0; i < 6; i++ {
			cands = append(cands, filepath.Join(wd, "pane"))
			parent := filepath.Dir(wd)
			if parent == wd {
				break
			}
			wd = parent
		}
	}
	if p, err := exec.LookPath("pane"); err == nil {
		cands = append(cands, p)
	}
	for _, c := range cands {
		st, err := os.Stat(c)
		if err == nil && !st.IsDir() {
			abs, _ := filepath.Abs(c)
			return abs
		}
	}
	return ""
}

func paneURLFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".grok", "pane-url")
}

func readPaneURL() string {
	p := paneURLFile()
	if p == "" {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func writePaneURL(origin string) error {
	p := paneURLFile()
	if p == "" {
		return fmt.Errorf("no home")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(origin+"\n"), 0o600)
}

func normalizeOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimRight(raw, "/")
	if raw == "" {
		return "http://127.0.0.1:7420"
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return raw
}

func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	h := strings.ToLower(u.Hostname())
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}
