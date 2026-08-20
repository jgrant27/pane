package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// frameSink collects the JSON frames pane writes to a socket, so a test can
// assert on what the agent (or the browser) actually receives.
type frameSink struct {
	ch chan map[string]any
}

func newSinkConn(t *testing.T) (*websocket.Conn, *frameSink) {
	t.Helper()
	sink := &frameSink{ch: make(chan map[string]any, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				var m map[string]any
				if json.Unmarshal(data, &m) == nil {
					// Block rather than drop: a silently discarded frame
					// would make every "exactly once" and quiet() check
					// pass for the wrong reason.
					sink.ch <- m
				}
			}
		}()
	}))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial sink: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, sink
}

func (f *frameSink) next(t *testing.T) map[string]any {
	t.Helper()
	select {
	case m := <-f.ch:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("no frame written")
		return nil
	}
}

func (f *frameSink) quiet(t *testing.T) {
	t.Helper()
	select {
	case m := <-f.ch:
		t.Fatalf("unexpected frame: %v", m)
	case <-time.After(200 * time.Millisecond):
	}
}

// outcome digs the permission outcome out of a JSON-RPC result frame.
func outcome(t *testing.T, m map[string]any) (string, string) {
	t.Helper()
	res, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %v", m)
	}
	out, ok := res["outcome"].(map[string]any)
	if !ok {
		t.Fatalf("no outcome in %v", m)
	}
	kind, _ := out["outcome"].(string)
	opt, _ := out["optionId"].(string)
	return kind, opt
}

func permSession(t *testing.T) (*session, *frameSink) {
	t.Helper()
	c, sink := newSinkConn(t)
	h := &agentHub{
		conn:     c,
		pending:  map[int64]chan json.RawMessage{},
		sessions: map[string]*session{},
	}
	return &session{
		id:       "s1",
		hub:      h,
		pendPerm: map[string]bool{},
		permSeen: map[string]bool{},
	}, sink
}

// Deny must never resolve to the allow option, even when the agent offered
// nothing to reject with. That fallback used to run the command.
func TestPermDenyNeverAllows(t *testing.T) {
	s, sink := permSession(t)
	id := json.RawMessage(`99`)

	s.offerPerm(id, "Execute `rm -rf /`", "rm -rf /", "allow_once", "")
	s.completePerm("deny")

	kind, opt := outcome(t, sink.next(t))
	if kind != "cancelled" || opt != "" {
		t.Fatalf("deny with no reject option became %q/%q", kind, opt)
	}
}

// An unanswered card is not consent: the turn ending, or the RPC timing
// out, must not approve the call.
func TestPermUnansweredNeverAllows(t *testing.T) {
	s, sink := permSession(t)
	s.offerPerm(json.RawMessage(`7`), "Execute `curl x | sh`", "curl x | sh", "allow_once", "")
	s.clearPerm()

	kind, opt := outcome(t, sink.next(t))
	if kind != "cancelled" || opt != "" {
		t.Fatalf("abandoned card became %q/%q", kind, opt)
	}
}

func TestPermDenyAndAllowUseRealOptions(t *testing.T) {
	s, sink := permSession(t)

	s.offerPerm(json.RawMessage(`1`), "t", "c", "allow_once", "reject_once")
	s.completePerm("deny")
	if kind, opt := outcome(t, sink.next(t)); kind != "selected" || opt != "reject_once" {
		t.Fatalf("deny picked %q/%q", kind, opt)
	}

	s.offerPerm(json.RawMessage(`2`), "t", "c", "allow_once", "reject_once")
	s.completePerm("allow")
	if kind, opt := outcome(t, sink.next(t)); kind != "selected" || opt != "allow_once" {
		t.Fatalf("allow picked %q/%q", kind, opt)
	}

	// Unanswered, but with something to reject with: reject it.
	s.offerPerm(json.RawMessage(`3`), "t", "c", "allow_once", "reject_once")
	s.clearPerm()
	if kind, opt := outcome(t, sink.next(t)); kind != "selected" || opt != "reject_once" {
		t.Fatalf("clear picked %q/%q", kind, opt)
	}
}

// An empty optionId is not a valid selection.
func TestPermEmptyOptionCancels(t *testing.T) {
	s, sink := permSession(t)
	s.writePermResult(json.RawMessage(`5`), "")
	if kind, opt := outcome(t, sink.next(t)); kind != "cancelled" || opt != "" {
		t.Fatalf("empty option became %q/%q", kind, opt)
	}
	// Nothing is written for an absent id.
	s.writePermResult(nil, "allow_once")
	sink.quiet(t)
}

