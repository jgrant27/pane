package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHistoryEdges(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	t.Setenv("GROK_BIN", "")
	t.Setenv("PATH", t.TempDir())

	_ = grokSessionsDelete("/tmp", "01abc")
	if err := deleteGrokSession("/tmp", "bad/id"); err == nil {
		t.Fatal("invalid id")
	}

	if looksLikeSessionID("short") || looksLikeSessionID("01zzzz_not_hex______________") {
		t.Fatal("looksLike")
	}
	if !looksLikeSessionID("01aaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Fatal("want session id")
	}
	if isStubSession(sessionInfo{Messages: 4, Title: "Hi"}) {
		t.Fatal("not stub")
	}
	if !isStubSession(sessionInfo{ID: "01aaaaaaaaaaaaaaaaaaaaaaaa", Title: "01aaaaaaaaaaaaaaaaaaaaaaaa"}) {
		t.Fatal("stub title=id")
	}

	if _, ok := readSummary(filepath.Join(root, "nope.json")); ok {
		t.Fatal("missing summary")
	}
	bad := filepath.Join(root, "bad.json")
	_ = os.WriteFile(bad, []byte("{"), 0o644)
	if _, ok := readSummary(bad); ok {
		t.Fatal("bad json")
	}
	_ = os.WriteFile(bad, []byte(`{"info":{}}`), 0o644)
	if _, ok := readSummary(bad); ok {
		t.Fatal("no id")
	}

	if len(listGrokSessions(t.TempDir(), 10)) != 0 {
		t.Fatal("missing group")
	}
	if len(listGrokProjects()) != 0 {
		t.Fatal("no sessions dir")
	}
	if len(listAllGrokSessions(10)) != 0 {
		t.Fatal("no sessions dir all")
	}

	cwd := t.TempDir()
	group := sessionGroupDir(cwd)
	if err := os.MkdirAll(group, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(group, "note.txt"), []byte("x"), 0o644)
	empty := filepath.Join(group, "not-a-session")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(empty, "summary.json"), []byte(`{"info":{"id":"nope"}}`), 0o644)

	id1 := "01cccccccccccccccccccccccc"
	id2 := "01dddddddddddddddddddddddd"
	for i, id := range []string{id1, id2} {
		d := filepath.Join(group, id)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		sum := map[string]any{
			"info":            map[string]any{"id": id, "cwd": cwd},
			"generated_title": "T",
			"updated_at":      "2026-08-17T1" + string(rune('0'+i)) + ":00:00Z",
			"num_messages":    2,
		}
		b, _ := json.Marshal(sum)
		_ = os.WriteFile(filepath.Join(d, "summary.json"), b, 0o644)
	}
	if got := listGrokSessions(cwd, 1); len(got) != 1 {
		t.Fatalf("limit %d", len(got))
	}
	if got := listGrokProjects(); len(got) != 1 || got[0].Sessions != 2 {
		t.Fatalf("projects %+v", listGrokProjects())
	}
	if got := listAllGrokSessions(1); len(got) != 1 {
		t.Fatalf("all %d", len(got))
	}

	// file where a session dir should be
	fileSess := filepath.Join(sessionGroupDir(cwd), "01eeeeeeeeeeeeeeeeeeeeeeee")
	_ = os.WriteFile(fileSess, []byte("x"), 0o644)
	if err := deleteGrokSession(cwd, "01eeeeeeeeeeeeeeeeeeeeeeee"); err == nil {
		t.Fatal("expected not a dir")
	}

	// project group that is not a directory
	sessRoot := filepath.Join(root, "sessions")
	_ = os.WriteFile(filepath.Join(sessRoot, "notdir"), []byte("x"), 0o644)
	_ = listGrokProjects()
	_ = listAllGrokSessions(5)

	// replay edges
	if evs := replayUpdates(cwd, "missing", 10); len(evs) != 0 {
		t.Fatal(evs)
	}
	upd := filepath.Join(group, id1, "updates.jsonl")
	var lines string
	lines += `not json` + "\n"
	lines += `{"params":{"update":{"sessionUpdate":"tool_call","title":"x"}}}` + "\n"
	lines += `{"params":{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"t"}}}}` + "\n"
	lines += `{"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"text":""}}}}` + "\n"
	if err := os.WriteFile(upd, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = replayUpdates(cwd, id1, 10)
	_ = parseChatReplay([]byte(lines))

	if contentTextFromRaw(nil) != "" {
		t.Fatal("nil raw")
	}
}
