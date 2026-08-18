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

func TestListAllGrokSessions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01all")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"01all","cwd":"/Users/jgrant/stuff/demo"},"generated_title":"All","updated_at":"2026-08-17T12:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	rec := httptest.NewRecorder()
	handleSessions(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	var out []sessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "01all" {
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

func TestPruneStubSessions(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	stubDir := filepath.Join(sessionGroupDir(cwd), "01aaaaaaaaaaaaaaaaaaaaaaaa")
	realDir := filepath.Join(sessionGroupDir(cwd), "01bbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := []byte(`{"info":{"id":"01aaaaaaaaaaaaaaaaaaaaaaaa","cwd":"/Users/jgrant/stuff/demo"},"generated_title":"","num_messages":0}`)
	real := []byte(`{"info":{"id":"01bbbbbbbbbbbbbbbbbbbbbbbb","cwd":"/Users/jgrant/stuff/demo"},"generated_title":"Real talk","num_messages":4}`)
	if err := os.WriteFile(filepath.Join(stubDir, "summary.json"), stub, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "summary.json"), real, 0o644); err != nil {
		t.Fatal(err)
	}
	if n := pruneStubSessions(cwd, nil); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if _, err := os.Stat(stubDir); !os.IsNotExist(err) {
		t.Fatal("stub still there")
	}
	if _, err := os.Stat(realDir); err != nil {
		t.Fatal("real session deleted")
	}
}

func TestListGrokProjects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	dir := filepath.Join(sessionGroupDir(cwd), "01proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"01proj","cwd":"` + cwd + `"},"generated_title":"Hello","updated_at":"2026-08-17T12:00:00Z","num_messages":3}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	list := listGrokProjects()
	if len(list) != 1 || list[0].Cwd != cwd || list[0].Sessions != 1 {
		t.Fatalf("%+v", list)
	}
}

func TestDeleteGrokProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01gone")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"01gone","cwd":"/Users/jgrant/stuff/demo"},"generated_title":"Bye","num_messages":3}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := deleteGrokProject(cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionGroupDir(cwd)); !os.IsNotExist(err) {
		t.Fatal("group still there")
	}
	if err := deleteGrokProject(""); err == nil {
		t.Fatal("empty cwd allowed")
	}
}

func TestRenameGrokSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01ren")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"01ren","cwd":"/Users/jgrant/stuff/demo"},"generated_title":"Old"}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameGrokSession(cwd, "01ren", "Manual name"); err != nil {
		t.Fatal(err)
	}
	got := listGrokSessions(cwd, 10)
	if len(got) != 1 || got[0].Title != "Manual name" {
		t.Fatalf("%+v", got)
	}
}

func TestLooksLikeSessionID(t *testing.T) {
	if !looksLikeSessionID("01a01186-40f3-7dc3-a8c3-8aaa") {
		t.Fatal("ulid")
	}
	if looksLikeSessionID("What Is This Directory") {
		t.Fatal("title")
	}
}

func TestReplayUpdatesChatTailOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01rep")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"old"}}}}`,
		`{"params":{"update":{"sessionUpdate":"tool_call","title":"Execute rm -rf /"}}}`,
		`{"params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"thinking"}}}}`,
		`{"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"old-out"}}}}`,
		`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"new"}}}}`,
		`{"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"new-out"}}}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := replayUpdates(cwd, "01rep", 2)
	if len(evs) != 2 {
		t.Fatalf("%+v", evs)
	}
	if evs[0].Type != "you" || evs[0].Text != "new" {
		t.Fatalf("first %+v", evs[0])
	}
	if evs[1].Type != "out" || evs[1].Text != "new-out" {
		t.Fatalf("second %+v", evs[1])
	}
	for _, ev := range evs {
		if ev.Type == "tool" || ev.Type == "thought" {
			t.Fatalf("leaked %s", ev.Type)
		}
	}
}

func TestReplayUpdatesTailPastHugeLine(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01huge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"FIRST"}}}}` + "\n")
	b.WriteString(`{"params":{"update":{"sessionUpdate":"tool_call","title":"`)
	b.WriteString(strings.Repeat("x", 3<<20))
	b.WriteString(`"}}}` + "\n")
	b.WriteString(`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"LAST YOU"}}}}` + "\n")
	b.WriteString(`{"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"LAST OUT"}}}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := replayUpdates(cwd, "01huge", 2)
	if len(evs) != 2 {
		t.Fatalf("%+v", evs)
	}
	if evs[0].Text != "LAST YOU" || evs[1].Text != "LAST OUT" {
		t.Fatalf("got first-of-file instead of tail: %+v", evs)
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
