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

// newTestProxy is a proxy with just enough state for the HTTP handlers.
func newTestProxy() *proxy {
	return &proxy{uploads: map[string]bool{}}
}

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
	h := newAgentHub(c)
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
	h := newAgentHub(nil)
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
	h := newAgentHub(c)

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
	h := newAgentHub(c)
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
	h := newAgentHub(nil)
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
	h := newAgentHub(nil)
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

// A tab leaving must not cancel a turn another tab is still watching. drop
// used to cancel for any busy session it dropped, so the socket that lost
// the race on reconnect killed the turn the user had just reattached to.
func TestDropDoesNotCancelWhileAnotherTabWatches(t *testing.T) {
	s, sink := permSession(t)
	h := s.hub
	other := &session{id: "s1", hub: h}
	h.attach("s1", s)
	h.attach("s1", other)
	s.busy.Store(true)

	h.drop(s)
	sink.quiet(t)

	other.busy.Store(true)
	h.drop(other)
	if m := sink.next(t); m["method"] != "session/cancel" {
		t.Fatalf("the last tab leaving must cancel the turn, got %v", m)
	}
}

// A model change the agent never completes must say so rather than leave
// the picker showing something that was never applied.
func TestSetModelReportsFailure(t *testing.T) {
	s, _ := permSession(t)
	browser, bsink := newSinkConn(t)
	s.browser = browser
	s.hub.dead.Store(true)

	s.setModel("grok-4.6")
	if m := bsink.next(t); m["type"] != "err" {
		t.Fatalf("a failed model change said %v", m)
	}
	if s.model == "grok-4.6" {
		t.Fatal("the model was never actually set")
	}

	s.setEffort("high")
	bsink.quiet(t)
	if s.effort == "high" {
		t.Fatal("the effort was never actually set")
	}
}

// deafBrowser is an upgraded socket whose peer never reads a byte: the
// backgrounded phone, the closed lid, the dropped Wi-Fi.
func deafBrowser(t *testing.T) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = c.Close() })
	}))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Writing to a browser must never park the writer. The shared agent
