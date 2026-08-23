package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

type App struct {
	ctx context.Context
	// mu guards the connection state below, which the menu goroutines, the
	// bound methods the webview calls and the shutdown hook all touch.
	mu         sync.Mutex
	origin     string
	remote     bool
	paneCmd    *exec.Cmd
	paneDone   chan struct{}
	startedIt  bool
	defaultCwd string
	// serverMu serializes ensureServer end to end: pane needs a moment to
	// bind, so without it two callers both fail the health check and each
	// start a server, and the loser dies with "already listening".
	serverMu sync.Mutex
	picking  atomic.Bool
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
	runtime.WindowSetSize(ctx, 1040, 680)
	setDockIcon(icon)
	runtime.EventsOn(ctx, "request-open-project", func(_ ...interface{}) {
		go a.OpenProject()
	})
	origin, remote := a.snapshot()
	if remote {
		if !healthy(origin) {
			runtime.LogError(ctx, "remote pane not reachable: "+origin)
		}
		return
	}
	if err := a.ensureServer(); err != nil {
		runtime.LogError(ctx, err.Error())
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	cmd, done, started := a.paneCmd, a.paneDone, a.startedIt
	a.mu.Unlock()
	if !started || cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	// the reaper goroutine owns Wait, so wait on its channel rather than
	// calling Process.Wait here — two waiters race and one loses the status.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = cmd.Process.Kill()
	}
}

// snapshot reads the connection state under the lock so callers work from a
// consistent pair rather than two separately-torn reads.
func (a *App) snapshot() (origin string, remote bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.origin, a.remote
}

func (a *App) setOrigin(origin string, remote bool) {
	a.mu.Lock()
	a.origin = origin
	a.remote = remote
	a.mu.Unlock()
}

func (a *App) PaneOrigin() string {
	origin, _ := a.snapshot()
	return origin
}

func (a *App) IsRemote() bool {
	_, remote := a.snapshot()
	return remote
}

func (a *App) SetPaneOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.EqualFold(raw, "local") {
		const local = "http://127.0.0.1:7420"
		a.setOrigin(local, false)
		_ = os.Remove(paneURLFile())
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "pane-origin", local)
		}
		go func() {
			if err := a.ensureServer(); err != nil && a.ctx != nil {
				runtime.LogError(a.ctx, err.Error())
			}
		}()
		return local
	}
	origin := normalizeOrigin(raw)
	if !healthy(origin) {
		return "error: not reachable: " + origin
	}
	a.setOrigin(origin, !localOrigin(origin))
	_ = writePaneURL(origin)
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "pane-origin", origin)
	}
	return origin
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
	if _, remote := a.snapshot(); remote {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "request-remote-cwd")
		}
		return ""
	}
	path, err := pickFolder("Open project", a.cwd())
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
	a.mu.Lock()
	a.defaultCwd = path
	a.mu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "project", path)
	}
	return path
}

func (a *App) cwd() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.defaultCwd
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

func (a *App) PrevSession() { runtime.EventsEmit(a.ctx, "prev-session") }
func (a *App) NextSession() { runtime.EventsEmit(a.ctx, "next-session") }
func (a *App) PrevProject() { runtime.EventsEmit(a.ctx, "prev-project") }
func (a *App) NextProject() { runtime.EventsEmit(a.ctx, "next-project") }

func (a *App) CopyText(s string) {
	if s == "" {
		return
	}
	_ = runtime.ClipboardSetText(a.ctx, s)
}

// PaneToken hands the UI the local server's token. The desktop app serves
// its own page, so it cannot be given the token the way a browser is — but
// it does run on the same machine as a local pane, so it can read it.
// A remote pane's token lives on that machine; there the tailnet identity
// is the credential, and this is deliberately empty.
func (a *App) PaneToken() string {
	origin, remote := a.snapshot()
	if remote || !localOrigin(origin) {
		return ""
	}
	if v := strings.TrimSpace(os.Getenv("PANE_TOKEN")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "pane.token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (a *App) OpenURL(raw string) {
	target, ok := openableURL(raw)
	if !ok {
		return
	}
	// prefer Wails, which reaches the browser through ShellExecute and never
	// builds a command line a shell could reinterpret.
	if a.ctx != nil {
		runtime.BrowserOpenURL(a.ctx, target)
		return
	}
	launch(openerArgs(goruntime.GOOS, target))
}

// openableURL keeps OpenURL to the two schemes the UI ever links to. It is
// reachable from page script as window.go.main.App.OpenURL, so the scheme
// check is a trust boundary, not a convenience.
func openableURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return u.String(), true
}

