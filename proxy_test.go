package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentText(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hi", "hi"},
		{map[string]any{"type": "text", "text": "hi"}, "hi"},
		{map[string]any{"content": map[string]any{"text": "hi"}}, "hi"},
		{[]any{map[string]any{"text": "a"}, map[string]any{"text": "b"}}, "ab"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := contentText(c.in); got != c.want {
			t.Fatalf("contentText(%v)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRPCError(t *testing.T) {
	if err := rpcError(nil); err != nil {
		t.Fatal(err)
	}
	if err := rpcError([]byte(`{"sessionId":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := rpcError([]byte(`{"error":{"message":"nope"}}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePromptCaps(t *testing.T) {
	if parsePromptCaps([]byte(`{"agentCapabilities":{"promptCapabilities":{"image":true}}}`)) != true {
		t.Fatal("expected image")
	}
	if parsePromptCaps([]byte(`{}`)) {
		t.Fatal("default is false")
	}
}

func TestAskHelpers(t *testing.T) {
	if !isAskMethod("GrokBuild:ask_user_question") || !isAskMethod("ask_user_question") {
		t.Fatal("method")
	}
	if isAskMethod("session/update") || isAskMethod("") {
		t.Fatal("not ask")
	}
	if !isAskTool("ask_user_question") || !isAskTool("Ask: hello") || isAskTool("read_file") || isAskTool("") {
		t.Fatal("tool title")
	}
	if rpcIDSet(nil) || rpcIDSet([]byte("null")) || !rpcIDSet([]byte("7")) {
		t.Fatal("rpc id set")
	}
	if n, ok := rpcIDInt([]byte("42")); !ok || n != 42 {
		t.Fatal(n, ok)
	}
	if _, ok := rpcIDInt([]byte(`"x"`)); ok {
		t.Fatal("string id")
	}

	qs := parseAskQuestions([]byte(`{"questions":[{"question":"Q?","options":[{"label":"A","description":"da"},{"label":""}]}]}`))
	if len(qs) != 1 || qs[0].Question != "Q?" || len(qs[0].Options) != 1 {
		t.Fatalf("%+v", qs)
	}
	qs = parseAskQuestions([]byte(`{"rawInput":{"questions":[{"header":"H","multi_select":true,"options":[{"label":"x"}]}]}}`))
	if len(qs) != 1 || qs[0].Question != "H" || !qs[0].multi() {
		t.Fatalf("nested %+v", qs)
	}
	qs = parseAskQuestions([]byte(`[{"question":"bare","options":[{"label":"1"}]}]`))
	if len(qs) != 1 || qs[0].Question != "bare" {
		t.Fatalf("array %+v", qs)
	}
	if parseAskQuestions(nil) != nil || parseAskQuestions([]byte("nope")) != nil {
		t.Fatal("empty")
	}

	ans := parseAskAnswers([]byte(`[{"question":"Q?","selected":["A"]}]`))
	if len(ans) != 1 || ans[0].Selected[0] != "A" {
		t.Fatalf("%+v", ans)
	}
	ans = parseAskAnswers([]byte(`["only"]`))
	if len(ans) != 1 || ans[0].Selected[0] != "only" {
		t.Fatalf("labels %+v", ans)
	}
	if parseAskAnswers(nil) != nil {
		t.Fatal("nil answers")
	}

	skip := buildAskResult("skip", nil).(map[string]any)
	if skip["type"] != "skip_interview" {
		t.Fatal(skip)
	}
	chat := buildAskResult("chat", nil).(map[string]any)
	if chat["type"] != "chat_about_this" {
		t.Fatal(chat)
	}
	acc := buildAskResult("accept", []askAnswer{{Question: "Q?", Selected: []string{"A"}}}).(map[string]any)
	if acc["type"] != "accepted" {
		t.Fatal(acc)
	}

	s := &session{}
	s.offerAsk([]byte("3"), qs)
	s.completeAsk("accept", []askAnswer{{Question: "Q?", Selected: []string{"A"}}})
	s.offerAsk([]byte("4"), qs)
	s.offerAsk([]byte("5"), qs)
	s.clearAsk()
	s.completeAsk("skip", nil)
	s.offerAsk([]byte("6"), qs)
	s.replyMethodNotFound(nil, "x")
	s.writeAskResult(nil, skip)
}

func TestBuildPrompt(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(img, []byte("pngdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocks := buildPrompt("look", []promptFile{{Path: img, Name: "pic.png", Mime: "image/png", Size: 7}}, dir, true)
	if len(blocks) != 3 {
		t.Fatalf("got %d %+v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "text" || blocks[1]["type"] != "resource_link" || blocks[2]["type"] != "image" {
		t.Fatalf("%+v", blocks)
	}
	outside := filepath.Join(dir, "..", "nope.txt")
	blocks = buildPrompt("x", []promptFile{{Path: outside, Name: "nope.txt"}}, dir, false)
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("outside leaked %+v", blocks)
	}
}
