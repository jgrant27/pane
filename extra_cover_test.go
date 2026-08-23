package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRunServeAgent(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("missing") }
	t.Cleanup(func() { lookPath = old })
	if err := run([]string{"-serve-agent", "-secret", "x"}); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("%v", err)
	}
	lookPath = func(string) (string, error) { return "/bin/grok", nil }
	ln := startAgentListener(t, "run-secret")
	if err := run([]string{"-serve-agent", "-secret", "run-secret", "-agent-bind", ln.Addr().String()}); err != nil {
		t.Fatal(err)
	}
}

func TestServePaneReuseAndStartGrok(t *testing.T) {
	secret := "reuse-secret"
	ln := startAgentListener(t, secret)
	bind := ln.Addr().String()
	stop := make(chan struct{})
	errc := make(chan error, 1)
	listen := freeAddr(t)
	go func() {
		errc <- servePane(paneCfg{
			listen:    listen,
			agent:     agentWSBase(bind),
			agentBind: bind,
			cwd:       t.TempDir(),
			secret:    secret,
			noOpen:    true,
		}, stop)
	}()
	if err := waitTCP(listen, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	// probe fail on busy bind
	stop = make(chan struct{})
	errc = make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:    freeAddr(t),
			agent:     agentWSBase(bind),
			agentBind: bind,
			cwd:       t.TempDir(),
			secret:    "wrong",
			noOpen:    true,
		}, stop)
	}()
	time.Sleep(150 * time.Millisecond)
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	oldStart, oldWait, oldPath := startGrok, grokReadyFor, lookPath
	lookPath = func(string) (string, error) { return "/bin/grok", nil }
	grokReadyFor = 80 * time.Millisecond
	startGrok = func(bind, secret string) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "1")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
	t.Cleanup(func() { startGrok, grokReadyFor, lookPath = oldStart, oldWait, oldPath })
	err := servePane(paneCfg{
		listen:    freeAddr(t),
		agentBind: freeAddr(t),
		cwd:       t.TempDir(),
		secret:    "x",
		noOpen:    true,
	}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "grok agent serve") {
		t.Fatalf("%v", err)
	}
	grokReadyFor = 2 * time.Second

	started := make(chan string, 1)
	startGrok = func(bind, secret string) (*exec.Cmd, error) {
		ln, err := net.Listen("tcp", bind)
		if err != nil {
			return nil, err
		}
		go func() { time.Sleep(200 * time.Millisecond); _ = ln.Close() }()
		cmd := exec.Command("sleep", "2")
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		started <- bind
		return cmd, nil
	}
	addr := freeAddr(t)
	agentBind := freeAddr(t)
	stop = make(chan struct{})
	errc = make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{
			listen:    addr,
			agentBind: agentBind,
			cwd:       t.TempDir(),
			secret:    "x",
			noOpen:    true,
		}, stop)
	}()
	select {
	case <-started:
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("startGrok not called")
	}
	if err := waitTCP(addr, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestHandleWSReplay(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	dir := t.TempDir()
	sid := "01replayxxxxxxxxxxxxxxxxxxxxx"
	sdir := filepath.Join(sessionGroupDir(dir), sid)
	if err := os.MkdirAll(sdir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"old"}}}}` + "\n"
	line += `{"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"reply"}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(sdir, "updates.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := "replay-secret"
	agent := startMockAgent(t, secret)
	p := &proxy{agentBase: agent, secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + dir + "&sid=" + sid + "&replay=1"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(4 * time.Second))
	var sawReady bool
	for i := 0; i < 8; i++ {
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			break
		}
		if msg["type"] == "ready" {
			sawReady = true
			break
		}
	}
	if !sawReady {
		t.Fatal("no ready after replay")
	}
}

