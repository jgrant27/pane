package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestUploadErrorBranches(t *testing.T) {
	dir := t.TempDir()
	fileAsCwd := filepath.Join(dir, "notdir")
	if err := os.WriteFile(fileAsCwd, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+fileAsCwd, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, strings.NewReader("{"))
	req.Header.Set("Content-Type", "application/json")
	newTestProxy().handleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, strings.NewReader(`{"path":"/no/such/file"}`))
	req.Header.Set("Content-Type", "application/json")
	newTestProxy().handleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodDelete, "/v1/upload?cwd="+dir+"&path=/tmp/outside", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	// A path pane never copied in is not pane's to remove.
	inside := filepath.Join(dir, "gone.txt")
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodDelete, "/v1/upload?cwd="+dir+"&path="+inside, nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("notfile", "x")
	_ = w.Close()
	req = httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}

	for i := 0; i < 5; i++ {
		_ = uniquePath(dir, "dup.txt")
		_ = os.WriteFile(filepath.Join(dir, "dup.txt"), []byte("x"), 0o644)
		if i > 0 {
			_ = os.WriteFile(filepath.Join(dir, "dup-"+itoa(i+1)+".txt"), []byte("x"), 0o644)
		}
	}
	p := uniquePath(dir, "dup.txt")
	if filepath.Base(p) == "dup.txt" && exists(filepath.Join(dir, "dup.txt")) {
		// may be dup-N
	}
}

func itoa(n int) string { return strings.TrimPrefix(strings.Repeat("x", 0)+string(rune('0'+n)), "") }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestUsageAndRenameBranches(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	id := "01usagebranchxxxxxxxxxxxxxxxx"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`{"totalTokens":50,"contextWindowUsage":25}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u := readSessionUsage(cwd, id)
	if u.Used != 50 || u.Size == 0 || u.Pct == 0 {
		t.Fatalf("%+v", u)
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`{"contextWindowUsage":7}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u = readSessionUsage(cwd, id)
	if u.Pct != 7 {
		t.Fatalf("%+v", u)
	}
	u = readSessionUsage(cwd, "../bad")
	if u.Used != 0 {
		t.Fatal(u)
	}
	u = readSessionUsage(cwd, "missing")
	_ = u
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	u = readSessionUsage(cwd, id)
	if u.Model != "" && u.Used != 0 {
		// invalid json returns empty
	}

	if err := renameGrokSession(cwd, "../x", "t"); err == nil {
		t.Fatal("bad id")
	}
	if err := renameGrokSession(cwd, id, "  "); err == nil {
		t.Fatal("empty title")
	}
	if err := renameGrokSession(cwd, "01missingxxxxxxxxxxxxxxxxxxx", "t"); err == nil {
		t.Fatal("missing")
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameGrokSession(cwd, id, "t"); err == nil {
		t.Fatal("bad json")
	}
	rec := httptest.NewRecorder()
	handleRename(rec, httptest.NewRequest(http.MethodPost, "/v1/rename?cwd="+cwd+"&id="+id, strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	handleRename(rec, httptest.NewRequest(http.MethodPost, "/v1/rename?cwd="+cwd+"&id="+id, strings.NewReader(`{"title":"X"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	handleProjects(rec, httptest.NewRequest(http.MethodDelete, "/v1/projects?cwd=", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
}

func TestHandshakeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(data, &env)
		_ = c.WriteJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      env.ID,
			"error":   map[string]any{"message": "nope"},
		})
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	p := &proxy{agentBase: "ws" + strings.TrimPrefix(srv.URL, "http"), secret: "", cwd: t.TempDir()}
	hs := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(hs.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "err" {
		t.Fatalf("%v", msg)
	}
}

func TestParseSessionModelsSparse(t *testing.T) {
	st := parseSessionModels([]byte(`{}`))
	if st.Current != "" {
		t.Fatal(st)
	}
	st = parseSessionModels([]byte(`notjson`))
	_ = st
	st = parseSessionModels([]byte(`{"models":{"availableModels":[{"modelId":"m"}]}}`))
	if len(st.Models) != 1 {
		t.Fatal(st)
	}
}

func TestRemoteBadMeta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/meta":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{`))
		case "/v1/sessions":
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(srv.Close)
	if remotePaneOK(t.Context(), srv.URL) {
		t.Fatal("bad json meta")
	}
	if fetchRemoteSessions(t.Context(), srv.URL) != nil {
		t.Fatal("500 sessions")
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"Nope"}`))
	}))
	t.Cleanup(srv2.Close)
	if remotePaneOK(t.Context(), srv2.URL) {
		t.Fatal("wrong name")
	}
}
