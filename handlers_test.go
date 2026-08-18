package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleProjectsRenameUsageTranscript(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	id := "01handlertestxxxxxxxxxxxxxxxx"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"` + id + `","cwd":"` + cwd + `"},"generated_title":"Hello","updated_at":"2026-08-17T12:00:00Z","num_messages":2}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), []byte(`{"contextTokensUsed":10,"contextWindowTokens":100}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"hi"}}}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handleProjects(rec, httptest.NewRequest(http.MethodGet, "/v1/projects", nil))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var projects []projectInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &projects); err != nil || len(projects) != 1 {
		t.Fatalf("%s %v", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	handleProjects(rec, httptest.NewRequest(http.MethodPut, "/v1/projects", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rename?cwd="+cwd+"&id="+id, strings.NewReader(`{"title":"Renamed"}`))
	handleRename(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatal(rec.Body.String())
	}
	list := listGrokSessions(cwd, 10)
	if len(list) != 1 || list[0].Title != "Renamed" {
		t.Fatalf("%+v", list)
	}

	rec = httptest.NewRecorder()
	handleRename(rec, httptest.NewRequest(http.MethodGet, "/v1/rename", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	handleRename(rec, httptest.NewRequest(http.MethodPost, "/v1/rename", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}

	rec = httptest.NewRecorder()
	handleUsage(rec, httptest.NewRequest(http.MethodGet, "/v1/usage?cwd="+cwd+"&id="+id, nil))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var u usageInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &u); err != nil || u.Used != 10 {
		t.Fatalf("%s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleUsage(rec, httptest.NewRequest(http.MethodGet, "/v1/usage", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}

	rec = httptest.NewRecorder()
	handleTranscript(rec, httptest.NewRequest(http.MethodGet, "/v1/transcript?cwd="+cwd+"&id="+id, nil))
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = httptest.NewRecorder()
	handleTranscript(rec, httptest.NewRequest(http.MethodGet, "/v1/transcript?cwd="+cwd+"&id=../x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	handleTranscript(rec, httptest.NewRequest(http.MethodGet, "/v1/transcript", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}

	rec = httptest.NewRecorder()
	handleProjects(rec, httptest.NewRequest(http.MethodDelete, "/v1/projects?cwd="+cwd, nil))
	if rec.Code != http.StatusNoContent {
		t.Fatal(rec.Body.String())
	}
	if _, err := os.Stat(sessionGroupDir(cwd)); !os.IsNotExist(err) {
		t.Fatal("project group still there")
	}
}

func TestHandleRemoteSessions(t *testing.T) {
	remoteMu.Lock()
	prevList, prevAt, prevRef := remoteList, remoteAt, remoteRefreshing
	remoteList = []remoteSession{{Host: "peer", Origin: "https://x"}}
	remoteRefreshing = true
	remoteMu.Unlock()
	t.Cleanup(func() {
		remoteMu.Lock()
		remoteList, remoteAt, remoteRefreshing = prevList, prevAt, prevRef
		remoteMu.Unlock()
	})
	rec := httptest.NewRecorder()
	handleRemoteSessions(rec, httptest.NewRequest(http.MethodGet, "/v1/remote-sessions", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "peer") {
		t.Fatal(rec.Body.String())
	}
}

func TestGrokHomeAndSessionID(t *testing.T) {
	if !validSessionID("01abc") || validSessionID("../x") || validSessionID("") {
		t.Fatal("validSessionID")
	}
	t.Setenv("GROK_HOME", "/tmp/x")
	if grokHome() != "/tmp/x" {
		t.Fatal(grokHome())
	}
	if grokBin() == "" {
		// may be empty if grok not installed and GROK_BIN unset
	}
	t.Setenv("GROK_BIN", "/opt/grok")
	if grokBin() != "/opt/grok" {
		t.Fatal(grokBin())
	}
}

func TestContentTextFromRaw(t *testing.T) {
	if contentTextFromRaw([]byte(`{"text":"hi"}`)) != "hi" {
		t.Fatal(contentTextFromRaw([]byte(`{"text":"hi"}`)))
	}
	if contentTextFromRaw([]byte(`not-json`)) != "" {
		t.Fatal("bad json")
	}
}
