package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func startMockAgent(t *testing.T, secret string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go mockACP(c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func mockACP(c *websocket.Conn) {
	defer c.Close()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &env) != nil || env.Method == "" {
			continue
		}
		if env.ID == nil {
			continue
		}
		switch env.Method {
		case "initialize":
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      *env.ID,
				"result": map[string]any{
					"agentCapabilities": map[string]any{
						"promptCapabilities": map[string]any{"image": true},
					},
				},
			})
		case "session/new", "session/load":
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"id":      *env.ID,
				"result": map[string]any{
					"sessionId": "01mocksessionxxxxxxxxxxxxxxxx",
					"models": map[string]any{
						"currentModelId": "grok-4.6",
						"availableModels": []any{
							map[string]any{"modelId": "grok-4.6", "name": "Grok", "_meta": map[string]any{"totalContextTokens": 1000}},
						},
					},
				},
			})
		case "session/set_model", "session/set_mode":
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		case "session/prompt":
			// Tagged with the session id, the way a real agent tags it: the
			// routing that matters is per session, not "whoever is around".
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": paramsSessionID(env.Params),
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"text": "hello"},
						"_meta":         map[string]any{"totalTokens": 12},
					},
				},
			})
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Read file", "status": "pending"},
				},
			})
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/request_permission",
				"id":      99,
				"params": map[string]any{
					"options": []any{
						map[string]any{"optionId": "allow_once", "kind": "allow_once", "name": "Allow"},
					},
				},
			})
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		default:
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		}
	}
}

func TestHandleWSPrompt(t *testing.T) {
	secret := "ws-secret"
	agent := startMockAgent(t, secret)
	dir := t.TempDir()
	p := &proxy{agentBase: agent, secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + dir + "&model=grok-4.6&effort=xhigh"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready map[string]any
	if err := c.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready["type"] != "ready" {
		t.Fatalf("%v", ready)
	}
	if err := c.WriteJSON(map[string]any{"type": "in", "text": "hi"}); err != nil {
		t.Fatal(err)
	}
	sawBusy, sawOut, sawIdle := false, false, false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !(sawBusy && sawOut && sawIdle) {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			break
		}
		switch msg["type"] {
		case "busy":
			sawBusy = true
		case "out":
			sawOut = true
		case "idle":
			sawIdle = true
		}
	}
	if !sawBusy || !sawOut || !sawIdle {
		t.Fatalf("busy=%v out=%v idle=%v", sawBusy, sawOut, sawIdle)
	}
	_ = c.WriteJSON(map[string]any{"type": "model", "id": "grok-4.6"})
	_ = c.WriteJSON(map[string]any{"type": "effort", "id": "low"})
	_ = c.WriteJSON(map[string]any{"type": "cancel"})
	_ = c.WriteJSON(map[string]any{"type": "in"})
	_ = c.WriteJSON(map[string]any{"type": "model"})
	_ = c.WriteJSON(map[string]any{"type": "effort"})
	_ = c.WriteMessage(websocket.TextMessage, []byte("{"))
	time.Sleep(50 * time.Millisecond)
}

