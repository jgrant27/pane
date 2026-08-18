package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
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
	if askRes == nil || askRes["type"] != "accepted" {
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
