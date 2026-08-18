package main

import (
	"encoding/base64"
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
	host, _ := os.Hostname()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":   "Grok Pane",
		"cwd":    p.cwd,
		"listen": r.Host,
		"host":   host,
		"ts":     tailscaleDNS(),
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

	agent, err := dialAgent(p.agentBase, p.secret, 8*time.Second)
	if err != nil {
		_ = writeJSON(browser, map[string]string{"type": "err", "text": err.Error()})
		return
	}
	defer agent.Close()

	s := &session{
		browser:    browser,
		agent:      agent,
		cwd:        p.sessionCwd(r),
		resumeID:   strings.TrimSpace(r.URL.Query().Get("sid")),
		replay:     r.URL.Query().Get("replay") == "1",
		wantModel:  strings.TrimSpace(r.URL.Query().Get("model")),
		wantEffort: strings.TrimSpace(r.URL.Query().Get("effort")),
	}
	if err := s.handshake(); err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
		return
	}
	log.Printf("session %s cwd=%s model=%s effort=%s", s.id, s.cwd, s.model, s.effort)
	s.live.Store(true)
	_ = s.toBrowser(s.readyPayload())
	s.loop()
}

type session struct {
	browser    *websocket.Conn
	agent      *websocket.Conn
	cwd        string
	id         string
	resumeID   string
	replay     bool
	wantModel  string
	wantEffort string
	model      string
	effort     string
	contextN   int
	models     []modelInfo
	imageCap   bool

	mu       sync.Mutex
	bmu      sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan json.RawMessage
	busy     atomic.Bool
	live     atomic.Bool
	prompted atomic.Bool

	askID    json.RawMessage
	askQ     []askQuestion
	askReply any
}

func (s *session) toBrowser(v any) error {
	if s == nil || s.browser == nil {
		return nil
	}
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
			"version": "0.2.1",
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
	s.imageCap = parsePromptCaps(init)

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
	s.applyModelState(res)
	s.applyWantedModel()
	return nil
}

func (s *session) applyModelState(res json.RawMessage) {
	st := parseSessionModels(res)
	if len(st.Models) > 0 {
		s.models = st.Models
	}
	if st.Current != "" {
		s.model = st.Current
	}
	if st.Effort != "" {
		s.effort = st.Effort
	}
	if st.Context > 0 {
		s.contextN = st.Context
	}
}

func (s *session) applyWantedModel() {
	if s.wantModel != "" && s.wantModel != s.model {
		if raw, err := s.rpc("session/set_model", map[string]any{"sessionId": s.id, "modelId": s.wantModel}); err == nil && rpcError(raw) == nil {
			s.model = s.wantModel
		}
	}
	effort := s.wantEffort
	if effort == "" {
		effort = s.effort
	}
	if effort != "" {
		_, _ = s.rpc("session/set_mode", map[string]any{"sessionId": s.id, "modeId": effort})
		s.effort = effort
	}
}

func (s *session) readyPayload() map[string]any {
	return map[string]any{
		"type":    "ready",
		"cwd":     s.cwd,
		"session": s.id,
		"model":   s.model,
		"effort":  s.effort,
		"context": s.contextN,
		"models":  s.models,
	}
}