func TestHandleWSAskUserQuestion(t *testing.T) {
	secret := "ask-secret"
	got := make(chan map[string]any, 4)
	answered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			var promptID *int64
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					return
				}
				var env struct {
					ID     *int64          `json:"id"`
					Method string          `json:"method"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if json.Unmarshal(data, &env) != nil {
					continue
				}
				if env.Method == "" && env.ID != nil {
					var res map[string]any
					_ = json.Unmarshal(env.Result, &res)
					if res == nil {
						res = map[string]any{}
					}
					if len(env.Error) > 0 && string(env.Error) != "null" {
						res["error"] = string(env.Error)
					}
					res["_id"] = float64(*env.ID)
					select {
					case got <- res:
					default:
					}
					if *env.ID == 77 {
						select {
						case <-answered:
						default:
							close(answered)
						}
						if promptID != nil {
							_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *promptID, "result": map[string]any{}})
							promptID = nil
						}
					}
					continue
				}
				if env.ID == nil || env.Method == "" {
					continue
				}
				switch env.Method {
				case "initialize":
					_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
				case "session/new", "session/load":
					_ = c.WriteJSON(map[string]any{
						"jsonrpc": "2.0", "id": *env.ID,
						"result": map[string]any{"sessionId": "01asksessionxxxxxxxxxxxxxxxxx"},
					})
				case "session/prompt":
					id := *env.ID
					promptID = &id
					_ = c.WriteJSON(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"update": map[string]any{
								"sessionUpdate": "tool_call",
								"toolCallId":    "ask1",
								"title":         "ask_user_question",
								"status":        "pending",
								"rawInput": map[string]any{
									"questions": []any{
										map[string]any{
											"question": "Which one?",
											"options": []any{
												map[string]any{"label": "Alpha", "description": "first"},
												map[string]any{"label": "Beta"},
											},
										},
									},
								},
							},
						},
					})
					_ = c.WriteJSON(map[string]any{
						"jsonrpc": "2.0",
						"id":      77,
						"method":  "GrokBuild:ask_user_question",
						"params": map[string]any{
							"questions": []any{
								map[string]any{
									"question": "Which one?",
									"options":  []any{map[string]any{"label": "Alpha"}, map[string]any{"label": "Beta"}},
								},
							},
						},
					})
					_ = c.WriteJSON(map[string]any{
						"jsonrpc": "2.0",
						"id":      88,
						"method":  "GrokBuild:mystery",
						"params":  map[string]any{},
					})
				default:
					_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
				}
			}
		}()
	}))
	t.Cleanup(srv.Close)

	p := &proxy{agentBase: "ws" + strings.TrimPrefix(srv.URL, "http"), secret: secret, cwd: t.TempDir()}
	hs := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(hs.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(hs.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready map[string]any
	if err := c.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready["type"] != "ready" {
		t.Fatalf("%v", ready)
	}
	if err := c.WriteJSON(map[string]any{"type": "in", "text": "ask me"}); err != nil {
		t.Fatal(err)
	}
	var sawAsk, sawTool bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !sawAsk {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			break
		}
		switch msg["type"] {
		case "ask":
			sawAsk = true
		case "tool":
			sawTool = true
		}
	}
	if !sawAsk {
		t.Fatal("expected ask card")
	}
	if err := c.WriteJSON(map[string]any{
		"type":   "ask",
		"action": "accept",
		"answers": []any{
			map[string]any{"question": "Which one?", "selected": []string{"Alpha"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var askRes, unknown map[string]any
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (askRes == nil || unknown == nil) {
		select {
		case res := <-got:
			if res["_id"] == float64(77) {
				askRes = res
			} else if res["_id"] == float64(88) {
				unknown = res
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	if askRes == nil || askRes["outcome"] != "accepted" {
		t.Fatalf("ask result %v tool=%v", askRes, sawTool)
	}
	if unknown == nil || unknown["error"] == nil {
		t.Fatalf("expected method-not-found, got %v", unknown)
	}
}

func TestHandleWSBadSecret(t *testing.T) {
	agent := startMockAgent(t, "right")
	p := &proxy{agentBase: agent, secret: "wrong", cwd: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "err" {
		t.Fatalf("%v", msg)
	}
}

func TestSessionCwdAndForward(t *testing.T) {
	dir := t.TempDir()
	p := &proxy{cwd: dir}
	req := httptest.NewRequest(http.MethodGet, "/ws?cwd="+dir, nil)
	if p.sessionCwd(req) != dir {
		t.Fatal(p.sessionCwd(req))
	}
	req = httptest.NewRequest(http.MethodGet, "/ws?cwd=/no/such/dir", nil)
	if p.sessionCwd(req) != dir {
		t.Fatal("fallback")
	}
	req = httptest.NewRequest(http.MethodGet, "/ws", nil)
	if p.sessionCwd(req) != dir {
		t.Fatal("empty")
	}

	s := &session{cwd: dir}
	s.forwardUpdate([]byte(`{"update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"think"}}}`))
	s.forwardUpdate([]byte(`{"sessionUpdate":"user_message_chunk"}`))
	s.forwardUpdate([]byte(`not-json`))
	if fileURI("rel") == "" || !isImageMIME("image/png") || isImageMIME("text/plain") {
		t.Fatal("file helpers")
	}
	if ctxn := (&session{models: []modelInfo{{ID: "m", Context: 9}}, contextN: 1}).contextFor("m"); ctxn != 9 {
		t.Fatal(ctxn)
	}
	if ctxn := (&session{contextN: 3}).contextFor("missing"); ctxn != 3 {
		t.Fatal(ctxn)
	}
}

func startSharedMockAgent(t *testing.T, secret string, upgrades *atomic.Int32) string {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		upgrades.Add(1)
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go sharedMockACP(c, &n)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func sharedMockACP(c *websocket.Conn, n *atomic.Int64) {
	defer c.Close()
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		var env struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &env) != nil || env.Method == "" || env.ID == nil {
			continue
		}
		switch env.Method {
		case "initialize":
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		case "session/new", "session/load":
			sid := fmt.Sprintf("01share%022d", n.Add(1))
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0", "id": *env.ID,
				"result": map[string]any{"sessionId": sid},
			})
		case "session/prompt":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(env.Params, &p)
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "mine:" + p.SessionID}},
				},
			})
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": "01FOREIGNSESSIONNOTOURSXXXX",
					"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "LEAK"}},
				},
			})
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "untagged"}},
				},
			})
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		default:
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		}
	}
}