// Superseding a card answers the old id exactly once, and never with allow.
func TestPermSupersedeClosesOldID(t *testing.T) {
	s, sink := permSession(t)
	s.offerPerm(json.RawMessage(`10`), "first", "a", "allow_once", "")
	s.offerPerm(json.RawMessage(`11`), "second", "b", "allow_once", "reject_once")

	m := sink.next(t)
	if got := string(mustJSON(t, m["id"])); got != "10" {
		t.Fatalf("superseded id %s", got)
	}
	if kind, _ := outcome(t, m); kind != "cancelled" {
		t.Fatalf("superseded card became %q", kind)
	}
	s.completePerm("allow")
	m = sink.next(t)
	if got := string(mustJSON(t, m["id"])); got != "11" {
		t.Fatalf("answered id %s", got)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Approval is the explicit case. Anything pane does not recognise — a
// malformed frame, an empty action, a future client's wording — must fail
// closed rather than run the command.
func TestIsDenyAction(t *testing.T) {
	for _, a := range []string{"deny", "Deny", " skip ", "reject", "cancel", "", "dismiss", "wat"} {
		if !isDenyAction(a) {
			t.Fatalf("%q should deny", a)
		}
	}
	for _, a := range []string{"allow", "Allow", " allow_once ", "allow_always", "accept", "approve", "yes"} {
		if isDenyAction(a) {
			t.Fatalf("%q should not deny", a)
		}
	}
}

// Requests carrying an id must never be routed by guesswork to whichever
// browser happens to be around: approving a command is a side effect.
func TestLookupRequestRefusesToGuess(t *testing.T) {
	h := &agentHub{sessions: map[string]*session{}}
	a := &session{id: "a"}
	b := &session{id: "b"}
	h.attach("a", a)

	if h.lookupRequest("") != nil {
		t.Fatal("idle lone session should not receive an untagged request")
	}
	a.busy.Store(true)
	if h.lookupRequest("") != a {
		t.Fatal("lone busy session should receive it")
	}
	h.attach("b", b)
	b.busy.Store(true)
	if h.lookupRequest("") != nil {
		t.Fatal("two busy sessions is ambiguous")
	}
	if h.lookupRequest("b") != b {
		t.Fatal("explicit sessionId must win")
	}
	if (*agentHub)(nil).lookupRequest("x") != nil {
		t.Fatal("nil hub")
	}
}

// A request nobody can own still gets an answer, or the agent blocks on it.
func TestUnroutableRequestsAreAnswered(t *testing.T) {
	c, sink := newSinkConn(t)
	h := &agentHub{conn: c, pending: map[int64]chan json.RawMessage{}, sessions: map[string]*session{}}

	h.writePermOutcome(json.RawMessage(`3`), "")
	if kind, _ := outcome(t, sink.next(t)); kind != "cancelled" {
		t.Fatalf("unroutable permission answered %q", kind)
	}

	h.writeAskOutcome(json.RawMessage(`4`), buildAskResult("skip", nil, "ask_user_question"))
	m := sink.next(t)
	res, _ := m["result"].(map[string]any)
	if res["outcome"] != "skip_interview" {
		t.Fatalf("unroutable ask answered %v", res)
	}

	h.writeMethodNotFound(json.RawMessage(`5`), "fs/read_text_file")
	m = sink.next(t)
	if _, ok := m["error"].(map[string]any); !ok {
		t.Fatalf("unknown method answered %v", m)
	}

	h.writePermOutcome(nil, "")
	h.writeAskOutcome(nil, nil)
	h.writeMethodNotFound(nil, "x")
	sink.quiet(t)
}

// An ask request with nothing renderable must be answered, not sat on until
// the turn times out.
func TestOfferAskWithNothingToShowRepliesSkip(t *testing.T) {
	s, sink := permSession(t)
	s.offerAsk(json.RawMessage(`21`), "session/elicitation", nil)

	m := sink.next(t)
	if got := string(mustJSON(t, m["id"])); got != "21" {
		t.Fatalf("answered id %s", got)
	}
	res, _ := m["result"].(map[string]any)
	if res["action"] != "cancel" {
		t.Fatalf("elicitation answered %v", res)
	}
}

// Answering when no card is up must not arm a reply for the next question.
func TestCompleteAskWithNoCardDoesNotStash(t *testing.T) {
	s, sink := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser
	s.live.Store(true)

	// Escape mid-turn with nothing pending: this used to cache a skip.
	s.completeAsk("skip", nil)
	sink.quiet(t)
	s.mu.Lock()
	stashed := s.askReply
	s.mu.Unlock()
	if stashed != nil {
		t.Fatal("skip was stashed with no card on screen")
	}

	// The next genuine question must reach the user.
	s.offerAsk(json.RawMessage(`31`), "ask_user_question", []askQuestion{{Question: "Ship it?"}})
	sink.quiet(t)
	if got, _ := bsink.next(t)["type"].(string); got != "ask" {
		t.Fatalf("browser got %q, wanted the question", got)
	}
}

// A card shown before the agent's request arrives keeps working: the answer
// is held and applied when the request lands.
func TestCompleteAskStashesForAnOfferedCard(t *testing.T) {
	s, sink := permSession(t)
	browser, _ := newSinkConn(t)
	s.browser = browser
	s.live.Store(true)

	s.offerAsk(nil, "", []askQuestion{{Question: "Pick one"}})
	s.completeAsk("accept", []askAnswer{{Question: "Pick one", Selected: []string{"a"}}})
	s.offerAsk(json.RawMessage(`41`), "ask_user_question", nil)

	m := sink.next(t)
	res, _ := m["result"].(map[string]any)
	if res["outcome"] != "accepted" {
		t.Fatalf("held answer not delivered: %v", res)
	}
}

// A turn starting must not inherit the previous turn's question state.
// The hub is killed first so prompt()'s RPC fails immediately instead of
// leaving a goroutine parked on the 10-minute timeout.
func TestPromptClearsStaleAskState(t *testing.T) {
	s, _ := permSession(t)
	browser, _ := newSinkConn(t)
	s.browser = browser
	s.hub.dead.Store(true)

	s.mu.Lock()
	s.askQ = []askQuestion{{Question: "stale"}}
	s.askReply = map[string]any{"outcome": "skip_interview"}
	s.askDone = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() { defer close(done); s.prompt("hi", nil) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not return")
	}

	s.mu.Lock()
	q, r, d := s.askQ, s.askReply, s.askDone
	s.mu.Unlock()
	if q != nil || r != nil || d {
		t.Fatalf("stale ask state survived: q=%v reply=%v done=%v", q, r, d)
	}
}

// A question already answered this turn must not be restated. The agent
// re-sends the ask tool's rawInput on later updates; showing it again
// would replace the answered card with a blank one.
func TestAnsweredQuestionIsNotReoffered(t *testing.T) {
	s, sink := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser
	s.live.Store(true)

	qs := []askQuestion{{Question: "Deploy?"}}
	s.offerAsk(json.RawMessage(`51`), "ask_user_question", qs)
	if got, _ := bsink.next(t)["type"].(string); got != "ask" {
		t.Fatalf("first offer sent %q", got)
	}
	s.completeAsk("accept", []askAnswer{{Question: "Deploy?", Selected: []string{"yes"}}})
	sink.next(t) // the answer goes to the agent

	// The re-offer that used to resurrect the card.
	s.offerAsk(nil, "", qs)
	bsink.quiet(t)
	sink.quiet(t)
}

// A completed or cancelled ask tool is not a live question.
func TestTerminalToolStatus(t *testing.T) {
	for _, s := range []string{"completed", "failed", "cancelled", "canceled", "REJECTED", " completed "} {
		if !terminalToolStatus(s) {
			t.Fatalf("%q should be terminal", s)
		}
	}
	for _, s := range []string{"", "in_progress", "pending"} {
		if terminalToolStatus(s) {
			t.Fatalf("%q should not be terminal", s)
		}
	}
}

// forwardUpdate must not re-offer the question once the tool has finished.
func TestForwardUpdateSkipsFinishedAskTool(t *testing.T) {
	s, _ := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser
	s.live.Store(true)
	s.busy.Store(true)

	raw := `{"sessionId":"s1","update":{"sessionUpdate":"tool_call_update","toolCallId":"t1",` +
		`"title":"ask_user_question","status":"%s","rawInput":{"questions":[{"question":"Go on?"}]}}}`

	s.forwardUpdate(fmt.Appendf(nil, raw, "in_progress"))
	if got, _ := bsink.next(t)["type"].(string); got != "ask" {
		t.Fatalf("live ask tool did not offer, got %q", got)
	}
	bsink.next(t) // the accompanying tool row

	s.forwardUpdate(fmt.Appendf(nil, raw, "completed"))
	if got, _ := bsink.next(t)["type"].(string); got != "tool" {
		t.Fatalf("completed ask tool re-offered the question: %q", got)
	}
	bsink.quiet(t)
}

// When grok resolves its own permission prompts, say so once.
func TestNoteInteractionWarnsOnAutoApproval(t *testing.T) {
	s, _ := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser

	s.noteInteraction([]byte(`{"sessionId":"s1","update":{"sessionUpdate":"pending_interaction","tool_call_id":"tc1","kind":"permission"}}`))
	s.noteInteraction([]byte(`{"sessionId":"s1","update":{"sessionUpdate":"interaction_resolved","tool_call_id":"tc1"}}`))

	m := bsink.next(t)
	if m["type"] != "warn" {
		t.Fatalf("expected a warning, got %v", m)
	}
	if txt, _ := m["text"].(string); !strings.Contains(txt, "auto-approve") {
		t.Fatalf("unhelpful warning: %q", txt)
	}

	// Once per session, not once per command.
	s.noteInteraction([]byte(`{"sessionId":"s1","update":{"sessionUpdate":"pending_interaction","tool_call_id":"tc2","kind":"permission"}}`))
	s.noteInteraction([]byte(`{"sessionId":"s1","update":{"sessionUpdate":"interaction_resolved","tool_call_id":"tc2"}}`))
	bsink.quiet(t)
}

// A permission pane actually handled is not evidence of auto-approval.
func TestNoteInteractionQuietWhenPaneWasAsked(t *testing.T) {
	s, _ := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser

	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"pending_interaction","tool_call_id":"tc9","kind":"permission"}}`))
	s.markPermHandled("tc9")
	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"interaction_resolved","tool_call_id":"tc9"}}`))
	bsink.quiet(t)

	// Junk and irrelevant shapes are ignored.
	s.noteInteraction([]byte(`{`))
	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"pending_interaction","kind":"permission"}}`))
	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"pending_interaction","tool_call_id":"x","kind":"plan"}}`))
	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"interaction_resolved","tool_call_id":"never-pending"}}`))
	s.markPermHandled("")
	bsink.quiet(t)
}

// hubWithAgent runs a real readLoop against a fake agent, so the dispatch
// in readLoop is exercised rather than the handler functions alone.
func hubWithAgent(t *testing.T) (*agentHub, *frameSink, chan []byte) {
	t.Helper()
	sink := &frameSink{ch: make(chan map[string]any, 64)}
	toPane := make(chan []byte, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			for b := range toPane {
				if c.WriteMessage(websocket.TextMessage, b) != nil {
					return
				}
			}
		}()
		go func() {
			defer c.Close()
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				var m map[string]any
				if json.Unmarshal(data, &m) == nil {
					sink.ch <- m
				}
			}
		}()
	}))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h := &agentHub{conn: c, pending: map[int64]chan json.RawMessage{}, sessions: map[string]*session{}}
	go h.readLoop()
	t.Cleanup(func() { h.shutdown() })
	return h, sink, toPane
}

// The readLoop must answer a request no session can own, and must route
// grok's interaction notifications into the auto-approve detector.
func TestReadLoopAnswersAndRoutes(t *testing.T) {
	h, sink, toPane := hubWithAgent(t)

	// Nobody is attached: the permission request still gets an answer.
	toPane <- []byte(`{"jsonrpc":"2.0","id":61,"method":"session/request_permission","params":{"sessionId":"ghost","options":[{"optionId":"allow_once","kind":"allow_once"}]}}`)
	m := sink.next(t)
	if kind, opt := outcome(t, m); kind != "cancelled" || opt != "" {
		t.Fatalf("unowned permission answered %q/%q", kind, opt)
	}

	// An unknown method with an id is answered too.
	toPane <- []byte(`{"jsonrpc":"2.0","id":62,"method":"fs/read_text_file","params":{}}`)
	if _, ok := sink.next(t)["error"].(map[string]any); !ok {
		t.Fatal("unknown method was not answered")
	}

	// The interaction notifications reach the session and raise the warning.
	browser, bsink := newSinkConn(t)
	s := &session{id: "s9", hub: h, browser: browser, pendPerm: map[string]bool{}, permSeen: map[string]bool{}}
	h.attach("s9", s)
	toPane <- []byte(`{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"s9","update":{"sessionUpdate":"pending_interaction","tool_call_id":"tc1","kind":"permission"}}}`)
	toPane <- []byte(`{"jsonrpc":"2.0","method":"_x.ai/session_notification","params":{"sessionId":"s9","update":{"sessionUpdate":"interaction_resolved","tool_call_id":"tc1"}}}`)
	if got, _ := bsink.next(t)["type"].(string); got != "warn" {
		t.Fatalf("auto-approval not detected through readLoop, got %q", got)
	}
}

// The agent documents this notification without the leading underscore.
func TestIsSessionNotify(t *testing.T) {
	if !isSessionNotify("_x.ai/session_notification") || !isSessionNotify("x.ai/session_notification") {
		t.Fatal("both spellings must route")
	}
	if isSessionNotify("session/update") || isSessionNotify("") {
		t.Fatal("unrelated methods must not route")
	}
}

// A resumed session is attached under two ids; requests must still reach it.
func TestLookupRequestFindsSessionAttachedTwice(t *testing.T) {
	h := &agentHub{sessions: map[string]*session{}}
	s := &session{id: "new-id"}
	s.busy.Store(true)
	h.attach("resume-id", s)
	h.attach("new-id", s)
	if h.lookupRequest("") != s {
		t.Fatal("one session under two ids must not read as ambiguous")
	}
	if h.lookupRequest("resume-id") != s || h.lookupRequest("new-id") != s {
		t.Fatal("explicit ids must resolve")
	}
}

// Idle tabs must neither claim an untagged request nor block the busy one.
func TestLookupRequestIgnoresIdleTabs(t *testing.T) {
	h := &agentHub{sessions: map[string]*session{}}
	busy := &session{id: "busy"}
	idle := &session{id: "idle"}
	busy.busy.Store(true)
	h.attach("busy", busy)
	h.attach("idle", idle)
	if h.lookupRequest("") != busy {
		t.Fatal("a lone busy session must receive the request even with idle tabs open")
	}
	idle.busy.Store(true)
	if h.lookupRequest("") != nil {
		t.Fatal("two busy sessions is ambiguous")
	}
}

// Closing the tab rejects an outstanding card rather than leaving the
// agent waiting, which is what the README promises.
func TestDropAnswersOutstandingCards(t *testing.T) {
	s, sink := permSession(t)
	h := s.hub
	h.attach("s1", s)
	s.offerPerm(json.RawMessage(`71`), "Execute `rm -rf /`", "rm -rf /", "allow_once", "")

	h.drop(s)
	if kind, opt := outcome(t, sink.next(t)); kind != "cancelled" || opt != "" {
		t.Fatalf("closing the tab answered %q/%q", kind, opt)
	}
}

// A bookkeeping overflow must never turn into a false accusation.
func TestNoWarningAfterPermSeenOverflow(t *testing.T) {
	s, _ := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser

	for i := range 300 {
		s.markPermHandled(fmt.Sprintf("tc-%d", i))
	}
	s.pendMu.Lock()
	lost := s.permLost
	s.pendMu.Unlock()
	if !lost {
		t.Fatal("overflow not recorded")
	}

	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"pending_interaction","tool_call_id":"late","kind":"permission"}}`))
	s.noteInteraction([]byte(`{"update":{"sessionUpdate":"interaction_resolved","tool_call_id":"late"}}`))
	bsink.quiet(t)
}

// replyPermission records the tool call so the resolve that follows is not
// mistaken for the agent approving itself.
func TestReplyPermissionMarksToolCall(t *testing.T) {
	s, sink := permSession(t)
	browser, _ := newSinkConn(t)
	s.browser = browser
	s.live.Store(true)

	raw := []byte(`{"params":{"options":[{"optionId":"allow_once","kind":"allow_once"}],
	 "toolCall":{"toolCallId":"tc42","title":"Execute ` + "`ls`" + `","kind":"execute","rawInput":{"command":"ls"}}}}`)
	s.replyPermission(json.RawMessage(`77`), raw)

	s.pendMu.Lock()
	seen := s.permSeen["tc42"]
	s.pendMu.Unlock()
	if !seen {
		t.Fatal("tool call not recorded")
	}
	// An execute tool is gated, so the card goes to the browser rather than
	// being answered on the spot.
	sink.quiet(t)
}