func (s *session) replayHistory() {
	for _, ev := range replayUpdates(s.cwd, s.id, 20) {
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
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Cwd     string          `json:"cwd"`
			ID      string          `json:"id"`
			Files   []promptFile    `json:"files"`
			Action  string          `json:"action"`
			Answers json.RawMessage `json:"answers"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "in":
			if (msg.Text == "" && len(msg.Files) == 0) || s.busy.Load() {
				continue
			}
			go s.prompt(msg.Text, msg.Files)
		case "ask":
			s.completeAsk(msg.Action, parseAskAnswers(msg.Answers))
		case "cancel":
			s.completeAsk("skip", nil)
			s.notify("session/cancel", map[string]any{"sessionId": s.id})
		case "model":
			id := strings.TrimSpace(firstNonEmpty(msg.ID, msg.Text))
			if id == "" {
				continue
			}
			if raw, err := s.rpc("session/set_model", map[string]any{"sessionId": s.id, "modelId": id}); err == nil && rpcError(raw) == nil {
				s.model = id
				_ = s.toBrowser(map[string]any{"type": "model", "id": id, "models": s.models, "effort": s.effort, "context": s.contextFor(id)})
			} else if err != nil {
				_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
			}
		case "effort":
			id := strings.TrimSpace(firstNonEmpty(msg.ID, msg.Text))
			if id == "" {
				continue
			}
			if _, err := s.rpc("session/set_mode", map[string]any{"sessionId": s.id, "modeId": id}); err == nil {
				s.effort = id
				_ = s.toBrowser(map[string]any{"type": "effort", "id": id})
			}
		}
	}
}

func (s *session) prompt(text string, files []promptFile) {
	s.prompted.Store(true)
	s.busy.Store(true)
	_ = s.toBrowser(map[string]string{"type": "busy"})
	res, err := s.rpc("session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    buildPrompt(text, files, s.cwd, s.imageCap),
	})
	s.busy.Store(false)
	s.clearAsk()
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
			ID      json.RawMessage `json:"id"`
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
		if isAskMethod(env.Method) {
			s.offerAsk(env.ID, parseAskQuestions(env.Params))
			continue
		}
		if env.Method == "session/update" || env.Method == "x.ai/session/update" {
			s.forwardUpdate(env.Params)
			continue
		}
		if env.Method != "" && rpcIDSet(env.ID) {
			s.replyMethodNotFound(env.ID, env.Method)
			continue
		}
		if n, ok := rpcIDInt(env.ID); ok {
			s.mu.Lock()
			ch := s.pending[n]
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

func (s *session) replyPermission(id json.RawMessage, raw []byte) {
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
	if !rpcIDSet(id) {
		return
	}
	s.mu.Lock()
	_ = s.agent.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
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
		SessionUpdate string          `json:"sessionUpdate"`
		ToolCallID    string          `json:"toolCallId"`
		Title         string          `json:"title"`
		Status        string          `json:"status"`
		Kind          string          `json:"kind"`
		Content       any             `json:"content"`
		RawInput      json.RawMessage `json:"rawInput"`
		Meta          struct {
			TotalTokens int `json:"totalTokens"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(wrap.Update, &u); err != nil {
		return
	}
	if u.Meta.TotalTokens > 0 && s.live.Load() {
		_ = s.toBrowser(map[string]any{"type": "usage", "used": u.Meta.TotalTokens, "size": s.contextN})
	}
	if !s.live.Load() {
		return
	}
	askQ := parseAskQuestions(u.RawInput)
	askTool := len(askQ) > 0 || isAskTool(u.Title) || isAskTool(u.Kind)
	// session/load dumps the whole transcript as updates. We already
	// painted a short chat tail — ignore the flood until the user talks.
	// Mid-turn asks still have to land, or the agent sits on working….
	if s.resumeID != "" && !s.prompted.Load() && !s.busy.Load() && !askTool {
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
		if askTool && s.busy.Load() {
			s.offerAsk(nil, askQ)
		}
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

func (s *session) contextFor(id string) int {
	for _, m := range s.models {
		if m.ID == id && m.Context > 0 {
			s.contextN = m.Context
			return m.Context
		}
	}
	return s.contextN
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

type promptFile struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
}

func parsePromptCaps(raw json.RawMessage) bool {
	var wrap struct {
		AgentCapabilities struct {
			PromptCapabilities struct {
				Image bool `json:"image"`
			} `json:"promptCapabilities"`
		} `json:"agentCapabilities"`
	}
	_ = json.Unmarshal(raw, &wrap)
	return wrap.AgentCapabilities.PromptCapabilities.Image
}

func fileURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

func isImageMIME(m string) bool {
	return strings.HasPrefix(strings.ToLower(m), "image/")
}

const maxImageEmbed = 8 << 20

func buildPrompt(text string, files []promptFile, cwd string, imageCap bool) []map[string]any {
	var out []map[string]any
	text = strings.TrimSpace(text)
	if text == "" && len(files) == 1 {
		name := files[0].Name
		if name == "" {
			name = filepath.Base(files[0].Path)
		}
		text = "See attached file: " + name
	} else if text == "" && len(files) > 1 {
		text = "See attached files."
	}
	if text != "" {
		out = append(out, map[string]any{"type": "text", "text": text})
	}
	for _, f := range files {
		path := strings.TrimSpace(f.Path)
		if path == "" || !underCwd(cwd, path) {
			continue
		}
		name := f.Name
		if name == "" {
			name = filepath.Base(path)
		}
		link := map[string]any{
			"type": "resource_link",
			"uri":  fileURI(path),
			"name": name,
		}
		if f.Mime != "" {
			link["mimeType"] = f.Mime
		}
		if f.Size > 0 {
			link["size"] = f.Size
		}
		out = append(out, link)
		if !imageCap || !isImageMIME(f.Mime) {
			continue
		}
		st, err := os.Stat(path)
		if err != nil || st.Size() > maxImageEmbed {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{
			"type":     "image",
			"mimeType": f.Mime,
			"data":     base64.StdEncoding.EncodeToString(data),
			"uri":      fileURI(path),
		})
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "text", "text": text})
	}
	return out
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

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header,omitempty"`
	MultiSelect bool        `json:"multiSelect"`
	MultiSnake  bool        `json:"multi_select"`
	Options     []askOption `json:"options"`
}

func (q askQuestion) multi() bool { return q.MultiSelect || q.MultiSnake }

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
}

type askAnswer struct {
	Question string   `json:"question"`
	Selected []string `json:"selected"`
}

func isAskMethod(m string) bool {
	m = strings.ToLower(m)
	return strings.Contains(m, "ask_user") || strings.Contains(m, "askuserquestion")
}

