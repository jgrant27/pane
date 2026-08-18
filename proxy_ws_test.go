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
