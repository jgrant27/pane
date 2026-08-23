package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func TestParsePaneArgs(t *testing.T) {
	cfg, err := parsePaneArgs([]string{"-no-open", "-no-agent", "-cwd", t.TempDir(), "-listen", "127.0.0.1:9"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.noOpen || !cfg.noAgent || cfg.listen != "127.0.0.1:9" || cfg.tailscale || cfg.local {
		t.Fatalf("%+v", cfg)
	}
	cfg, err = parsePaneArgs([]string{"-local"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.local || cfg.tailscale {
		t.Fatalf("%+v", cfg)
	}
	if _, err := parsePaneArgs([]string{"-not-a-flag"}); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := parsePaneArgs([]string{"-h"}); err == nil {
		t.Fatal("expected help error")
	}
}

func TestRunLocalAndTailscaleConflict(t *testing.T) {
	if err := run([]string{"-local", "-tailscale", "-no-agent", "-no-open"}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("%v", err)
	}
}

func TestRunReplaceRequiresServe(t *testing.T) {
	if err := run([]string{"-replace-agent"}); err == nil || !strings.Contains(err.Error(), "-serve-agent") {
		t.Fatalf("%v", err)
	}
}

func TestServePaneHealthzAndMeta(t *testing.T) {
	dir := t.TempDir()
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:  addr,
			agent:   "ws://127.0.0.1:1",
			cwd:     dir,
			secret:  "test-secret",
			noAgent: true,
			noOpen:  true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(body), "ok") {
		t.Fatalf("%d %s", res.StatusCode, body)
	}
	res, err = http.Get("http://" + addr + "/meta")
	if err != nil {
		t.Fatal(err)
	}
	var meta map[string]string
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if meta["name"] != "Grok Pane" {
		t.Fatalf("%+v", meta)
	}
	opt, err := http.NewRequest(http.MethodOptions, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	ores, err := http.DefaultClient.Do(opt)
	if err != nil {
		t.Fatal(err)
	}
	_ = ores.Body.Close()
	if ores.StatusCode != http.StatusNoContent {
		t.Fatalf("cors %d", ores.StatusCode)
	}
	close(stop)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("servePane did not stop")
	}
}

func TestServePaneBusyListen(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	err = servePane(paneCfg{
		listen:  ln.Addr().String(),
		cwd:     t.TempDir(),
		secret:  "x",
		noAgent: true,
		noOpen:  true,
	}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Fatalf("%v", err)
	}
}

func TestServePaneBadCwd(t *testing.T) {
	err := servePane(paneCfg{cwd: filepath.Join(t.TempDir(), "missing"), noAgent: true, noOpen: true, listen: "127.0.0.1:0"}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "cwd") {
		t.Fatalf("%v", err)
	}
}

func TestServePaneGrokMissing(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { lookPath = old })
	err := servePane(paneCfg{
		listen:    freeAddr(t),
		agentBind: freeAddr(t),
		cwd:       t.TempDir(),
		secret:    "x",
		noOpen:    true,
	}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "grok not on PATH") {
		t.Fatalf("%v", err)
	}
}

func TestServePaneOpensBrowser(t *testing.T) {
	var got string
	old := openBrowser
	openBrowser = func(u string) { got = u }
	t.Cleanup(func() { openBrowser = old })
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{listen: addr, cwd: t.TempDir(), secret: "x", noAgent: true}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	<-errc
	if got != "http://"+addr {
		t.Fatalf("opened %q", got)
	}
}

func TestServePaneAutoTailnetServesLoopback(t *testing.T) {
	oldAuto := autoTailnet
	autoTailnet = true
	var ran [][]string
	var got string
	oldOpen, oldPath, oldRun, oldJSON := openBrowser, lookPath, tailscaleRun, tailscaleJSON
	openBrowser = func(u string) { got = u }
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error {
		ran = append(ran, append([]string{}, args...))
		return nil
	}
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"BackendState":"Running","Self":{"DNSName":"beelz.beluga-hydra.ts.net."}}`), nil
	}
	t.Cleanup(func() {
		autoTailnet = oldAuto
		openBrowser, lookPath, tailscaleRun, tailscaleJSON = oldOpen, oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:  addr,
			cwd:     t.TempDir(),
			secret:  "x",
			noAgent: true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("loopback %d", res.StatusCode)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got != "https://beelz.beluga-hydra.ts.net/" {
		t.Fatalf("remote URL %q", got)
	}
	if len(ran) < 2 || ran[0][0] != "serve" || ran[len(ran)-1][0] != "serve" {
		t.Fatalf("tailscale cmds %+v", ran)
	}
}

func TestServePaneAutoTailnetSkippedWhenDown(t *testing.T) {
	oldAuto := autoTailnet
	autoTailnet = true
	var ran int
	oldPath, oldRun, oldJSON := lookPath, tailscaleRun, tailscaleJSON
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error {
		ran++
		return nil
	}
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"BackendState":"Stopped"}`), nil
	}
	t.Cleanup(func() {
		autoTailnet = oldAuto
		lookPath, tailscaleRun, tailscaleJSON = oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:  addr,
			cwd:     t.TempDir(),
			secret:  "x",
			noAgent: true,
			noOpen:  true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatalf("served while Stopped: %d", ran)
	}
}