func isAskTool(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	return strings.Contains(t, "ask_user") || strings.Contains(t, "ask user") ||
		strings.HasPrefix(t, "ask:") || t == "askuserquestion"
}

func rpcIDSet(id json.RawMessage) bool {
	s := strings.TrimSpace(string(id))
	return s != "" && s != "null"
}

func rpcIDInt(id json.RawMessage) (int64, bool) {
	if !rpcIDSet(id) {
		return 0, false
	}
	var n int64
	if err := json.Unmarshal(id, &n); err != nil {
		return 0, false
	}
	return n, true
}

func parseAskQuestions(raw json.RawMessage) []askQuestion {
	if !rpcIDSet(raw) {
		return nil
	}
	var wrap struct {
		Questions []askQuestion   `json:"questions"`
		RawInput  json.RawMessage `json:"rawInput"`
		Input     json.RawMessage `json:"input"`
		Params    json.RawMessage `json:"params"`
		Update    json.RawMessage `json:"update"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		var arr []askQuestion
		if json.Unmarshal(raw, &arr) == nil {
			return cleanAskQuestions(arr)
		}
		return nil
	}
	if qs := cleanAskQuestions(wrap.Questions); len(qs) > 0 {
		return qs
	}
	for _, inner := range []json.RawMessage{wrap.RawInput, wrap.Input, wrap.Params, wrap.Update} {
		if qs := parseAskQuestions(inner); len(qs) > 0 {
			return qs
		}
	}
	return nil
}

func cleanAskQuestions(in []askQuestion) []askQuestion {
	var out []askQuestion
	for _, q := range in {
		q.Question = strings.TrimSpace(q.Question)
		if q.Question == "" {
			q.Question = strings.TrimSpace(q.Header)
		}
		var opts []askOption
		for _, o := range q.Options {
			o.Label = strings.TrimSpace(o.Label)
			if o.Label == "" {
				continue
			}
			opts = append(opts, o)
		}
		q.Options = opts
		if q.Question == "" && len(q.Options) == 0 {
			continue
		}
		out = append(out, q)
	}
	return out
}

func parseAskAnswers(raw json.RawMessage) []askAnswer {
	if !rpcIDSet(raw) {
		return nil
	}
	var out []askAnswer
	if json.Unmarshal(raw, &out) == nil {
		return out
	}
	var labels []string
	if json.Unmarshal(raw, &labels) == nil {
		return []askAnswer{{Selected: labels}}
	}
	return nil
}

func buildAskResult(action string, answers []askAnswer) any {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "skip", "skip_interview", "dismiss":
		return map[string]any{"type": "skip_interview"}
	case "chat", "chat_about_this":
		return map[string]any{"type": "chat_about_this"}
	default:
		payload := make([]map[string]any, 0, len(answers))
		for _, a := range answers {
			payload = append(payload, map[string]any{
				"question":        a.Question,
				"selected":        a.Selected,
				"selectedOptions": a.Selected,
			})
		}
		return map[string]any{
			"type":            "accepted",
			"answers":         payload,
			"partial_answers": []any{},
		}
	}
}

func (s *session) offerAsk(id json.RawMessage, qs []askQuestion) {
	s.mu.Lock()
	if rpcIDSet(id) {
		if rpcIDSet(s.askID) && string(s.askID) != string(id) {
			old := append(json.RawMessage(nil), s.askID...)
			s.mu.Unlock()
			s.writeAskResult(old, buildAskResult("skip", nil))
			s.mu.Lock()
		}
		s.askID = append(json.RawMessage(nil), id...)
		if s.askReply != nil {
			reply := s.askReply
			s.askReply = nil
			id2 := s.askID
			s.askID = nil
			s.mu.Unlock()
			s.writeAskResult(id2, reply)
			return
		}
	}
	if len(qs) > 0 {
		s.askQ = qs
	}
	out := s.askQ
	s.mu.Unlock()
	_ = s.toBrowser(map[string]any{"type": "ask", "questions": out})
}

func (s *session) completeAsk(action string, answers []askAnswer) {
	result := buildAskResult(action, answers)
	s.mu.Lock()
	id := s.askID
	s.askID = nil
	if !rpcIDSet(id) {
		s.askReply = result
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.writeAskResult(id, result)
}

func (s *session) clearAsk() {
	s.mu.Lock()
	id := s.askID
	s.askID = nil
	s.askQ = nil
	s.askReply = nil
	s.mu.Unlock()
	if rpcIDSet(id) {
		s.writeAskResult(id, buildAskResult("skip", nil))
	}
}

func (s *session) writeAskResult(id json.RawMessage, result any) {
	if s == nil || s.agent == nil || !rpcIDSet(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.agent.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (s *session) replyMethodNotFound(id json.RawMessage, method string) {
	if s == nil || s.agent == nil || !rpcIDSet(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.agent.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": "Method not found: " + method,
		},
	})
}
