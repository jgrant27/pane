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
	if !isAskMethod("GrokBuild:ask_user_question") || !isAskMethod("ask_user_question") || !isAskMethod("x.ai/ask_user_question") {
		t.Fatal("method")
	}
	if !isAskMethod("elicitation/create") {
		t.Fatal("elicitation")
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
	qs = parseAskQuestions([]byte(`{"sessionId":"s","request":{"questions":[{"question":"Which?","options":["Alpha","Beta"]}]}}`))
	if len(qs) != 1 || qs[0].Question != "Which?" || len(qs[0].Options) != 2 || qs[0].Options[0].Label != "Alpha" {
		t.Fatalf("wrapped strings %+v", qs)
	}
	qs = parseAskQuestions([]byte(`{"questions":[{"text":"Pick","choices":[{"title":"One","description":"d"}]}]}`))
	if len(qs) != 1 || qs[0].Question != "Pick" || qs[0].Options[0].Label != "One" {
		t.Fatalf("aliases %+v", qs)
	}
	qs = parseAskQuestions([]byte(`{"message":"Go?","requestedSchema":{"type":"object","properties":{"a":{"title":"Go?","enum":["yes","no"],"enumNames":["Yes","No"]}}}}`))
	if len(qs) != 1 || len(qs[0].Options) != 2 || qs[0].Options[0].Label != "Yes" {
		t.Fatalf("elicitation %+v", qs)
	}
	if parseAskQuestions(nil) != nil || parseAskQuestions([]byte("nope")) != nil {
		t.Fatal("empty")
	}
	if parseAskQuestions([]byte(`{"questions":[{"options":[{"label":"only"}]}]}`)) != nil {
		t.Fatal("option-only array is not a question list")
	}
	qs = parseAskQuestions([]byte(`{"payload":{"questions":[{"question":"Deep","options":[{"name":"N"}]}]}}`))
	if len(qs) != 1 || qs[0].Options[0].Label != "N" {
		t.Fatalf("payload nest %+v", qs)
	}
	qs = parseAskQuestions([]byte(`{"requested_schema":{"enum":["a","b"],"enum_names":["A",""]},"message":"M?"}`))
	if len(qs) != 1 || qs[0].Question != "M?" || qs[0].Options[1].Label != "b" {
		t.Fatalf("snake elicitation %+v", qs)
	}
	qs = parseAskQuestions([]byte(`{"new_string":"{\"questions\":[{\"question\":\"nope\"}]}","params":{"questions":[{"question":"Yes","options":["1"]}]}}`))
	if len(qs) != 1 || qs[0].Question != "Yes" {
		t.Fatalf("skip new_string %+v", qs)
	}
	if _, ok := optionFromAny(3); ok {
		t.Fatal("bad option")
	}
	if !usefulAsk([]askQuestion{{Question: "x"}}) || usefulAsk(nil) || usefulAsk([]askQuestion{{}}) {
		t.Fatal("useful")
	}
	if len(askFromTitle("Ask: hello there")) != 1 || askFromTitle("ask_user_question") != nil || askFromTitle("Ask: ") != nil || askFromTitle("") != nil {
		t.Fatal("title")
	}
	if askFromTitle("Execute `git push origin main && git status`") != nil {
		t.Fatal("execute title is not an ask")
	}
	if permissionAutoAllow(permHint{Kind: "read", Title: "Read file"}) == false ||
		permissionAutoAllow(permHint{Kind: "execute", Title: "Execute git"}) == true {
		t.Fatal("perm auto")
	}
	if permissionAutoAllow(permHint{Title: "Execute `git push`"}) ||
		permissionAutoAllow(permHint{Title: "git push origin"}) {
		t.Fatal("push must wait")
	}
	if permissionAutoAllow(permHint{Kind: "Other", Title: "run_terminal_command", Command: "git push origin main"}) {
		t.Fatal("ACP first-frame run_terminal_command must wait")
	}
	if permissionAutoAllow(permHint{Title: "run_terminal_command", Name: "run_terminal_command"}) {
		t.Fatal("tool name run_terminal_command must wait")
	}
	ro := true
	if !permissionAutoAllow(permHint{Kind: "execute", ReadOnly: &ro}) {
		t.Fatal("read_only execute")
	}
	if !permissionAutoAllow(permHint{Kind: "other", Title: "ask_user_question"}) ||
		!permissionAutoAllow(permHint{Title: "Grep foo"}) {
		t.Fatal("safe auto")
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

	skip := buildAskResult("skip", nil, "").(map[string]any)
	if skip["outcome"] != "skip_interview" {
		t.Fatal(skip)
	}
	chat := buildAskResult("chat", nil, "").(map[string]any)
	if chat["outcome"] != "chat_about_this" {
		t.Fatal(chat)
	}
	acc := buildAskResult("accept", []askAnswer{{Question: "Q?", Selected: []string{"A"}}}, "").(map[string]any)
	if acc["outcome"] != "accepted" {
		t.Fatal(acc)
	}
	el := buildAskResult("accept", []askAnswer{{Question: "Go?", Selected: []string{"Yes"}}}, "elicitation/create").(map[string]any)
	if el["action"] != "accept" {
		t.Fatal(el)
	}
	if buildAskResult("skip", nil, "elicitation/create").(map[string]any)["action"] != "cancel" {
		t.Fatal("elicit skip")
	}
	multi := buildAskResult("accept", []askAnswer{{Selected: []string{"a", "b"}}}, "elicitation").(map[string]any)
	if multi["action"] != "accept" {
		t.Fatal(multi)
	}

	s := &session{}
	s.live.Store(true)
	s.busy.Store(true)
	s.forwardUpdate([]byte(`{"update":{"sessionUpdate":"tool_call","title":"ask_user_question","rawInput":{"questions":[{"question":"Live?","options":["Y","N"]}]}}}`))
	if len(s.askQ) != 1 || s.askQ[0].Question != "Live?" {
		t.Fatalf("forward %+v", s.askQ)
	}
	s.askQ = nil
	s.forwardUpdate([]byte(`{"update":{"sessionUpdate":"tool_call","title":"Execute ` + "`git push origin main`" + `","kind":"execute","rawInput":{"command":"git push origin main"}}}`))
	if len(s.askQ) != 0 {
		t.Fatalf("execute became ask %+v", s.askQ)
	}
	s.replyPermission([]byte("9"), []byte(`{"params":{"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}],"toolCall":{"title":"Execute git push","kind":"execute","rawInput":{"command":"git push"}}}}`))
	if !rpcIDSet(s.permID) || s.permAllow != "allow_once" {
		t.Fatalf("execute should wait %+v", s.permID)
	}
	s.completePerm("allow")
	if rpcIDSet(s.permID) {
		t.Fatal("perm cleared")
	}
	s.replyPermission([]byte("10"), []byte(`{"params":{"options":[{"optionId":"allow_once","kind":"allow_once"}],"toolCall":{"title":"Read file","kind":"read"}}}`))
	if rpcIDSet(s.permID) {
		t.Fatal("read should auto-allow")
	}
	s.replyPermission([]byte("11"), []byte(`{"params":{"options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}],"toolCall":{"title":"run_terminal_command","kind":"Other","rawInput":{"command":"git push origin main && git status"},"_meta":{"x.ai/tool":{"name":"run_terminal_command","kind":"execute","read_only":false}}}}}`))
	if !rpcIDSet(s.permID) {
		t.Fatal("real ACP execute frame should wait")
	}
	s.completePerm("deny")
	s.offerAsk([]byte("3"), "x.ai/ask_user_question", qs)
	s.completeAsk("accept", []askAnswer{{Question: "Q?", Selected: []string{"A"}}})
	s.offerAsk([]byte("4"), "", qs)
	s.offerAsk([]byte("5"), "", qs)
	s.clearAsk()
	s.completeAsk("skip", nil)
	s.offerAsk([]byte("6"), "", qs)
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
