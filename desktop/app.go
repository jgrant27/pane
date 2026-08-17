package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx        context.Context
	origin     string
	paneCmd    *exec.Cmd
	startedIt  bool
	defaultCwd string
}

func NewApp() *App {
	return &App{origin: "http://127.0.0.1:7420"}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	setDockIcon(icon)
	runtime.EventsOn(ctx, "request-open-project", func(_ ...interface{}) {
		go a.OpenProject()
	})
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

func (a *App) OpenProject() string {
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
	c := &http.Client{Timeout: 400 * time.Millisecond}
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