func dialPaneSession(t *testing.T, paneURL, cwd string) (*websocket.Conn, string) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(paneURL, "http") + "/ws?cwd=" + cwd
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready map[string]any
	if err := c.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready["type"] != "ready" {
		t.Fatalf("ready %v", ready)
	}
	sid, _ := ready["session"].(string)
	if sid == "" {
		t.Fatalf("no session id: %v", ready)
	}
	return c, sid
}

func readUntilIdle(t *testing.T, c *websocket.Conn, d time.Duration) []string {
	t.Helper()
	var texts []string
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(400 * time.Millisecond))
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			continue
		}
		switch msg["type"] {
		case "out":
			if s, _ := msg["text"].(string); s != "" {
				texts = append(texts, s)
			}
		case "idle":
			return texts
		}
	}
	return texts
}

func drainOut(c *websocket.Conn, d time.Duration) []string {
	var texts []string
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(120 * time.Millisecond))
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			return texts
		}
		if msg["type"] == "out" {
			if s, _ := msg["text"].(string); s != "" {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

func TestHandleWSSharesAgentAndDoesNotCrossTalk(t *testing.T) {
	secret := "hub-secret"
	var upgrades atomic.Int32
	agent := startSharedMockAgent(t, secret, &upgrades)
	dir := t.TempDir()
	p := &proxy{agentBase: agent, secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)

	a, sidA := dialPaneSession(t, srv.URL, dir)
	b, sidB := dialPaneSession(t, srv.URL, dir)
	if sidA == sidB {
		t.Fatalf("same session id %s", sidA)
	}
	if upgrades.Load() != 1 {
		t.Fatalf("agent connections %d want 1", upgrades.Load())
	}

	if err := a.WriteJSON(map[string]any{"type": "in", "text": "hi"}); err != nil {
		t.Fatal(err)
	}
	got := readUntilIdle(t, a, 5*time.Second)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "mine:"+sidA) {
		t.Fatalf("missing own chunk: %v", got)
	}
	if !strings.Contains(joined, "untagged") {
		t.Fatalf("untagged should follow the busy session: %v", got)
	}
	if strings.Contains(joined, "LEAK") {
		t.Fatalf("foreign sessionId leaked onto A: %v", got)
	}
	if leak := drainOut(b, 300*time.Millisecond); len(leak) != 0 {
		t.Fatalf("B saw A's turn: %v", leak)
	}
}

func TestHandleWSResumeSession(t *testing.T) {
	secret := "resume-secret"
	agent := startMockAgent(t, secret)
	dir := t.TempDir()
	p := &proxy{agentBase: agent, secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?sid=01mocksessionxxxxxxxxxxxxxxxx&replay=1"
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready map[string]any
	if err := c.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready["type"] != "ready" || ready["session"] != "01mocksessionxxxxxxxxxxxxxxxx" {
		t.Fatalf("%v", ready)
	}
}

func TestEnsureHubAgentHangup(t *testing.T) {
	secret := "hangup-secret"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close()
	}))
	t.Cleanup(agent.Close)
	p := &proxy{agentBase: "ws" + strings.TrimPrefix(agent.URL, "http"), secret: secret, cwd: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "err" {
		t.Fatalf("%v", msg)
	}
}