// readLoop writes here for every session, so one tab that stopped draining
// used to freeze output, RPC demuxing and new connections for all of them.
func TestStuckBrowserIsDroppedNotWaitedOn(t *testing.T) {
	h := newAgentHub(nil)
	s := &session{id: "stuck", hub: h, browser: deafBrowser(t)}
	h.attach("stuck", s)

	done := make(chan error, 1)
	go func() {
		big := strings.Repeat("x", 32<<10)
		var err error
		for i := 0; i < 4000 && err == nil; i++ {
			err = s.toBrowser(map[string]string{"type": "out", "text": big})
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a browser that never reads was never dropped")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("toBrowser parked on a browser that stopped reading")
	}
	if h.lookup("stuck") != nil {
		t.Fatal("updates are still routed to the dropped browser")
	}
}

// One id answered more than once must not park the read loop: it is the
// only reader of the agent socket, so every session on the hub stops with
// it. A pending call with no receiver is the state rpc is in between its
// answer arriving and its cleanup running.
func TestRepeatedResponseDoesNotFreezeTheHub(t *testing.T) {
	h, sink, toPane := hubWithAgent(t)
	h.mu.Lock()
	h.pending[7] = make(chan json.RawMessage, 1)
	h.mu.Unlock()

	for range 3 {
		toPane <- []byte(`{"jsonrpc":"2.0","id":7,"result":{}}`)
	}

	toPane <- []byte(`{"jsonrpc":"2.0","id":8,"method":"session/request_permission","params":{"sessionId":"ghost","options":[]}}`)
	if kind, _ := outcome(t, sink.next(t)); kind != "cancelled" {
		t.Fatalf("the hub stopped routing after a repeated response: %q", kind)
	}
}

// A prompt is deliberately given no useful deadline, so the hub dying is
// what has to free it — otherwise the tab sits on "working…" for hours.
func TestPromptUnblocksWhenTheHubDies(t *testing.T) {
	h, _, _ := hubWithAgent(t)
	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		raw, err := h.rpc("session/prompt", map[string]any{"sessionId": "s1"})
		done <- result{raw, err}
	}()
	time.Sleep(200 * time.Millisecond)
	h.shutdown()

	select {
	case got := <-done:
		if got.err == nil && rpcError(got.raw) == nil {
			t.Fatalf("a prompt freed by the agent dying reported success: %s", got.raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt still waiting on a hub that is gone")
	}
	if _, err := h.rpc("session/prompt", nil); err != errNoAgent {
		t.Fatalf("a call on a dead hub must not wait: %v", err)
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
	s.replyPermission(json.RawMessage(`77`), raw, true)

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

// permRequest is one gated tool call, tagged with the session it belongs to.
func permRequest(id int, sid string) []byte {
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"session/request_permission","params":{
	 "sessionId":%q,
	 "options":[{"optionId":"allow_once","kind":"allow_once"},{"optionId":"reject_once","kind":"reject_once"}],
	 "toolCall":{"toolCallId":"tc-%d","title":"Execute `+"`rm -rf /`"+`","kind":"execute","rawInput":{"command":"rm -rf /"}}}}`, id, sid, id))
}

// permTab is one browser watching an ACP session id, with a sink standing in
// for what that browser sees.
func permTab(t *testing.T, h *agentHub, sid string) (*session, *frameSink) {
	t.Helper()
	c, sink := newSinkConn(t)
	s := &session{id: sid, hub: h, browser: c, pendPerm: map[string]bool{}, permSeen: map[string]bool{}}
	h.attach(sid, s)
	return s, sink
}

// Two tabs on one session, both with a turn in flight, is a tie routing
// cannot break. It used to answer "cancelled", so neither browser was shown
// the card and the tool call was denied behind the user's back.
func TestTiedTabsBothSeeThePermissionCard(t *testing.T) {
	h, agent, toPane := hubWithAgent(t)
	a, aSink := permTab(t, h, "shared")
	b, bSink := permTab(t, h, "shared")
	a.busy.Store(true)
	b.busy.Store(true)

	toPane <- permRequest(81, "shared")
	for who, sink := range map[string]*frameSink{"a": aSink, "b": bSink} {
		if got := sink.next(t)["type"]; got != "perm" {
			t.Fatalf("tab %s saw %q instead of the card", who, got)
		}
	}
	// Nothing is answered yet: the user is looking at the card.
	agent.quiet(t)

	a.completePerm("allow")
	if kind, opt := outcome(t, agent.next(t)); kind != "selected" || opt != "allow_once" {
		t.Fatalf("the answered card resolved to %q/%q", kind, opt)
	}
	// The first answer is the answer. The other tab's stale card must not
	// reject the same request after it was allowed.
	b.completePerm("deny")
	b.clearPerm()
	agent.quiet(t)
}

// A tab whose write side is already shut cannot show a card, so routing must
// not pick it. Preferring the busy one handed the request to the socket that
// was leaving, and clearPerm denied it when the turn ended.
func TestCardSkipsADepartingBrowser(t *testing.T) {
	h, agent, toPane := hubWithAgent(t)
	leaving, _ := permTab(t, h, "shared")
	leaving.busy.Store(true)
	// The tab is on its way out: its prompt is still outstanding and routing
	// still holds it, but nothing can reach it any more. Reattaching after
	// the write side is shut is the window a teardown spends flushing.
	leaving.stopWriting()
	h.attach("shared", leaving)
	_, stayingSink := permTab(t, h, "shared")

	toPane <- permRequest(82, "shared")
	if got := stayingSink.next(t)["type"]; got != "perm" {
		t.Fatalf("the tab that could still show the card saw %q", got)
	}
	agent.quiet(t)
}

// The writer is the only thing that can notice a failed write. It used to
// exit leaving the write side open, so toBrowser went on reporting frames
// delivered into a queue nobody drains and the hub went on routing to a
// socket with no writer.
func TestFailedWriteEndsTheSession(t *testing.T) {
	h := newAgentHub(nil)
	c, _ := newSinkConn(t)
	s := &session{id: "dead", hub: h, browser: c}
	h.attach("dead", s)

	_ = c.Close()
	_ = s.toBrowser(map[string]string{"type": "out", "text": "first"})
	waitFor(t, 3*time.Second, "the writer to notice the failed write", s.gone)

	if err := s.toBrowser(map[string]string{"type": "out", "text": "second"}); err != errBrowserGone {
		t.Fatalf("a frame queued with no writer left reported %v", err)
	}
	if len(h.lookupAll("dead")) != 0 {
		t.Fatal("the hub still routes to a session whose writer is gone")
	}
}

// A browser that stops draining must not park its writer for ever: the write
// deadline is what turns a wedged socket into an error the session can act
// on.
func TestWriterGivesUpOnASocketThatNeverDrains(t *testing.T) {
	h := newAgentHub(nil)
	s := &session{id: "deaf", hub: h, browser: deafBrowser(t), writeWait: 50 * time.Millisecond}
	h.attach("deaf", s)

	// Fewer frames than the queue holds, so nothing here is the queue-full
	// path — what has to stop the writer is the deadline on the write.
	big := strings.Repeat("x", 128<<10)
	for range browserQueue - 6 {
		if err := s.toBrowser(map[string]any{"type": "out", "text": big}); err != nil {
			t.Fatalf("the queue filled: this is the wrong path, %v", err)
		}
	}
	waitFor(t, 5*time.Second, "the writer to give up on a socket that never drains", s.gone)
}

// A model switch writes the token budget from its own goroutine while usage
// updates read it on the hub's readLoop. Nothing switched model while totals
// were streaming, so the detector never had the two in flight together.
func TestUsageReadsTheContextSizeUnderTheLock(t *testing.T) {
	h, agent, toPane := hubWithAgent(t)
	s := &session{id: "ctx", hub: h, models: []modelInfo{{ID: "big", Context: 4096}}, contextN: 1024}
	s.live.Store(true)
	h.attach("ctx", s)

	usage := []byte(`{"sessionId":"ctx","update":{"sessionUpdate":"agent_message_chunk",
	 "content":{"text":"x"},"_meta":{"totalTokens":7}}}`)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				s.forwardUpdate(usage)
			}
		}
	}()

	go s.setModel("big")
	id, _ := agent.next(t)["id"].(float64)
	toPane <- []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, int64(id)))
	waitFor(t, 3*time.Second, "the switch to apply the new budget", func() bool {
		return s.contextSize() == 4096
	})
	close(stop)
	<-done
}

// Closing the write side flushes what is already queued: the last frame of a
// session is usually the one that explains why it ended.
func TestClosingFlushesQueuedFrames(t *testing.T) {
	c, sink := newSinkConn(t)
	s := &session{id: "flush", hub: newAgentHub(nil), browser: c, writeWait: browserWriteWait}
	// The queue is filled before the writer exists, so every frame is still
	// waiting when it starts and finds the session already closing. That is
	// the state startWriter would have handed it, minus the race.
	s.outCh = make(chan any, browserQueue)
	s.closing = make(chan struct{})
	s.written = make(chan struct{})
	for i := range 20 {
		s.outCh <- map[string]any{"type": "out", "text": fmt.Sprint(i)}
	}
	s.endWrites()
	go s.writeLoop()

	select {
	case <-s.written:
	case <-time.After(5 * time.Second):
		t.Fatal("the writer never finished")
	}
	for i := range 20 {
		if got := sink.next(t)["text"]; got != fmt.Sprint(i) {
			t.Fatalf("frame %d arrived as %v", i, got)
		}
	}
}
