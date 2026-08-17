package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type proxy struct {
	agentBase string
	secret    string
	cwd       string
}

func (p *proxy) handleMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":   "Grok Pane",
		"cwd":    p.cwd,
		"listen": r.Host,
	})
}

func (p *proxy) sessionCwd(r *http.Request) string {
	q := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if q == "" {
		return p.cwd
	}
	abs, err := filepath.Abs(q)
	if err != nil {
		return p.cwd
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return p.cwd
	}
	return abs
}

func (p *proxy) handleWS(w http.ResponseWriter, r *http.Request) {
	browser, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}
	defer browser.Close()

	u, err := url.Parse(p.agentBase + "/ws")
	if err != nil {
		_ = writeJSON(browser, map[string]string{"type": "err", "text": err.Error()})
		return
	}
	q := u.Query()
	q.Set("server-key", p.secret)
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{HandshakeTimeout: 8 * time.Second}
	agent, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		_ = writeJSON(browser, map[string]string{"type": "err", "text": "agent: " + err.Error()})
		return
	}
	defer agent.Close()

	s := &session{
		browser:  browser,
		agent:    agent,
		cwd:      p.sessionCwd(r),
		resumeID: strings.TrimSpace(r.URL.Query().Get("sid")),
		replay:   r.URL.Query().Get("replay") == "1",
	}
	if err := s.handshake(); err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
		return
	}
	log.Printf("session %s cwd=%s", s.id, s.cwd)
	_ = s.toBrowser(map[string]any{"type": "ready", "cwd": s.cwd, "session": s.id})
	s.loop()
}

type session struct {
	browser  *websocket.Conn
	agent    *websocket.Conn
	cwd      string
	id       string
	resumeID string
	replay   bool

	mu      sync.Mutex
	bmu     sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan json.RawMessage
	busy    atomic.Bool
}

func (s *session) toBrowser(v any) error {
	s.bmu.Lock()
	defer s.bmu.Unlock()
	return s.browser.WriteJSON(v)
}

func (s *session) handshake() error {
	s.pending = map[int64]chan json.RawMessage{}
	go s.readAgent()

	init, err := s.rpc("initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]string{
			"name":    "grok-pane",
			"title":   "Grok Pane",
			"version": "0.2.0",
		},
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	})
	if err != nil {
		return err
	}
	if err := rpcError(init); err != nil {
		return err
	}

	meta := map[string]any{
		"yoloMode": true,
		"rules":    "You are reached through Grok Pane, a desktop face onto grok agent serve. Answer the user in the transcript. Do not narrate tool calls, status lines, or a tour of the working tree unless asked. No session chrome.",
	}
	params := map[string]any{
		"cwd":        s.cwd,
		"mcpServers": []any{},
		"_meta":      meta,
	}
	method := "session/new"
	if s.resumeID != "" {
		method = "session/load"
		params["sessionId"] = s.resumeID
	}
	res, err := s.rpc(method, params)
	if err != nil {
		return err
	}
	if err := rpcError(res); err != nil {
		return err
	}
	var out struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return err
	}
	if out.SessionID != "" {
		s.id = out.SessionID
	} else if s.resumeID != "" {
		s.id = s.resumeID
	}
	if s.resumeID != "" && s.replay {
		s.replayHistory()
	}
	return nil
}

func (s *session) replayHistory() {
	for _, ev := range replayUpdates(s.cwd, s.id, 400) {
		_ = s.toBrowser(map[string]string{"type": ev.Type, "text": ev.Text})
	}
}

