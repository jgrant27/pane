package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionGroupDir(t *testing.T) {
	t.Setenv("GROK_HOME", "/tmp/grok-home-test")
	got := sessionGroupDir("/Users/jgrant/stuff/i27-blog")
	want := filepath.Join("/tmp/grok-home-test", "sessions", "%2FUsers%2Fjgrant%2Fstuff%2Fi27-blog")
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestListGrokSessions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01abc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{
		"info": {"id":"01abc","cwd":"/Users/jgrant/stuff/demo"},
		"generated_title":"Hello there",
		"updated_at":"2026-08-17T12:00:00Z",
		"num_messages":3
	}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	list := listGrokSessions(cwd, 10)
	if len(list) != 1 || list[0].Title != "Hello there" || list[0].ID != "01abc" {
		t.Fatalf("%+v", list)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/sessions?cwd="+cwd, nil)
	rec := httptest.NewRecorder()
	handleSessions(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var out []sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("%s", rec.Body.String())
	}
}

func TestDeleteGrokSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01del")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(`{"info":{"id":"01del"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/v1/sessions?cwd="+cwd+"&id=01del", nil)
	rec := httptest.NewRecorder()
	handleSessions(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dir still there")
	}
	bad := httptest.NewRequest(http.MethodDelete, "/v1/sessions?cwd="+cwd+"&id=../x", nil)
	rec2 := httptest.NewRecorder()
	handleSessions(rec2, bad)
	if rec2.Code == 204 {
		t.Fatal("traversal allowed")
	}

	// Missing id is idempotent once the directory is gone.
	req3 := httptest.NewRequest(http.MethodDelete, "/v1/sessions?cwd="+cwd+"&id=01del", nil)
	rec3 := httptest.NewRecorder()
	handleSessions(rec3, req3)
	if rec3.Code != http.StatusNoContent {
		t.Fatalf("missing code %d %s", rec3.Code, rec3.Body.String())
	}
}

func TestAppendReplay(t *testing.T) {
	var evs []replayEvent
	evs = appendReplay(evs, "you", "hel")
	evs = appendReplay(evs, "you", "lo")
	evs = appendReplay(evs, "out", "hi")
	if len(evs) != 2 || evs[0].Text != "hello" || evs[1].Text != "hi" {
		t.Fatalf("%+v", evs)
	}
}