func TestMoreUploadAndPrompt(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	if err := os.WriteFile(src, []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, filepath.Join(dir, "b.bin")); err != nil {
		t.Fatal(err)
	}
	if detectMIME("x.png", nil) == "" {
		t.Fatal("png")
	}
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, make([]byte, maxUpload+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := attachPath(dir, big); err == nil {
		t.Fatal("oversize")
	}
	blocks := buildPrompt("", []promptFile{{Path: src, Name: "a.bin", Mime: "application/octet-stream", Size: 4}}, dir, false)
	if len(blocks) < 2 {
		t.Fatalf("%+v", blocks)
	}
	blocks = buildPrompt("", []promptFile{
		{Path: src, Name: "a.bin", Mime: "application/octet-stream", Size: 4},
		{Path: src, Name: "a.bin", Mime: "application/octet-stream", Size: 4},
	}, dir, false)
	if blocks[0]["text"] != "See attached files." {
		t.Fatalf("%+v", blocks)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir+"&path="+src, nil)
	rec := httptest.NewRecorder()
	handleUpload(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
}

func TestSessionsPruneAndGrokHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	stub := "01aaaaaaaaaaaaaaaaaaaaaaaa"
	real := "01bbbbbbbbbbbbbbbbbbbbbbbb"
	for _, id := range []string{stub, real} {
		d := filepath.Join(sessionGroupDir(cwd), id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(sessionGroupDir(cwd), stub, "summary.json"), []byte(`{"info":{"id":"`+stub+`"},"generated_title":"","num_messages":0}`), 0o644)
	_ = os.WriteFile(filepath.Join(sessionGroupDir(cwd), real, "summary.json"), []byte(`{"info":{"id":"`+real+`"},"generated_title":"Real","num_messages":3}`), 0o644)
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions?cwd="+cwd+"&prune=1&keep="+real, nil)
	rec := httptest.NewRecorder()
	handleSessions(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/v1/sessions", nil)
	rec = httptest.NewRecorder()
	handleSessions(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	t.Setenv("GROK_HOME", "")
	if !strings.Contains(grokHome(), ".grok") {
		t.Fatal(grokHome())
	}
	if errTimeout.Error() == "" {
		t.Fatal("timeout err")
	}
}

func TestDialAgentRefusedAndHTTPError(t *testing.T) {
	err := probeAgent("ws://127.0.0.1:1", "x")
	if err == nil {
		t.Fatal("expected refuse")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	err = probeAgent("ws"+strings.TrimPrefix(srv.URL, "http"), "x")
	if err == nil {
		t.Fatal("expected http error")
	}
}

func TestTailscaleMissingAndServeAgentStart(t *testing.T) {
	old, oldApp := lookPath, lookTailscaleApp
	lookPath = func(string) (string, error) { return "", errors.New("no") }
	lookTailscaleApp = func() string { return "" }
	t.Cleanup(func() { lookPath, lookTailscaleApp = old, oldApp })
	err := servePane(paneCfg{
		listen:    freeAddr(t),
		cwd:       t.TempDir(),
		secret:    "x",
		noAgent:   true,
		noOpen:    true,
		tailscale: true,
	}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("%v", err)
	}

	lookPath = func(string) (string, error) { return "/bin/grok", nil }
	oldRun := runGrokServe
	runGrokServe = func(bind, secret string) error { return errors.New("spawn failed") }
	t.Cleanup(func() { runGrokServe = oldRun })
	if err := serveAgent(freeAddr(t), "s", false); err == nil {
		t.Fatal("expected spawn fail")
	}
	if err := serveAgent(freeAddr(t), "s", true); err == nil {
		t.Fatal("replace + spawn fail")
	}
}

func TestRunUntilSignal(t *testing.T) {
	addr := freeAddr(t)
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-no-open", "-no-agent", "-listen", addr, "-cwd", t.TempDir(), "-secret", "sig"})
	}()
	if err := waitTCP(addr, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after SIGINT")
	}
}

func TestStartGrokDefault(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := startGrok("127.0.0.1:1", "s"); err == nil {
		t.Fatal("expected start error")
	}
}

func TestServePaneDefaultCwdAndGrokServe(t *testing.T) {
	addr := freeAddr(t)
	stop := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- servePane(paneCfg{listen: addr, secret: "x", noAgent: true, noOpen: true}, stop)
	}()
	if err := waitTCP(addr, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	close(stop)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if err := runGrokServe("127.0.0.1:1", "s"); err == nil {
		t.Fatal("expected grok start error")
	}
}

func TestHandleWSHTTPAndJunk(t *testing.T) {
	p := &proxy{agentBase: "ws://127.0.0.1:1", secret: "x", cwd: t.TempDir()}
	rec := httptest.NewRecorder()
	p.handleWS(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
}

func TestCopyFileExistsAndDeleteDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "s")
	dest := filepath.Join(dir, "d")
	_ = os.WriteFile(src, []byte("hi"), 0o644)
	_ = os.WriteFile(dest, []byte("no"), 0o644)
	if err := copyFile(src, dest); err == nil {
		t.Fatal("excl")
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sub, "f"), []byte("x"), 0o644)
	rec := httptest.NewRecorder()
	handleUpload(rec, httptest.NewRequest(http.MethodDelete, "/v1/upload?cwd="+dir+"&path="+sub, nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatal(rec.Code)
	}
}

func TestHandleMetaDirect(t *testing.T) {
	p := &proxy{cwd: t.TempDir()}
	rec := httptest.NewRecorder()
	p.handleMeta(rec, httptest.NewRequest(http.MethodGet, "/meta", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Grok Pane") {
		t.Fatal(rec.Body.String())
	}
}