// dialReady opens a pane socket at a full URL and waits for the ready frame.
func dialReady(t *testing.T, u string) *websocket.Conn {
	t.Helper()
	c, _ := dialReadyFrame(t, u)
	return c
}

// dialReadyFrame hands back the ready frame too, for tests that care what
// the tab was told about the session it just joined.
func dialReadyFrame(t *testing.T, u string) (*websocket.Conn, map[string]any) {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready map[string]any
	if err := c.ReadJSON(&ready); err != nil {
		t.Fatal(err)
	}
	if ready["type"] != "ready" {
		t.Fatalf("ready %v", ready)
	}
	return c, ready
}

// startHangingPromptAgent answers everything except session/prompt, which it
// counts and then sits on — the turn stays in flight for as long as the test
// needs it to.
func startHangingPromptAgent(t *testing.T, secret, sid string, prompts *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
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
				var env struct {
					ID     *int64 `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(data, &env) != nil || env.Method == "" || env.ID == nil {
					continue
				}
				if env.Method == "session/prompt" {
					prompts.Add(1)
					continue
				}
				res := map[string]any{}
				if env.Method == "session/new" || env.Method == "session/load" {
					res["sessionId"] = sid
				}
				_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": res})
			}
		}()
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// waitFor polls until the condition holds, or fails the test.
func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A turn belongs to the ACP session, and ACP runs one at a time. The guard
// was the socket's own busy flag, so once several tabs shared a session id
// each one passed its own guard and grok was asked to run two turns on one
// session with their replies interleaved.
func TestOneTurnPerSessionAcrossTabs(t *testing.T) {
	var prompts atomic.Int32
	secret, sid := "shared-turn", "01hangsessionxxxxxxxxxxxxxxxx"
	dir := t.TempDir()
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + dir + "&sid=" + sid

	a := dialReady(t, u)
	b := dialReady(t, u)
	if err := a.WriteJSON(map[string]any{"type": "in", "text": "one"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the first turn to reach the agent", func() bool {
		return prompts.Load() == 1
	})

	if err := b.WriteJSON(map[string]any{"type": "in", "text": "two"}); err != nil {
		t.Fatal(err)
	}
	// The second tab is told the session is working rather than having its
	// message quietly dropped.
	if got := waitFrame(t, b, "busy"); got == nil {
		t.Fatal("the second tab was never told the session is busy")
	}
	time.Sleep(300 * time.Millisecond)
	if n := prompts.Load(); n != 1 {
		t.Fatalf("two tabs on one session started %d turns", n)
	}
}

// A tab that joins mid-turn has never prompted anything itself, so only the
// session's own state can tell it the agent is working. Without it the
// composer comes up idle and offers to send into a turn already running.
func TestReadyTellsANewTabTheSessionIsBusy(t *testing.T) {
	var prompts atomic.Int32
	secret, sid := "ready-busy", "01readysessionxxxxxxxxxxxxxxx"
	dir := t.TempDir()
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + dir + "&sid=" + sid

	first, ready := dialReadyFrame(t, u)
	if ready["busy"] != false {
		t.Fatalf("an idle session reported %v", ready["busy"])
	}
	if err := first.WriteJSON(map[string]any{"type": "in", "text": "one"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "the turn to reach the agent", func() bool {
		return prompts.Load() == 1
	})

	if _, ready = dialReadyFrame(t, u); ready["busy"] != true {
		t.Fatalf("a tab joining mid-turn was told busy=%v", ready["busy"])
	}
}

func plantSessionUpdates(t *testing.T, cwd, sid, body string, age time.Duration) string {
	t.Helper()
	dir := filepath.Join(sessionGroupDir(cwd), sid)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "updates.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

// The TUI writes this session's updates.jsonl without going through pane's
// hub. Connecting mid-TUI-turn used to come back idle.
func TestReadyBusyWhenTUIIsWritingTheSession(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	secret, sid := "tui-busy", "01tuibusyxxxxxxxxxxxxxxxxxxx"
	cwd := t.TempDir()
	plantSessionUpdates(t, cwd, sid, "{}\n", 0)

	var prompts atomic.Int32
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: cwd}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + cwd + "&sid=" + sid
	_, ready := dialReadyFrame(t, u)
	if ready["busy"] != true {
		t.Fatalf("a TUI turn in progress reported busy=%v", ready["busy"])
	}
}

func TestReadyIdleWhenSessionFileIsStale(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	secret, sid := "tui-idle", "01tuiidlexxxxxxxxxxxxxxxxxxx"
	cwd := t.TempDir()
	plantSessionUpdates(t, cwd, sid, "{}\n", time.Hour)

	var prompts atomic.Int32
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: cwd}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + cwd + "&sid=" + sid
	_, ready := dialReadyFrame(t, u)
	if ready["busy"] != false {
		t.Fatalf("a stale session reported busy=%v", ready["busy"])
	}
}

func TestFollowDiskAnnouncesAForeignTurn(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	secret, sid := "tui-follow", "01tuifollowxxxxxxxxxxxxxxxxx"
	cwd := t.TempDir()
	path := plantSessionUpdates(t, cwd, sid, "{}\n", time.Hour)

	var prompts atomic.Int32
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: cwd}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + cwd + "&sid=" + sid
	c, ready := dialReadyFrame(t, u)
	if ready["busy"] != false {
		t.Fatalf("stale file reported busy=%v", ready["busy"])
	}
	line := `{"method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"agent_thought_chunk","content":{"text":"thinking"}}}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := waitFrame(t, c, "busy"); got == nil {
		t.Fatal("a TUI write on disk must show working…")
	}
}

func TestFollowDiskShowsTUIUserMessage(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	oldW, oldE := diskTurnWindow, diskFollowEvery
	t.Cleanup(func() { diskTurnWindow, diskFollowEvery = oldW, oldE })
	diskTurnWindow = time.Second
	diskFollowEvery = 15 * time.Millisecond

	secret, sid := "tui-you", "01tuiyouxxxxxxxxxxxxxxxxxxxx"
	cwd := t.TempDir()
	path := plantSessionUpdates(t, cwd, sid, "{}\n", time.Hour)

	var prompts atomic.Int32
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: cwd}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + cwd + "&sid=" + sid
	c, _ := dialReadyFrame(t, u)
	line := `{"method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"user_message_chunk","content":{"text":"continue"}}}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got := waitFrame(t, c, "you")
	if got == nil || got["text"] != "continue" {
		t.Fatalf("TUI ask must appear once, got %v", got)
	}
}

func TestFollowDiskDoesNotEchoOurOwnAsk(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	oldW, oldE := diskTurnWindow, diskFollowEvery
	t.Cleanup(func() { diskTurnWindow, diskFollowEvery = oldW, oldE })
	diskTurnWindow = time.Second
	diskFollowEvery = 15 * time.Millisecond

	secret, sid := "own-you", "01ownyouxxxxxxxxxxxxxxxxxxxx"
	cwd := t.TempDir()
	path := plantSessionUpdates(t, cwd, sid, "{}\n", time.Hour)

	var prompts atomic.Int32
	p := &proxy{agentBase: startHangingPromptAgent(t, secret, sid, &prompts), secret: secret, cwd: cwd}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?cwd=" + cwd + "&sid=" + sid
	c, _ := dialReadyFrame(t, u)
	if err := c.WriteJSON(map[string]any{"type": "in", "text": "continue"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, "our turn to start", func() bool { return prompts.Load() == 1 })
	line := `{"method":"session/update","params":{"sessionId":"` + sid + `","update":{"sessionUpdate":"user_message_chunk","content":{"text":"continue"}}}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := waitFrame(t, c, "you"); got != nil {
		t.Fatalf("pane already echoed the ask; disk tail sent another: %v", got)
	}
}

// waitFrame reads until a frame of the given type arrives.
func waitFrame(t *testing.T, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		var msg map[string]any
		if err := c.ReadJSON(&msg); err != nil {
			continue
		}
		if msg["type"] == want {
			return msg
		}
	}
	return nil
}