func TestServePaneLocalSkipsAutoTailnet(t *testing.T) {
	oldAuto := autoTailnet
	autoTailnet = true
	var ran int
	oldPath, oldRun, oldJSON := lookPath, tailscaleRun, tailscaleJSON
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error {
		ran++
		return nil
	}
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"BackendState":"Running","Self":{"DNSName":"h.ts.net."}}`), nil
	}
	t.Cleanup(func() {
		autoTailnet = oldAuto
		lookPath, tailscaleRun, tailscaleJSON = oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:  addr,
			cwd:     t.TempDir(),
			secret:  "x",
			noAgent: true,
			noOpen:  true,
			local:   true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatalf("local served: %d", ran)
	}
}

func TestServePaneAutoTailnetServeErrorKeepsLocal(t *testing.T) {
	oldAuto := autoTailnet
	autoTailnet = true
	oldPath, oldRun, oldJSON := lookPath, tailscaleRun, tailscaleJSON
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error { return errors.New("serve down") }
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"BackendState":"Running"}`), nil
	}
	t.Cleanup(func() {
		autoTailnet = oldAuto
		lookPath, tailscaleRun, tailscaleJSON = oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:  addr,
			cwd:     t.TempDir(),
			secret:  "x",
			noAgent: true,
			noOpen:  true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("loopback %d", res.StatusCode)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestTailscaleRunning(t *testing.T) {
	old := tailscaleJSON
	t.Cleanup(func() { tailscaleJSON = old })
	tailscaleJSON = func() ([]byte, error) { return nil, errors.New("no") }
	if tailscaleRunning() {
		t.Fatal("err")
	}
	tailscaleJSON = func() ([]byte, error) { return []byte(`not-json`), nil }
	if tailscaleRunning() {
		t.Fatal("junk")
	}
	tailscaleJSON = func() ([]byte, error) { return []byte(`{"BackendState":"Running"}`), nil }
	if !tailscaleRunning() {
		t.Fatal("running")
	}
	tailscaleJSON = func() ([]byte, error) { return []byte(`{"BackendState":"Stopped"}`), nil }
	if tailscaleRunning() {
		t.Fatal("stopped")
	}
}

func TestServePaneTailscaleOpensPhoneURL(t *testing.T) {
	var got string
	oldOpen, oldPath, oldRun, oldJSON := openBrowser, lookPath, tailscaleRun, tailscaleJSON
	openBrowser = func(u string) { got = u }
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error { return nil }
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"beelz.beluga-hydra.ts.net."}}`), nil
	}
	t.Cleanup(func() {
		openBrowser, lookPath, tailscaleRun, tailscaleJSON = oldOpen, oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:    addr,
			cwd:       t.TempDir(),
			secret:    "x",
			noAgent:   true,
			tailscale: true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got != "https://beelz.beluga-hydra.ts.net/" {
		t.Fatalf("phone URL %q", got)
	}
}

func TestServePaneTailscale(t *testing.T) {
	oldPath, oldRun, oldJSON := lookPath, tailscaleRun, tailscaleJSON
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	var ran [][]string
	tailscaleRun = func(args ...string) error {
		ran = append(ran, append([]string{}, args...))
		return nil
	}
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{"Self":{"DNSName":"box.tailnet.ts.net."}}`), nil
	}
	t.Cleanup(func() {
		lookPath, tailscaleRun, tailscaleJSON = oldPath, oldRun, oldJSON
	})
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:    addr,
			cwd:       t.TempDir(),
			secret:    "x",
			noAgent:   true,
			noOpen:    true,
			tailscale: true,
		}, stop)
	}()
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("loopback %d", res.StatusCode)
	}
	req.Header.Set("Tailscale-User-Login", "jgrant@")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("authed %d", res.StatusCode)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if len(ran) < 2 || ran[0][0] != "serve" || ran[len(ran)-1][0] != "serve" {
		t.Fatalf("tailscale cmds %+v", ran)
	}
}

