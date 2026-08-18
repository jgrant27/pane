package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverTruncatesAndEmptyHost(t *testing.T) {
	list := make([]sessionInfo, 45)
	for i := range list {
		list[i] = sessionInfo{ID: "01" + strings.Repeat("a", 24), Title: "n", Updated: "2026-08-17T12:00:00Z"}
		if i == 0 {
			list[i].Updated = "2026-08-18T12:00:00Z"
		}
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/meta" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "Grok Pane"})
			return
		}
		_ = json.NewEncoder(w).Encode(list)
	}))
	t.Cleanup(ok.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "nope"})
	}))
	t.Cleanup(bad.Close)
	oldScheme, oldPath, oldJSON := remoteURLScheme, lookPath, tailscaleJSON
	remoteURLScheme = "http"
	lookPath = func(string) (string, error) { return "/usr/bin/tailscale", nil }
	tailscaleJSON = func() ([]byte, error) {
		return []byte(`{
			"Self":{"DNSName":"self.ts.net.","HostName":"self"},
			"Peer":{
				"a":{"DNSName":"` + strings.TrimPrefix(ok.URL, "http://") + `","HostName":"","Online":true},
				"b":{"DNSName":"` + strings.TrimPrefix(bad.URL, "http://") + `","HostName":"bad","Online":true},
				"c":{"DNSName":"","HostName":"empty","Online":true}
			}
		}`), nil
	}
	t.Cleanup(func() {
		remoteURLScheme, lookPath, tailscaleJSON = oldScheme, oldPath, oldJSON
	})
	got := discoverRemoteSessions(3 * time.Second)
	if len(got) != 40 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Host == "" {
		t.Fatal("expected host from DNS")
	}
}

func TestServePaneListenAndTailscaleErrors(t *testing.T) {
	err := servePane(paneCfg{
		listen:  "256.256.256.256:9",
		cwd:     t.TempDir(),
		secret:  "x",
		noAgent: true,
		noOpen:  true,
	}, make(chan struct{}))
	if err == nil {
		t.Fatal("expected listen error")
	}
	oldPath, oldRun := lookPath, tailscaleRun
	lookPath = func(string) (string, error) { return "/bin/true", nil }
	tailscaleRun = func(args ...string) error { return errors.New("serve fail") }
	t.Cleanup(func() { lookPath, tailscaleRun = oldPath, oldRun })
	err = servePane(paneCfg{
		listen:    freeAddr(t),
		cwd:       t.TempDir(),
		secret:    "x",
		noAgent:   true,
		noOpen:    true,
		tailscale: true,
	}, make(chan struct{}))
	if err == nil || !strings.Contains(err.Error(), "tailscale serve") {
		t.Fatalf("%v", err)
	}
}

func TestBuildPromptEmptyAndReplayLine(t *testing.T) {
	dir := t.TempDir()
	blocks := buildPrompt("", nil, dir, false)
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("%+v", blocks)
	}
	link := filepath.Join(dir, "gone.png")
	if err := os.Symlink(filepath.Join(dir, "missing.png"), link); err != nil {
		t.Fatal(err)
	}
	_ = buildPrompt("x", []promptFile{{Path: link, Name: "gone.png", Mime: "image/png", Size: 1}}, dir, true)
	if err := rpcError([]byte("not-json")); err != nil {
		t.Fatal(err)
	}
	_ = parseChatReplay([]byte(`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"z"}}}}`))
	_ = parseChatReplay(append(bytesRepeat('x', 1<<20+10), '\n'))
	t.Setenv("GROK_HOME", t.TempDir())
	db := filepath.Join(grokHome(), "sessions", "session_search.sqlite")
	_ = os.MkdirAll(filepath.Dir(db), 0o755)
	_ = os.WriteFile(db, []byte("x"), 0o644)
	purgeSessionSearch("01abc")

	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "dup.txt"), []byte("x"), 0o644)
	for i := 2; i < 1000; i++ {
		_ = os.WriteFile(filepath.Join(dir2, "dup-"+itoa3(i)+".txt"), []byte("x"), 0o644)
	}
	p := uniquePath(dir2, "dup.txt")
	if filepath.Base(p) == "dup.txt" {
		t.Fatal("expected uniqued name")
	}
	if err := copyFile(dir2, filepath.Join(dir2, "out.bin")); err == nil {
		t.Fatal("copy dir")
	}

	_ = underCwd("\x00", "x")
	_ = underCwd(dir2, "\x00")
	_, _ = attachPath("\x00", "x")
	_, _ = dialAgent("http://[", "x", time.Millisecond)

	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	stub := "01ffffffffffffffffffffffff"
	sd := filepath.Join(sessionGroupDir(cwd), stub)
	if err := os.MkdirAll(sd, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(sd, "summary.json"), []byte(`{"info":{"id":"`+stub+`"},"generated_title":"","num_messages":0}`), 0o644)
	_ = os.Chmod(sd, 0)
	t.Cleanup(func() { _ = os.Chmod(sd, 0o755) })
	_ = pruneStubSessions(cwd, nil)
}

func itoa3(n int) string {
	if n == 0 {
		return "0"
	}
	var d [12]byte
	i := len(d)
	for n > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
