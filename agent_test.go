package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbeAgentSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != "ok-secret" {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")

	if err := probeAgent(ws, "ok-secret"); err != nil {
		t.Fatalf("matching secret: %v", err)
	}
	err := probeAgent(ws, "wrong")
	if err == nil {
		t.Fatal("wrong secret: expected error")
	}
	if !strings.Contains(err.Error(), "rejected this secret") {
		t.Fatalf("wrong secret: %v", err)
	}
	if !strings.Contains(err.Error(), "make agent-restart") {
		t.Fatalf("want restart hint: %v", err)
	}
}

func TestRedactSecret(t *testing.T) {
	in := "grok agent --debug serve --bind 127.0.0.1:2419 --secret pane-dev-secret --debug-file /tmp/grok-serve.log"
	got := redactSecret(in)
	if strings.Contains(got, "pane-dev-secret") {
		t.Fatalf("leaked secret: %s", got)
	}
	if !strings.Contains(got, "--secret ***") {
		t.Fatalf("got %s", got)
	}
}

func TestAgentWSBase(t *testing.T) {
	if got := agentWSBase("127.0.0.1:2419"); got != "ws://127.0.0.1:2419" {
		t.Fatal(got)
	}
}

func TestRedactSecretEquals(t *testing.T) {
	if !strings.Contains(redactSecret("grok --secret=pane-dev-secret"), "--secret=***") {
		t.Fatal(redactSecret("grok --secret=pane-dev-secret"))
	}
}

func TestServeAgentPaths(t *testing.T) {
	oldPath, oldRun := lookPath, runGrokServe
	t.Cleanup(func() { lookPath, runGrokServe = oldPath, oldRun })

	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if err := serveAgent("127.0.0.1:1", "s", false); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("%v", err)
	}

	lookPath = func(string) (string, error) { return "/bin/grok", nil }
	secret := "match-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	u := strings.TrimPrefix(srv.URL, "http://")
	// httptest is not on 127.0.0.1:2419; serveAgent probes agentWSBase(bind).
	// Occupy a real port with a websocket agent.
	ln := startAgentListener(t, secret)
	bind := ln.Addr().String()
	if err := serveAgent(bind, secret, false); err != nil {
		t.Fatal(err)
	}
	if err := serveAgent(bind, "wrong", false); err == nil {
		t.Fatal("expected secret mismatch")
	}

	started := false
	runGrokServe = func(bind, secret string) error {
		started = true
		return nil
	}
	free := freeAddr(t)
	if err := serveAgent(free, "s", false); err != nil || !started {
		t.Fatalf("start %v started=%v", err, started)
	}
	_ = u
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	t.Fatal("go.mod not found")
	return ""
}

func TestListenerInfoAndKill(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	pid, cmd := listenerInfo(ln.Addr().String())
	if pid == "" {
		t.Fatalf("expected listener pid, cmd=%q", cmd)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// Same port, an address nothing is bound to: matching on the port alone
	// would hand back the pid above and get it killed.
	if p, _ := listenerInfo("127.0.0.2:" + port); p != "" {
		t.Fatalf("foreign host matched by port: pid %s", p)
	}
	// A hostless bind is not an address; it used to fall back to pane's own
	// UI port and kill the pane server.
	if p, _ := listenerInfo(port); p != "" {
		t.Fatalf("hostless bind matched: pid %s", p)
	}
	if err := killListener(port); err == nil {
		t.Fatal("hostless bind must be refused, not guessed at")
	}
	if err := killListener(freeAddr(t)); err != nil {
		t.Fatal(err)
	}

	root := moduleRoot(t)
	dir := t.TempDir()
	helper := filepath.Join(dir, "listen")
	build := exec.Command("go", "build", "-o", helper, "./cmd/listen")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build listen: %v\n%s", err, out)
	}
	addr := freeAddr(t)
	proc := startListener(t, helper, addr)
	if err := killListener(addr); err == nil {
		t.Fatal("a stranger's process on the bind must not be killed")
	}
	if !tcpBusy(addr) {
		t.Fatal("killed a process that is not a grok agent")
	}
	_ = proc.Process.Kill()
	_ = proc.Wait()

	// The same helper under a command line that reads as the grok agent — the
	// one process pane may replace. cmd/listen takes the address first and
	// ignores the rest.
	agent := filepath.Join(dir, "grok")
	if err := os.Rename(helper, agent); err != nil {
		t.Fatal(err)
	}
	addr = freeAddr(t)
	startListener(t, agent, addr, "agent", "serve")
	if err := killListener(addr); err != nil {
		t.Fatal(err)
	}
	if tcpBusy(addr) {
		t.Fatal("still listening after kill")
	}
}

func startListener(t *testing.T, bin, addr string, args ...string) *exec.Cmd {
	t.Helper()
	proc := exec.Command(bin, append([]string{addr}, args...)...)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
			_ = proc.Wait()
		}
	})
	if err := waitTCP(addr, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	return proc
}

func TestIsGrokAgent(t *testing.T) {
	ok := []string{
		"/opt/homebrew/bin/grok agent serve --bind 127.0.0.1:2419 --secret s",
		"grok --debug agent serve --bind 127.0.0.1:2419",
	}
	for _, cmd := range ok {
		if !isGrokAgent(cmd) {
			t.Fatalf("want grok agent: %s", cmd)
		}
	}
	no := []string{
		"",
		"/usr/sbin/httpd -D FOREGROUND -f /etc/apache2/httpd.conf",
		"/usr/local/bin/grok",
		"grok sessions list --json",
		"/usr/bin/notgrok agent serve --bind 127.0.0.1:2419",
	}
	for _, cmd := range no {
		if isGrokAgent(cmd) {
			t.Fatalf("must not pass as grok agent: %s", cmd)
		}
	}
}

func TestSameBindHost(t *testing.T) {
	cases := []struct {
		host, sock string
		want       bool
	}{
		{"127.0.0.1", "127.0.0.1:2419", true},
		{"127.0.0.1", "127.0.0.2:2419", false},
		{"127.0.0.1", "*:2419", true},
		{"0.0.0.0", "127.0.0.1:2419", true},
		{"::1", "[::1]:2419", true},
		{"::1", "[fe80::1]:2419", false},
		{"box.local", "box.local:2419", true},
		{"127.0.0.1", "nonsense", false},
	}
	for _, c := range cases {
		if got := sameBindHost(c.host, c.sock); got != c.want {
			t.Fatalf("sameBindHost(%q, %q) = %v", c.host, c.sock, got)
		}
	}
}

func startAgentListener(t *testing.T, secret string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close()
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln
}