func TestHelpers(t *testing.T) {
	if env("PANE_TEST_MISSING", "fb") != "fb" {
		t.Fatal("fallback")
	}
	t.Setenv("PANE_TEST_MISSING", "set")
	if env("PANE_TEST_MISSING", "fb") != "set" {
		t.Fatal("env")
	}
	if listenPort("127.0.0.1:9") != "9" || listenPort("bad") != "7420" {
		t.Fatal("listenPort")
	}
	if !tcpBusy("127.0.0.1:1") && tcpBusy(freeAddr(t)) {
		// 127.0.0.1:1 may or may not be busy; just exercise tcpBusy false path
	}
	addr := freeAddr(t)
	if tcpBusy(addr) {
		t.Fatal("expected free")
	}
	if err := waitTCP("127.0.0.1:1", 150*time.Millisecond); err == nil {
		t.Fatal("waitTCP should timeout")
	}
	s, err := resolveSecret("given")
	if err != nil || s != "given" {
		t.Fatal(s, err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	// resolveSecret uses UserHomeDir which is not HOME on all platforms the same...
	// UserHomeDir on mac ignores HOME sometimes? On Unix it uses HOME.
	got, err := resolveSecret("")
	if err != nil || len(got) < 8 {
		t.Fatal(got, err)
	}
	again, err := resolveSecret("")
	if err != nil || again != got {
		t.Fatalf("reread %s vs %s", again, got)
	}
	old := tailscaleJSON
	tailscaleJSON = func() ([]byte, error) { return nil, errors.New("no") }
	if tailscaleDNS() != "" {
		t.Fatal("dns on error")
	}
	tailscaleJSON = func() ([]byte, error) { return []byte(`not-json`), nil }
	if tailscaleDNS() != "" {
		t.Fatal("dns on bad json")
	}
	tailscaleJSON = func() ([]byte, error) { return []byte(`{"Self":{"DNSName":"h.ts.net."}}`), nil }
	if tailscaleDNS() != "h.ts.net" {
		t.Fatal(tailscaleDNS())
	}
	tailscaleJSON = old

	h := noStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rec.Header())
	}
}

func TestOpenURL(t *testing.T) {
	old := openCmd
	t.Cleanup(func() { openCmd = old })
	openCmd = func(string) *exec.Cmd { return exec.Command("true") }
	openURL("http://example.invalid")
	openCmd = func(string) *exec.Cmd { return exec.Command("/no/such/open-binary") }
	openURL("http://example.invalid")
}