// openerArgs is the shell-free way to hand a URL to the system handler on each
// platform. Windows must not go through `cmd /c start`: Go leaves an argument
// with no spaces or quotes unescaped, so cmd.exe splits the line at an & or |
// in the query string and runs whatever follows as a second command.
func openerArgs(goos, target string) []string {
	switch goos {
	case "darwin":
		return []string{"open", target}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", target}
	default:
		return []string{"xdg-open", target}
	}
}

func launch(args []string) {
	cmd := exec.Command(args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		return
	}
	// nothing ever waits on the opener, so release the handle rather than
	// leaving a zombie behind for the life of the app.
	_ = cmd.Process.Release()
}

func (a *App) Reveal(path string) {
	if path == "" {
		path = a.cwd()
	}
	dir, ok := revealTarget(path)
	if !ok {
		return
	}
	var args []string
	switch goruntime.GOOS {
	case "darwin":
		// -R selects the folder in Finder instead of opening it; a .app
		// bundle is a directory, so plain `open` would run it.
		args = []string{"open", "-R", dir}
	case "windows":
		args = []string{"explorer", dir}
	default:
		args = []string{"xdg-open", dir}
	}
	launch(args)
}

// revealTarget only lets through a path that really is a directory on this
// machine. Reveal is bound into the webview, and the platform openers happily
// launch what is handed to them — URL schemes, shell: URIs, .desktop files —
// so anything that is not a plain directory is refused, mirroring OpenProject.
func revealTarget(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return "", false
	}
	return abs, true
}

func (a *App) ensureServer() error {
	a.serverMu.Lock()
	defer a.serverMu.Unlock()
	origin, _ := a.snapshot()
	if healthy(origin) {
		return nil
	}
	bin := findPane()
	if bin == "" {
		return fmt.Errorf("pane server is not running on %s and no pane binary was found — start `pane` first", origin)
	}
	cmd := exec.Command(bin, "-no-open")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start pane: %w", err)
	}
	a.adoptPane(cmd)
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if healthy(origin) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("pane started but %s never became healthy", origin)
}

// adoptPane records the server this app started and reaps it when it exits.
// Without the reaper a child that died on its own — say because another pane
// already holds the port — stays a zombie with a live PID, and shutdown then
// signals the corpse while the real server keeps serving.
func (a *App) adoptPane(cmd *exec.Cmd) {
	done := make(chan struct{})
	a.mu.Lock()
	a.paneCmd = cmd
	a.paneDone = done
	a.startedIt = true
	a.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		a.mu.Lock()
		if a.paneCmd == cmd {
			a.paneCmd = nil
			a.startedIt = false
		}
		a.mu.Unlock()
		close(done)
	}()
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
		raw = defaultScheme(raw) + "://" + raw
	}
	return raw
}

// defaultScheme picks the scheme for an entry typed without one. pane itself
// only ever serves cleartext; https is right only when something in front of
// it terminates TLS, which in practice means a tailscale-serve hostname. A
// loopback, RFC1918, CGNAT or link-local literal, or a name with no dot in it,
// is being reached directly, so https there is just a guaranteed TLS error.
func defaultScheme(raw string) string {
	host := raw
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "http"
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || isCGNAT(ip) {
			return "http"
		}
		return "https"
	}
	if !strings.Contains(host, ".") {
		return "http"
	}
	return "https"
}

// isCGNAT reports 100.64.0.0/10, which is the range tailscale hands out. A
// bare tailnet IP reaches pane directly, unlike the MagicDNS name that
// tailscale serve fronts with TLS.
func isCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func localOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return true
	}
	h := strings.ToLower(u.Hostname())
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}