// Two browsers on one session id must both see the turn. Routing used to be
// a single slot per id, so the tab that attached last evicted the first and
// the reply reached neither of them: the first was unreachable, and the
// second threw it away as session/load transcript because it had never
// prompted anything itself.
func TestSecondTabOnOneSessionSeesTheReply(t *testing.T) {
	secret := "fanout-secret"
	agent := startMockAgent(t, secret)
	dir := t.TempDir()
	p := &proxy{agentBase: agent, secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)

	u := "ws" + strings.TrimPrefix(srv.URL, "http") +
		"/ws?cwd=" + dir + "&sid=01mocksessionxxxxxxxxxxxxxxxx"
	a := dialReady(t, u)
	b := dialReady(t, u)

	if err := a.WriteJSON(map[string]any{"type": "in", "text": "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := readUntilIdle(t, a, 5*time.Second); !strings.Contains(strings.Join(got, "\n"), "hello") {
		t.Fatalf("the tab that prompted saw %v", got)
	}
	if got := drainOut(b, 2*time.Second); !strings.Contains(strings.Join(got, "\n"), "hello") {
		t.Fatalf("the other tab on the same session saw %v", got)
	}
}

// startCountingAgent answers everything at once and counts the prompts, so a
// test can tell how many turns pane actually started.
func startCountingAgent(t *testing.T, secret string, prompts *atomic.Int32) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
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
				var env struct {
					ID     *int64 `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(data, &env) != nil || env.Method == "" || env.ID == nil {
					continue
				}
				if env.Method == "session/prompt" {
					prompts.Add(1)
				}
				res := map[string]any{}
				if env.Method == "session/new" || env.Method == "session/load" {
					res["sessionId"] = "01countsessionxxxxxxxxxxxxxxx"
				}
				_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": res})
			}
		}()
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// Two prompts sent back to back must start one turn. busy used to be set by
// the prompt goroutine rather than by the reader, so the next message could
// be read while the flag still said idle and a second turn started on the
// same ACP session. Holding s.mu parks that goroutine exactly where the old
// code set the flag, which is the window the race lives in.
func TestBackToBackPromptsStartOneTurn(t *testing.T) {
	var prompts atomic.Int32
	secret := "one-turn"
	dir := t.TempDir()
	p := &proxy{agentBase: startCountingAgent(t, secret, &prompts), secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)

	c, sid := dialPaneSession(t, srv.URL, dir)
	p.hubMu.Lock()
	h := p.hub
	p.hubMu.Unlock()
	s := h.lookup(sid)
	if s == nil {
		t.Fatal("session not attached")
	}

	s.mu.Lock()
	if err := c.WriteJSON(map[string]any{"type": "in", "text": "one"}); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteJSON(map[string]any{"type": "in", "text": "two"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	s.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && prompts.Load() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if n := prompts.Load(); n != 1 {
		t.Fatalf("two messages started %d turns on one session", n)
	}
}

// A wedged agent must be dialled once per attempt, not once per tab.
// ensureHub used to hold its mutex across the dial and initialize, so every
// tab queued behind the whole failing attempt and then repeated it.
func TestEnsureHubDialsOncePerAttempt(t *testing.T) {
	var dials atomic.Int32
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		time.Sleep(300 * time.Millisecond)
		http.Error(w, "no agent", http.StatusServiceUnavailable)
	}))
	t.Cleanup(agent.Close)
	p := &proxy{agentBase: "ws" + strings.TrimPrefix(agent.URL, "http"), secret: "s", cwd: t.TempDir()}

	start := time.Now()
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = p.ensureHub()
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err == nil {
			t.Fatalf("waiter %d was handed a hub by a dead agent", i)
		}
	}
	if n := dials.Load(); n != 1 {
		t.Fatalf("one attempt dialled the agent %d times", n)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("tabs serialised behind each other: %v", elapsed)
	}
}

// Changing the model must not stop pane reading the browser. model and
// effort used to call the agent inline on the only goroutine reading that
// socket, so Allow/Deny, answers and Escape sat unread behind them — while
// the agent was often blocked on the very permission being answered.
func TestModelRPCDoesNotStallTheReader(t *testing.T) {
	secret := "stall-secret"
	seen := make(chan string, 32)
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
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
				var env struct {
					ID     *int64 `json:"id"`
					Method string `json:"method"`
				}
				if json.Unmarshal(data, &env) != nil || env.Method == "" {
					continue
				}
				select {
				case seen <- env.Method:
				default:
				}
				// The wedged call: never answered, exactly like an agent
				// that is mid-turn waiting on a permission reply.
				if env.Method == "session/set_model" || env.ID == nil {
					continue
				}
				res := map[string]any{}
				if env.Method == "session/new" || env.Method == "session/load" {
					res["sessionId"] = "01stallsessionxxxxxxxxxxxxxxx"
				}
				_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": res})
			}
		}()
	}))
	t.Cleanup(agent.Close)

	dir := t.TempDir()
	p := &proxy{agentBase: "ws" + strings.TrimPrefix(agent.URL, "http"), secret: secret, cwd: dir}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	c, _ := dialPaneSession(t, srv.URL, dir)

	if err := c.WriteJSON(map[string]any{"type": "model", "id": "grok-4.6"}); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteJSON(map[string]any{"type": "cancel"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case m := <-seen:
			if m == "session/cancel" {
				return
			}
		case <-deadline:
			t.Fatal("cancel never reached the agent: the reader was parked on session/set_model")
		}
	}
}

func TestHubAgentDeathClosesBrowsers(t *testing.T) {
	secret := "die-secret"
	agent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go mockACP(c)
	}))
	p := &proxy{agentBase: "ws" + strings.TrimPrefix(agent.URL, "http"), secret: secret, cwd: t.TempDir()}
	srv := httptest.NewServer(http.HandlerFunc(p.handleWS))
	t.Cleanup(srv.Close)
	c, _ := dialPaneSession(t, srv.URL, p.cwd)
	agent.Close()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	var msg map[string]any
	if err := c.ReadJSON(&msg); err != nil {
		return
	}
	if msg["type"] != "err" {
		t.Fatalf("%v", msg)
	}
}