func (s *session) loop() {
	for {
		_, data, err := s.browser.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Cwd  string `json:"cwd"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "in":
			if msg.Text == "" || s.busy.Load() {
				continue
			}
			go s.prompt(msg.Text)
		case "cancel":
			s.notify("session/cancel", map[string]any{"sessionId": s.id})
		}
	}
}

func (s *session) prompt(text string) {
	s.busy.Store(true)
	_ = s.toBrowser(map[string]string{"type": "busy"})
	res, err := s.rpc("session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	})
	s.busy.Store(false)
	if err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
	} else if err := rpcError(res); err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
	}
	_ = s.toBrowser(map[string]string{"type": "idle"})
}

func (s *session) rpc(method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()

	env := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	s.mu.Lock()
	err := s.agent.WriteJSON(env)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}

	select {
	case raw := <-ch:
		return raw, nil
	case <-time.After(10 * time.Minute):
		return nil, errTimeout
	}
}

func (s *session) notify(method string, params any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.agent.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

var errTimeout = timeoutErr("agent rpc timeout")

type timeoutErr string

func (e timeoutErr) Error() string { return string(e) }

func (s *session) readAgent() {
	for {
		_, data, err := s.agent.ReadMessage()
		if err != nil {
			_ = s.toBrowser(map[string]string{"type": "err", "text": "agent closed"})
			_ = s.browser.Close()
			return
		}
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int64          `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}

		if env.Method == "session/request_permission" {
			s.replyPermission(env.ID, data)
			continue
		}
		if env.Method == "session/update" || env.Method == "x.ai/session/update" {
			s.forwardUpdate(env.Params)
			continue
		}
		if env.ID != nil {
			s.mu.Lock()
			ch := s.pending[*env.ID]
			s.mu.Unlock()
			if ch != nil {
				if len(env.Error) > 0 && string(env.Error) != "null" {
					ch <- json.RawMessage(`{"error":` + string(env.Error) + `}`)
				} else {
					ch <- env.Result
				}
			}
		}
	}
}

func (s *session) replyPermission(id *int64, raw []byte) {
	var req struct {
		Params struct {
			Options []struct {
				OptionID string `json:"optionId"`
				Kind     string `json:"kind"`
				Name     string `json:"name"`
			} `json:"options"`
			ToolCall json.RawMessage `json:"toolCall"`
		} `json:"params"`
	}
	_ = json.Unmarshal(raw, &req)
	option := ""
	for _, o := range req.Params.Options {
		if o.Kind == "allow_once" || o.OptionID == "allow_once" || o.Kind == "allow_always" {
			option = o.OptionID
			break
		}
	}
	if option == "" && len(req.Params.Options) > 0 {
		option = req.Params.Options[0].OptionID
	}
	if id == nil {
		return
	}
	s.mu.Lock()
	_ = s.agent.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      *id,
		"result": map[string]any{
			"outcome": map[string]any{
				"outcome":  "selected",
				"optionId": option,
			},
		},
	})
	s.mu.Unlock()
}

func (s *session) forwardUpdate(params json.RawMessage) {
	var wrap struct {
		Update json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil || len(wrap.Update) == 0 {
		wrap.Update = params
	}
	var u struct {
		SessionUpdate string `json:"sessionUpdate"`
		ToolCallID    string `json:"toolCallId"`
		Title         string `json:"title"`
		Status        string `json:"status"`
		Content       any    `json:"content"`
	}
	if err := json.Unmarshal(wrap.Update, &u); err != nil {
		return
	}
	text := contentText(u.Content)
	switch u.SessionUpdate {
	case "agent_message_chunk":
		_ = s.toBrowser(map[string]string{"type": "out", "text": text})
	case "agent_thought_chunk":
		_ = s.toBrowser(map[string]string{"type": "thought", "text": text})
	case "user_message_chunk":
		// already echoed
	case "tool_call", "tool_call_update":
		title := u.Title
		if title == "" {
			title = "tool"
		}
		_ = s.toBrowser(map[string]string{
			"type":   "tool",
			"id":     u.ToolCallID,
			"text":   title,
			"status": u.Status,
		})
	}
}

func contentText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, item := range t {
			b.WriteString(contentText(item))
		}
		return b.String()
	case map[string]any:
		if s, ok := t["text"].(string); ok {
			return s
		}
		if inner, ok := t["content"]; ok {
			return contentText(inner)
		}
	}
	return ""
}

func rpcError(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var wrap struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil
	}
	if len(wrap.Error) == 0 || string(wrap.Error) == "null" {
		return nil
	}
	return fmt.Errorf("%s", wrap.Error)
}

func writeJSON(c *websocket.Conn, v any) error {
	return c.WriteJSON(v)
}
