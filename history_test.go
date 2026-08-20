package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

func TestListGrokSessionsWithoutSummary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	id := "01a01252254873809b92c35df45e8471"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	list := listGrokSessions(cwd, 10)
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("want session without summary, got %+v", list)
	}
	projs := listGrokProjects()
	if len(projs) != 1 || projs[0].Sessions != 1 {
		t.Fatalf("project must list dirs without summary.json: %+v", projs)
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

func TestRenameGrokProject(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	if err := renameGrokProject(cwd, "LEGEND Case"); err != nil {
		t.Fatal(err)
	}
	if projectDisplayName(cwd, sessionGroupDir(cwd)) != "LEGEND Case" {
		t.Fatal(projectDisplayName(cwd, sessionGroupDir(cwd)))
	}
	id := "01projnamexxxxxxxxxxxxxxxxxx"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"` + id + `","cwd":"` + cwd + `"},"generated_title":"Hi","num_messages":2}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	list := listGrokProjects()
	if len(list) != 1 || list[0].Name != "LEGEND Case" {
		t.Fatalf("%+v", list)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/projects?cwd="+cwd, strings.NewReader(`{"name":"Case file"}`))
	handleProjects(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if projectDisplayName(cwd, sessionGroupDir(cwd)) != "Case file" {
		t.Fatal(projectDisplayName(cwd, sessionGroupDir(cwd)))
	}
	if err := renameGrokProject(cwd, ""); err != nil {
		t.Fatal(err)
	}
	if projectDisplayName(cwd, sessionGroupDir(cwd)) != filepath.Base(cwd) {
		t.Fatal(projectDisplayName(cwd, sessionGroupDir(cwd)))
	}
	if err := renameGrokProject(cwd, "a/b"); err == nil {
		t.Fatal("slash allowed")
	}
	if err := renameGrokProject("", "x"); err == nil {
		t.Fatal("empty cwd")
	}
	rec = httptest.NewRecorder()
	handleProjects(rec, httptest.NewRequest(http.MethodPost, "/v1/projects?cwd="+cwd, strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
}

func TestListGrokProjectsIncludesStubOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	id := "01aaaaaaaaaaaaaaaaaaaaaaaa"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := []byte(`{"info":{"id":"` + id + `","cwd":"` + cwd + `"},"generated_title":"` + id + `","num_messages":1}`)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	list := listGrokProjects()
	if len(list) != 1 || list[0].Cwd != cwd || list[0].Sessions != 1 {
		t.Fatalf("stub-only project missing: %+v", list)
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

func TestAppendReplayWhitespace(t *testing.T) {
	// A bubble may not open on whitespace: that is the empty first bubble a
	// resumed session used to show.
	if evs := appendReplay(nil, "out", "\n"); len(evs) != 0 {
		t.Fatalf("whitespace-only chunk started a bubble: %+v", evs)
	}
	if evs := appendReplay(nil, "out", " \t\n"); len(evs) != 0 {
		t.Fatalf("whitespace-only chunk started a bubble: %+v", evs)
	}
	// Inside a message the same chunk is a paragraph break and must survive.
	evs := appendReplay(nil, "out", "one")
	evs = appendReplay(evs, "out", "\n\n")
	evs = appendReplay(evs, "out", "two")
	if len(evs) != 1 || evs[0].Text != "one\n\ntwo" {
		t.Fatalf("lost the blank line between paragraphs: %+v", evs)
	}
	// Types that never merge always open a bubble, so whitespace-only is
	// dropped there too.
	if evs := appendReplay([]replayEvent{{Type: "tool", Text: "x"}}, "tool", "  "); len(evs) != 1 {
		t.Fatalf("%+v", evs)
	}
}

// A session's updates.jsonl is mostly tool traffic, so a transcript request
// walks a long way back. The walk must cost about what it reads, not the
// square of it: /v1/transcript on a real 85 MB session took 4.6s and 6.5 GB
// when every chunk re-parsed the whole accumulated tail.
func TestReplayUpdatesTailCostStaysLinear(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01big")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const chats = 200
	path := filepath.Join(dir, "updates.jsonl")
	writeSparseUpdates(t, path, chats)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	evs := replayUpdates(cwd, "01big", 400)
	runtime.ReadMemStats(&after)

	if len(evs) != chats {
		t.Fatalf("want %d events, got %d", chats, len(evs))
	}
	if evs[0].Text != "m0" || evs[len(evs)-1].Text != fmt.Sprintf("m%d", chats-1) {
		t.Fatalf("first %+v last %+v", evs[0], evs[len(evs)-1])
	}
	// Reading the file once and parsing it once costs about its size. The
	// version that re-parsed the accumulated tail allocated fifteen times it
	// on this input, and much worse on a real 85 MB session.
	if limit := uint64(st.Size()) * 4; after.TotalAlloc-before.TotalAlloc > limit {
		t.Fatalf("allocated %d bytes for a %d byte file (limit %d)",
			after.TotalAlloc-before.TotalAlloc, st.Size(), limit)
	}
}

// writeSparseUpdates writes an updates.jsonl shaped like a real session: chat
// lines are rare, buried in tool traffic, so the tail walk has to go a long
// way back before it has enough of them.
func writeSparseUpdates(t *testing.T, path string, chats int) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	filler := `{"params":{"update":{"sessionUpdate":"tool_call","title":"` + strings.Repeat("x", 900) + `"}}}` + "\n"
	for i := 0; i < chats; i++ {
		for j := 0; j < 70; j++ {
			if _, err := w.WriteString(filler); err != nil {
				t.Fatal(err)
			}
		}
		// Alternating speakers, because two chunks of the same speaker are
		// one message and come back as one event.
		kind := "user_message_chunk"
		if i%2 == 1 {
			kind = "agent_message_chunk"
		}
		if _, err := fmt.Fprintf(w, `{"params":{"update":{"sessionUpdate":%q,"content":{"text":"m%d"}}}}`+"\n", kind, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestBoundedCarry(t *testing.T) {
	if got := boundedCarry([]byte("half a line")); string(got) != "half a line" {
		t.Fatalf("%q", got)
	}
	// A line the parser would skip anyway must not be dragged back through
	// every chunk of the walk.
	if got := boundedCarry(make([]byte, maxReplayLine+1)); got != nil {
		t.Fatalf("carried %d bytes of a line that is too long to parse", len(got))
	}
}

func TestReplayUpdatesMergesAcrossChunkBoundary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01seam")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One message streamed as many chunks, long enough to straddle the 512 KB
	// read boundary: it is one bubble, not one per chunk of the file.
	var b strings.Builder
	b.WriteString(`{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":"q"}}}}` + "\n")
	line := `{"params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"` + strings.Repeat("a", 100) + `"}}}}` + "\n"
	for i := 0; i < 20_000; i++ {
		b.WriteString(line)
	}
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	evs := replayUpdates(cwd, "01seam", 400)
	if len(evs) != 2 || evs[0].Type != "you" || evs[1].Type != "out" {
		t.Fatalf("one question and one answer came back as %d events", len(evs))
	}
	if want := 20_000 * 100; len(evs[1].Text) != want {
		t.Fatalf("answer is %d bytes, want %d", len(evs[1].Text), want)
	}
}

func TestListGrokProjectsCaching(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := t.TempDir()
	id := "01cachexxxxxxxxxxxxxxxxxxxxx"
	dir := filepath.Join(sessionGroupDir(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(sid, updated string) {
		t.Helper()
		d := filepath.Join(sessionGroupDir(cwd), sid)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := []byte(`{"info":{"id":"` + sid + `","cwd":"` + cwd + `"},"generated_title":"T","updated_at":"` + updated + `","num_messages":2}`)
		if err := os.WriteFile(filepath.Join(d, "summary.json"), sum, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(id, "2026-08-17T12:00:00Z")
	first := listGrokProjects()
	if len(first) != 1 || first[0].Updated != "2026-08-17T12:00:00Z" {
		t.Fatalf("%+v", first)
	}

	// Rewriting a summary in place leaves every directory mtime alone, so the
	// second call must be answered from the cache without reading it again.
	write(id, "2026-08-18T12:00:00Z")
	if got := listGrokProjects(); got[0].Updated != "2026-08-17T12:00:00Z" {
		t.Fatalf("summary re-read on every call: %+v", got)
	}

	// A new session changes the group's mtime, and that has to be seen at once.
	write("01cache2xxxxxxxxxxxxxxxxxxxx", "2026-08-19T12:00:00Z")
	got := listGrokProjects()
	if len(got) != 1 || got[0].Sessions != 2 || got[0].Updated != "2026-08-19T12:00:00Z" {
		t.Fatalf("new session not picked up: %+v", got)
	}

	// So does a delete, which also invalidates the cache explicitly.
	if err := deleteGrokSession(cwd, "01cache2xxxxxxxxxxxxxxxxxxxx"); err != nil {
		t.Fatal(err)
	}
	if got := listGrokProjects(); len(got) != 1 || got[0].Sessions != 1 {
		t.Fatalf("deleted session still listed: %+v", got)
	}

	// A rename overwrites .name in place; nothing else would notice.
	if err := renameGrokProject(cwd, "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := renameGrokProject(cwd, "Renamed twice"); err != nil {
		t.Fatal(err)
	}
	if got := listGrokProjects(); len(got) != 1 || got[0].Name != "Renamed twice" {
		t.Fatalf("stale project name: %+v", got)
	}
}

func TestProjectsFingerprintMissingRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	if projectsFingerprint(filepath.Join(root, "sessions")) != "" {
		t.Fatal("no sessions dir must not fingerprint")
	}
	if len(listGrokProjects()) != 0 {
		t.Fatal("no sessions dir")
	}
}
