package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	hubMu   sync.Mutex
	hub     *agentHub
	dialing *hubDial

	// Files pane copied into a project on the user's behalf, and so may
	// remove again on request.
	upMu    sync.Mutex
	uploads map[string]bool
}

// One ACP WebSocket to grok agent serve. Pane used to dial a new agent
// socket per browser tab; grok's persistent agent then replaced the
// actor and the in-flight reply landed in whichever project you had
// just switched to.
type agentHub struct {
	conn     *websocket.Conn
	imageCap bool
	dead     atomic.Bool

	wmu     sync.Mutex
	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan json.RawMessage
	// Every browser watching an ACP session id, not just the last one to
	// arrive. A reconnect or a second tab used to overwrite the routing
	// entry, and the reply to a turn already in flight reached nobody.
	sessions map[string]map[*session]struct{}
	// How long a call waits, by method. nil means the standard table.
	deadline func(string) time.Duration
	// The ACP session ids with a turn in flight. A turn belongs to the
	// session, not to one socket: several browsers share an id, so this is
	// both the single-turn guard and the answer to "is this session working?"
	// for a tab that never prompted anything itself.
	turns map[string]bool
}

func newAgentHub(conn *websocket.Conn) *agentHub {
	return &agentHub{
		conn:     conn,
		pending:  map[int64]chan json.RawMessage{},
		sessions: map[string]map[*session]struct{}{},
		turns:    map[string]bool{},
	}
}

func (p *proxy) handleMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	host, _ := os.Hostname()
	lastCwd, lastSid, lastTitle := lastGrok()
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":      "Grok Pane",
		"cwd":       p.cwd,
		"listen":    r.Host,
		"host":      host,
		"ts":        tailscaleDNS(),
		"lastCwd":   lastCwd,
		"lastSid":   lastSid,
		"lastTitle": lastTitle,
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

	h, err := p.ensureHub()
	if err != nil {
		_ = writeJSON(browser, map[string]string{"type": "err", "text": err.Error()})
		return
	}

	s := &session{
		browser:    browser,
		hub:        h,
		cwd:        p.sessionCwd(r),
		resumeID:   strings.TrimSpace(r.URL.Query().Get("sid")),
		replay:     r.URL.Query().Get("replay") == "1",
		wantModel:  strings.TrimSpace(r.URL.Query().Get("model")),
		wantEffort: strings.TrimSpace(r.URL.Query().Get("effort")),
		pendPerm:   map[string]bool{},
		permSeen:   map[string]bool{},
	}
	defer h.drop(s)
	// Runs before the deferred Close above it, so the frame that says what
	// went wrong is on the wire before the socket goes away.
	defer s.stopWriting()
	if err := s.handshake(); err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
		return
	}
	log.Printf("session %s cwd=%s model=%s effort=%s", s.id, s.cwd, s.model, s.effort)
	s.live.Store(true)
	_ = s.toBrowser(s.readyPayload())
	s.startFollow()
	s.loop()
}

// hubDial is one attempt to bring the shared agent connection up. Both the
// dial and initialize block on the network, so they happen outside hubMu:
// held across them, a wedged agent made every new tab queue behind the last
// one's full timeout and then re-dial it — N tabs waited N×8s to be told the
// same thing. Now they share the attempt that is already running.
type hubDial struct {
	done chan struct{}
	h    *agentHub
	err  error
}

func (p *proxy) ensureHub() (*agentHub, error) {
	p.hubMu.Lock()
	if p.hub != nil && !p.hub.dead.Load() {
		h := p.hub
		p.hubMu.Unlock()
		return h, nil
	}
	if d := p.dialing; d != nil {
		p.hubMu.Unlock()
		<-d.done
		return d.h, d.err
	}
	d := &hubDial{done: make(chan struct{})}
	p.dialing = d
	p.hubMu.Unlock()

	h, err := p.newHub()

	p.hubMu.Lock()
	d.h, d.err = h, err
	if err == nil {
		p.hub = h
	}
	p.dialing = nil
	p.hubMu.Unlock()
	close(d.done)
	return h, err
}

func (p *proxy) newHub() (*agentHub, error) {
	conn, err := dialAgent(p.agentBase, p.secret, 8*time.Second)
	if err != nil {
		return nil, err
	}
	h := newAgentHub(conn)
	go h.readLoop()
	init, err := h.rpc("initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo": map[string]string{
			"name":    "grok-pane",
			"title":   "Grok Pane",
			"version": "0.2.15",
		},
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
	})
	if err != nil {
		h.shutdown()
		return nil, err
	}
	if err := rpcError(init); err != nil {
		h.shutdown()
		return nil, err
	}
	h.imageCap = parsePromptCaps(init)
	return h, nil
}

type session struct {
	browser    *websocket.Conn
	hub        *agentHub
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

	// Frames for the browser go through a bounded queue drained by one
	// writer goroutine, so whoever produced the frame — including the single
	// readLoop that feeds every session on the shared agent connection —
	// hands it off instead of waiting on a socket it does not own.
	wOnce   sync.Once
	cOnce   sync.Once
	outCh   chan any
	closing chan struct{}
	written chan struct{}
	// The same fact as a closed closing channel, readable from anywhere.
	// Routing has to skip a browser it can no longer write to, and it holds
	// the hub lock while it decides.
	closed atomic.Bool
	// How long one frame gets to reach the browser before the socket is
	// called dead. Filled in when the writer starts, so a test can set it to
	// milliseconds and not spend ten seconds proving the deadline is there.
	writeWait time.Duration

	mu       sync.Mutex
	busy     atomic.Bool
	live     atomic.Bool
	prompted atomic.Bool
	autoWarn atomic.Bool
	// Tailing grok's updates.jsonl so a TUI turn on this session shows as
	// working… instead of an idle composer. followStop is closed from drop.
	following  atomic.Bool
	followStop chan struct{}
	fOnce      sync.Once

	// Permission interactions the agent announced, and the subset it
	// actually asked us to decide. A resolve with no matching ask means
	// grok approved it on its own.
	pendMu   sync.Mutex
	pendPerm map[string]bool
	permSeen map[string]bool
	permLost bool

	askID     json.RawMessage
	askMethod string
	askQ      []askQuestion
	askReply  any
	askDone   bool
	permID    json.RawMessage
	permAllow string
	permDeny  string
	permOpts  []permChoice
}

type permChoice struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

const (
	// Deep enough for a burst of stream chunks, shallow enough that a
	// browser which has genuinely stopped reading is noticed rather than
	// buffered forever.
	browserQueue     = 256
	browserWriteWait = 10 * time.Second
)

// toBrowser queues a frame for this session's browser. It never blocks: a
// tab that stopped draining — backgrounded phone, dead Wi-Fi, closed lid —
// used to park whichever goroutine wrote to it, and the readLoop that feeds
// every other session writes here too.
func (s *session) toBrowser(v any) error {
	if s == nil || s.browser == nil {
		return nil
	}
	s.wOnce.Do(s.startWriter)
	select {
	case <-s.closing:
		return errBrowserGone
	default:
	}
	select {
	case s.outCh <- v:
		return nil
	default:
		// The queue is full, so this browser is not keeping up with its own
		// output. Dropping it is the only answer that does not charge every
		// other session for it.
		s.dropBrowser()
		return errBrowserGone
	}
}

func (s *session) startWriter() {
	if s.writeWait <= 0 {
		s.writeWait = browserWriteWait
	}
	s.outCh = make(chan any, browserQueue)
	s.closing = make(chan struct{})
	s.written = make(chan struct{})
	go s.writeLoop()
}

func (s *session) writeLoop() {
	defer func() {
		// However this writer got here — a failed write as much as a close —
		// nothing will ever be sent again. Leaving the write side open would
		// have toBrowser report frames delivered into a queue with no drainer,
		// and leave the hub routing cards to a socket that cannot show them.
		s.endWrites()
		s.hub.detach(s)
		_ = s.browser.Close()
		close(s.written)
	}()
	for {
		select {
		case v := <-s.outCh:
			if s.writeFrame(v) != nil {
				return
			}
		case <-s.closing:
			// Flush what is already queued: the last frame of a session is
			// usually the one that explains why it ended. The write deadline
			// bounds how long that courtesy can take.
			for {
				select {
				case v := <-s.outCh:
					if s.writeFrame(v) != nil {
						return
					}
				default:
					return
				}
			}
		}
	}
}

func (s *session) writeFrame(v any) error {
	// Without a deadline a full kernel send buffer blocks WriteJSON with no
	// way back out.
	_ = s.browser.SetWriteDeadline(time.Now().Add(s.writeWait))
	return s.browser.WriteJSON(v)
}

// endWrites shuts the write side, once, from whichever goroutine gets here
// first. Callers must have started the writer.
func (s *session) endWrites() {
	s.cOnce.Do(func() {
		s.closed.Store(true)
		close(s.closing)
	})
}

// gone reports a browser that can no longer be written to. Routing asks
// before it picks one: a card queued on a dead socket is a card the user is
// never shown, and clearPerm denies it on their behalf when the turn ends.
func (s *session) gone() bool {
	return s == nil || s.closed.Load()
}

// dropBrowser cuts a browser loose and unhooks it from the hub, so routing
// stops pointing at a socket nobody is reading.
func (s *session) dropBrowser() {
	s.endWrites()
	_ = s.browser.Close()
	s.hub.detach(s)
}

// stopWriting ends the session's write side: the queue is flushed, then the
// socket is closed. It waits for that to finish so a caller closing the
// connection behind it cannot cut the last frame off.
func (s *session) stopWriting() {
	if s == nil || s.browser == nil {
		return
	}
	s.wOnce.Do(s.startWriter)
	s.endWrites()
	select {
	case <-s.written:
	// One frame's deadline is all the flush can be waiting on; past that the
	// writer is not coming back and the teardown must not queue behind it.
	case <-time.After(s.writeWait):
	}
}

func (s *session) handshake() error {
	if s.hub != nil {
		s.imageCap = s.hub.imageCap
	}
	if s.hub != nil && s.resumeID != "" {
		s.hub.attach(s.resumeID, s)
	}

	meta := map[string]any{
		"yoloMode":       false,
		"permissionMode": "default",
		"rules":          "You are reached through Grok Pane, a desktop face onto grok agent serve. Answer the user in the transcript. Do not narrate tool calls, status lines, or a tour of the working tree unless asked. No session chrome.",
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
	if s.hub != nil && s.id != "" {
		s.hub.attach(s.id, s)
	}
	if s.id != "" && s.cwd != "" {
		rememberFocus(s.cwd, s.id, "") // #59
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
	busy := s.hub.turnActive(s.id) || diskTurnFresh(s.cwd, s.id)
	if busy {
		s.busy.Store(true)
	}
	return map[string]any{
		"type":    "ready",
		"cwd":     s.cwd,
		"session": s.id,
		"model":   s.model,
		"effort":  s.effort,
		"context": s.contextN,
		"models":  s.models,
		// Whether the session is working, not whether this socket has ever
		// prompted. A tab that reconnects mid-turn has never prompted
		// anything itself and used to be handed an idle composer. The TUI
		// writes the same session's updates.jsonl without going through
		// this hub, so disk freshness is the other half of the answer.
		"busy": busy,
	}
}

func (s *session) replayHistory() {
	for _, ev := range replayUpdates(s.cwd, s.id, 20) {
		frame := map[string]any{"type": ev.Type, "text": ev.Text}
		if ev.At > 0 {
			frame["at"] = ev.At
		}
		_ = s.toBrowser(frame)
	}
}

// How recently grok must have written updates.jsonl for the session to
// count as working when this hub did not start the turn. The TUI streams
// thoughts and tools at least this often; a longer gap is a finished turn.
var diskTurnWindow = 3 * time.Second

var diskFollowEvery = 250 * time.Millisecond

func diskTurnFresh(cwd, id string) bool {
	if cwd == "" || id == "" {
		return false
	}
	st, err := os.Stat(filepath.Join(sessionGroupDir(cwd), id, "updates.jsonl"))
	if err != nil {
		return false
	}
	return time.Since(st.ModTime()) < diskTurnWindow
}

func (s *session) startFollow() {
	if s == nil || s.id == "" || s.cwd == "" {
		return
	}
	path := filepath.Join(sessionGroupDir(s.cwd), s.id, "updates.jsonl")
	var off int64
	if st, err := os.Stat(path); err == nil {
		off = st.Size()
	}
	stop := make(chan struct{})
	s.followStop = stop
	every, window := diskFollowEvery, diskTurnWindow
	go s.followDisk(path, off, stop, every, window)
}

func (s *session) stopFollow() {
	if s == nil {
		return
	}
	s.fOnce.Do(func() {
		if s.followStop != nil {
			close(s.followStop)
		}
	})
}

func (s *session) followDisk(path string, off int64, stop <-chan struct{}, every, window time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var rest []byte
	announced := false
	lastGrow := time.Time{}
	if st, err := os.Stat(path); err == nil {
		lastGrow = st.ModTime()
		announced = time.Since(lastGrow) < window
	}
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			st, err := os.Stat(path)
			if err != nil {
				continue
			}
			if st.Size() < off {
				off = 0
				rest = nil
			}
			if st.Size() > off {
				f, err := os.Open(path)
				if err != nil {
					continue
				}
				_, err = f.Seek(off, io.SeekStart)
				if err != nil {
					f.Close()
					continue
				}
				chunk, err := io.ReadAll(f)
				f.Close()
				if err != nil && len(chunk) == 0 {
					continue
				}
				off += int64(len(chunk))
				lastGrow = time.Now()
				own := s.hub.turnActive(s.id)
				if !own && !announced {
					announced = true
					s.busy.Store(true)
					_ = s.toBrowser(map[string]string{"type": "busy", "session": s.id})
				}
				// This hub already streams our own turn over ACP. Replaying
				// the same lines from disk double-echoes the ask (and the
				// reply). Consume the bytes so they are not applied later.
				if own {
					rest = nil
					continue
				}
				rest = append(rest, chunk...)
				parts := bytes.Split(rest, []byte("\n"))
				rest = parts[len(parts)-1]
				s.following.Store(true)
				for _, line := range parts[:len(parts)-1] {
					s.applyDiskLine(line)
				}
				s.following.Store(false)
			}
			if announced && !s.hub.turnActive(s.id) && time.Since(lastGrow) >= window {
				announced = false
				if s.busy.CompareAndSwap(true, false) {
					_ = s.toBrowser(map[string]string{"type": "idle", "session": s.id})
				}
			}
		}
	}
}

func (s *session) applyDiskLine(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var ev struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	if ev.Method != "session/update" && ev.Method != "_x.ai/session/update" {
		return
	}
	s.forwardUpdate(ev.Params)
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
			if msg.Text == "" && len(msg.Files) == 0 {
				continue
			}
			// The turn is claimed here, not in the goroutine: prompt() could
			// be scheduled after the next message had already been read, and
			// two turns then ran on one ACP session with their replies
			// interleaved.
			if !s.busy.CompareAndSwap(false, true) {
				_ = s.toBrowser(map[string]string{"type": "busy"})
				continue
			}
			// busy guards one socket, and several sockets now share one ACP
			// session id: two tabs each passed their own guard and grok ran
			// two turns on one session. The hub is the only thing that sees
			// all of them, so the turn is claimed there too.
			if !s.hub.claimTurn(s.id) {
				s.busy.Store(false)
				// Say so rather than swallowing the message: the composer has
				// to stop offering to send what pane is not going to send.
				_ = s.toBrowser(map[string]string{"type": "busy"})
				continue
			}
			go s.prompt(msg.Text, msg.Files)
		case "ask":
			s.completeAsk(msg.Action, parseAskAnswers(msg.Answers))
		case "perm":
			s.completePerm(msg.Action)
		case "cancel":
			s.completePerm("deny")
			s.completeAsk("skip", nil)
			s.notify("session/cancel", map[string]any{"sessionId": s.id})
		case "model":
			id := strings.TrimSpace(firstNonEmpty(msg.ID, msg.Text))
			if id == "" {
				continue
			}
			// Off the reader, the way "in" already is. This loop is the only
			// thing reading the browser socket: an RPC run inline leaves the
			// Allow/Deny click, the answer and Escape sitting unread — and
			// the agent may be blocked on exactly the one being answered.
			go s.setModel(id)
		case "effort":
			id := strings.TrimSpace(firstNonEmpty(msg.ID, msg.Text))
			if id == "" {
				continue
			}
			go s.setEffort(id)
		}
	}
}

func (s *session) setModel(id string) {
	raw, err := s.rpc("session/set_model", map[string]any{"sessionId": s.id, "modelId": id})
	if err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
		return
	}
	if rpcError(raw) != nil {
		return
	}
	// Off the reader, a model change and an effort change can now be in
	// flight together, so the state they share takes turns. The RPC above
	// stays outside the lock — that is the wait this fix was about.
	s.mu.Lock()
	s.model = id
	frame := map[string]any{"type": "model", "id": id, "models": s.models, "effort": s.effort, "context": s.contextFor(id)}
	s.mu.Unlock()
	_ = s.toBrowser(frame)
}

func (s *session) setEffort(id string) {
	if _, err := s.rpc("session/set_mode", map[string]any{"sessionId": s.id, "modeId": id}); err != nil {
		return
	}
	s.mu.Lock()
	s.effort = id
	s.mu.Unlock()
	_ = s.toBrowser(map[string]any{"type": "effort", "id": id})
}

func (s *session) prompt(text string, files []promptFile) {
	s.prompted.Store(true)
	// Start clean: a leftover answer or question list from the last turn
	// must not be applied to this one.
	s.mu.Lock()
	s.askQ = nil
	s.askReply = nil
	s.askDone = false
	s.mu.Unlock()
	// Both guards are already held — loop() claimed the socket and the ACP
	// session before starting this goroutine.
	_ = s.toBrowser(map[string]string{"type": "busy"})
	res, err := s.rpc("session/prompt", map[string]any{
		"sessionId": s.id,
		"prompt":    buildPrompt(text, files, s.cwd, s.imageCap),
	})
	s.hub.releaseTurn(s.id)
	s.busy.Store(false)
	s.clearAsk()
	s.clearPerm()
	if err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
	} else if err := rpcError(res); err != nil {
		_ = s.toBrowser(map[string]string{"type": "err", "text": err.Error()})
	}
	_ = s.toBrowser(map[string]string{"type": "idle"})
}

func (s *session) rpc(method string, params any) (json.RawMessage, error) {
	if s == nil || s.hub == nil {
		return nil, errNoAgent
	}
	return s.hub.rpc(method, params)
}

func (s *session) notify(method string, params any) {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.notify(method, params)
}

var (
	errTimeout     = timeoutErr("agent rpc timeout")
	errNoAgent     = timeoutErr("no agent")
	errBrowserGone = timeoutErr("browser gone")
)

type timeoutErr string

func (e timeoutErr) Error() string { return string(e) }

func (h *agentHub) writeJSON(v any) error {
	if h == nil || h.conn == nil || h.dead.Load() {
		return errNoAgent
	}
	h.wmu.Lock()
	defer h.wmu.Unlock()
	return h.conn.WriteJSON(v)
}

// rpcDeadline is per method because the methods are not alike. In ACP
// session/prompt only returns when the whole turn ends, so a deadline on it
// is a cap on how long the agent may work — pane used to call a live turn
// dead after ten minutes, deny the permission it was blocked on and go idle
// while it kept streaming. Control calls answer immediately, so a slow one
// means the agent is wedged and the user should hear that in seconds.
func rpcDeadline(method string) time.Duration {
	switch method {
	case "session/prompt":
		return 12 * time.Hour
	case "session/new", "session/load":
		// Loading a long transcript is real work, but still bounded.
		return 2 * time.Minute
	default:
		return 30 * time.Second
	}
}

// rpcWait is how long this hub gives one call. Per hub so a test can watch
// what rpc asks for and answer it in milliseconds, rather than proving the
// table is consulted by waiting out a real deadline.
func (h *agentHub) rpcWait(method string) time.Duration {
	if h.deadline != nil {
		return h.deadline(method)
	}
	return rpcDeadline(method)
}

func (h *agentHub) rpc(method string, params any) (json.RawMessage, error) {
	if h == nil || h.dead.Load() {
		return nil, errNoAgent
	}
	id := h.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	h.mu.Lock()
	// The hub can die between the check above and this line; the teardown
	// takes the same lock, so seeing it here means we would wait forever.
	if h.dead.Load() {
		h.mu.Unlock()
		return nil, errNoAgent
	}
	if h.pending == nil {
		h.pending = map[int64]chan json.RawMessage{}
	}
	h.pending[id] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, id)
		h.mu.Unlock()
	}()
	if err := h.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		return nil, err
	}
	// A timer rather than time.After: a turn's deadline is hours long and
	// its timer would sit in the heap until then.
	t := time.NewTimer(h.rpcWait(method))
	defer t.Stop()
	select {
	case raw := <-ch:
		return raw, nil
	case <-t.C:
		return nil, errTimeout
	}
}

func (h *agentHub) notify(method string, params any) {
	_ = h.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (h *agentHub) attach(id string, s *session) {
	id = strings.TrimSpace(id)
	if h == nil || s == nil || id == "" {
		return
	}
	h.mu.Lock()
	if h.sessions == nil {
		h.sessions = map[string]map[*session]struct{}{}
	}
	set := h.sessions[id]
	if set == nil {
		set = map[*session]struct{}{}
		h.sessions[id] = set
	}
	set[s] = struct{}{}
	h.mu.Unlock()
}

// detach unhooks a browser and reports whether its ACP session is now
// unwatched. Only then may the caller cancel the turn: with a second tab, or
// a reconnect that raced the old socket's teardown, the browser leaving must
// not cancel the turn the one still there is watching.
func (h *agentHub) detach(s *session) bool {
	if h == nil || s == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, set := range h.sessions {
		if _, ok := set[s]; !ok {
			continue
		}
		delete(set, s)
		if len(set) == 0 {
			delete(h.sessions, id)
		}
	}
	return len(h.sessions[strings.TrimSpace(s.id)]) == 0
}

func (h *agentHub) drop(s *session) {
	if h == nil || s == nil {
		return
	}
	s.stopFollow()
	// Answer anything this browser was still holding before letting it go.
	// A closed tab is not an approval, and an unanswered id would leave
	// the agent blocked on a reply that is never coming.
	s.clearPerm()
	s.clearAsk()
	last := h.detach(s)
	// Cancel only a turn this pane claimed. s.busy is also set when the
	// TUI is writing the session on disk; cancelling that would kill the
	// turn the user is watching in grok.
	if last && h.turnActive(s.id) {
		h.notify("session/cancel", map[string]any{"sessionId": s.id})
	}
}

// claimTurn takes the one turn an ACP session id may have in flight, and
// reports whether it got it. ACP runs a session one prompt at a time, and a
// second tab has never prompted anything itself, so neither its own busy
// flag nor its own socket can answer either question — only the hub sees
// every browser on the id.
func (h *agentHub) claimTurn(sid string) bool {
	if h == nil || sid == "" {
		// Nothing to key a claim on. The per-socket guard is all there is.
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.turns == nil {
		h.turns = map[string]bool{}
	}
	if h.turns[sid] {
		return false
	}
	h.turns[sid] = true
	return true
}

func (h *agentHub) releaseTurn(sid string) {
	if h == nil || sid == "" {
		return
	}
	h.mu.Lock()
	delete(h.turns, sid)
	h.mu.Unlock()
}

func (h *agentHub) turnActive(sid string) bool {
	if h == nil || sid == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turns[sid]
}

func (h *agentHub) shutdown() {
	if h == nil {
		return
	}
	h.dead.Store(true)
	if h.conn != nil {
		_ = h.conn.Close()
	}
}

// lookupAll returns every browser watching an ACP session id, because they
// all have to see the turn. An update with no id still has to be guessed at,
// and that guess can only ever name one session.
func (h *agentHub) lookupAll(sid string) []*session {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if sid = strings.TrimSpace(sid); sid == "" {
		if s := h.guessLocked(); s != nil {
			return []*session{s}
		}
		return nil
	}
	set := h.sessions[sid]
	out := make([]*session, 0, len(set))
	for s := range set {
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// lookup picks the single session that owns bookkeeping which cannot be
// duplicated — a permission card, an interaction record. With several tabs
// on one id the turn belongs to the busy one; a tie belongs to nobody.
func (h *agentHub) lookup(sid string) *session {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return h.guessLocked()
	}
	set := h.sessions[sid]
	if len(set) == 1 {
		for s := range set {
			return s
		}
	}
	var busy *session
	n := 0
	for s := range set {
		if s != nil && s.busy.Load() {
			busy = s
			n++
		}
	}
	if n == 1 {
		return busy
	}
	return nil
}

// guessLocked names the session an untagged message must have meant: the
// only one attached, or the only one with a turn in flight.
func (h *agentHub) guessLocked() *session {
	seen := map[*session]struct{}{}
	var only, busy *session
	n, nBusy := 0, 0
	for _, set := range h.sessions {
		for s := range set {
			if s == nil {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			n++
			only = s
			if s.busy.Load() {
				busy = s
				nBusy++
			}
		}
	}
	if n == 1 {
		return only
	}
	if nBusy == 1 {
		return busy
	}
	return nil
}

// lookupRequestAll resolves the browsers to show an agent *request* —
// something carrying a JSON-RPC id whose answer has a side effect, like
// approving a command. Returning nobody means the caller answers
// "cancelled", which denies the tool call behind the user's back, so a tie
// between tabs on one session is fanned out to all of them instead and the
// first answer wins. Sockets we can no longer write to are never picked:
// they would swallow the card rather than show it.
func (h *agentHub) lookupRequestAll(sid string) []*session {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if sid = strings.TrimSpace(sid); sid == "" {
		if s := h.guessRequestLocked(); s != nil {
			return []*session{s}
		}
		return nil
	}
	var live, busy []*session
	for s := range h.sessions[sid] {
		if s == nil || s.gone() {
			continue
		}
		live = append(live, s)
		if s.busy.Load() {
			busy = append(busy, s)
		}
	}
	// The turn belongs to one of them: that is the tab being asked.
	if len(busy) == 1 {
		return busy
	}
	return live
}

// lookupRequest is lookupRequestAll for callers that can only address one
// browser. Anything ambiguous stays nil rather than showing one tab a
// request meant for another.
func (h *agentHub) lookupRequest(sid string) *session {
	if list := h.lookupRequestAll(sid); len(list) == 1 {
		return list[0]
	}
	return nil
}

// guessRequestLocked names the session an untagged request must have meant.
// Guessing is worse than admitting we do not know, so it is only ever the
// lone session that actually has a turn in flight: an idle tab cannot be the
// one being asked, and two busy ones are ambiguous.
func (h *agentHub) guessRequestLocked() *session {
	seen := map[*session]struct{}{}
	var busy *session
	n := 0
	for _, set := range h.sessions {
		for s := range set {
			if s == nil || s.gone() {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			if s.busy.Load() {
				busy = s
				n++
			}
		}
	}
	if n == 1 {
		return busy
	}
	return nil
}

func (h *agentHub) readLoop() {
	defer func() {
		h.mu.Lock()
		// Marked dead under the same lock the pending map is drained under,
		// so a call registering right now either lands in the map and is
		// answered below, or sees the hub is gone. Otherwise it would wait
		// out its whole deadline for a reply that can never arrive.
		h.dead.Store(true)
		list := make([]*session, 0, len(h.sessions))
		seen := map[*session]struct{}{}
		for _, set := range h.sessions {
			for s := range set {
				if s == nil {
					continue
				}
				if _, ok := seen[s]; ok {
					continue
				}
				seen[s] = struct{}{}
				list = append(list, s)
			}
		}
		h.sessions = map[string]map[*session]struct{}{}
		h.turns = map[string]bool{}
		pending := h.pending
		h.pending = map[int64]chan json.RawMessage{}
		h.mu.Unlock()
		for _, ch := range pending {
			select {
			case ch <- json.RawMessage(`{"error":{"message":"agent closed"}}`):
			default:
			}
		}
		for _, s := range list {
			_ = s.toBrowser(map[string]string{"type": "err", "text": "agent closed"})
			// Off this goroutine: flushing waits on the browser, and a
			// browser that stopped reading must not hold up the teardown of
			// every other session.
			go s.stopWriting()
		}
		if h.conn != nil {
			_ = h.conn.Close()
		}
	}()
	for {
		_, data, err := h.conn.ReadMessage()
		if err != nil {
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

		// Every request with an id gets an answer. Dropping one leaves the
		// agent blocked on a reply that is never coming, and the turn hangs
		// until something else tears the session down.
		if env.Method == "session/request_permission" {
			targets := h.lookupRequestAll(paramsSessionID(env.Params))
			if len(targets) == 0 {
				h.writePermOutcome(env.ID, "")
				continue
			}
			for i, s := range targets {
				// Every tab on the session records the ask, or the ones that
				// did not get to answer would read the resolve as an approval
				// grok made on its own and warn about it. Only one of them may
				// put a reply on the wire: two responses to one JSON-RPC id is
				// a protocol error, and for a card the user's click decides.
				s.replyPermission(env.ID, data, i == 0)
			}
			continue
		}
		if isAskMethod(env.Method) {
			if s := h.lookupRequest(paramsSessionID(env.Params)); s != nil {
				s.offerAsk(env.ID, env.Method, parseAskQuestions(env.Params))
			} else {
				h.writeAskOutcome(env.ID, buildAskResult("skip", nil, env.Method))
			}
			continue
		}
		if env.Method == "session/update" || env.Method == "x.ai/session/update" {
			for _, s := range h.lookupAll(paramsSessionID(env.Params)) {
				s.forwardUpdate(env.Params)
			}
			continue
		}
		if isSessionNotify(env.Method) {
			// Correlating a permission with the session that was asked only
			// works on an exact match. A guess here would pin one session's
			// interaction to another and warn about the wrong one.
			if sid := paramsSessionID(env.Params); sid != "" {
				if s := h.lookup(sid); s != nil {
					s.noteInteraction(env.Params)
				}
			}
			continue
		}
		if env.Method != "" && rpcIDSet(env.ID) {
			h.writeMethodNotFound(env.ID, env.Method)
			continue
		}
		if n, ok := rpcIDInt(env.ID); ok {
			// Claimed under the lock that reads it, and handed over without
			// blocking: an agent that answers one id more than once must not
			// be able to park the loop that feeds every session on this hub.
			h.mu.Lock()
			ch := h.pending[n]
			delete(h.pending, n)
			h.mu.Unlock()
			if ch != nil {
				raw := env.Result
				if len(env.Error) > 0 && string(env.Error) != "null" {
					raw = json.RawMessage(`{"error":` + string(env.Error) + `}`)
				}
				select {
				case ch <- raw:
				default:
				}
			}
		}
	}
}

func paramsSessionID(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var wrap struct {
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(params, &wrap)
	return strings.TrimSpace(wrap.SessionID)
}

func (s *session) owns(sid string) bool {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return true
	}
	return sid == s.id || sid == s.resumeID
}

// replyPermission shows one browser a permission request, or answers it on
// the spot when the tool needs no gate. mayAnswer is false for every browser
// but one when a card is fanned out to several tabs, so an auto-allowed call
// is still only answered once.
func (s *session) replyPermission(id json.RawMessage, raw []byte, mayAnswer bool) {
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
	var tc struct {
		Title      string          `json:"title"`
		Kind       string          `json:"kind"`
		ToolCallID string          `json:"toolCallId"`
		RawInput   json.RawMessage `json:"rawInput"`
		Meta       struct {
			Tool struct {
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				ReadOnly *bool  `json:"read_only"`
			} `json:"x.ai/tool"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params.ToolCall, &tc)
	s.markPermHandled(tc.ToolCallID)
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(tc.RawInput, &in)
	hint := permHint{
		Kind:     firstNonEmpty(tc.Meta.Tool.Kind, tc.Kind),
		Title:    tc.Title,
		Name:     tc.Meta.Tool.Name,
		Command:  strings.TrimSpace(in.Command),
		ReadOnly: tc.Meta.Tool.ReadOnly,
	}
	allow := pickPermOption(req.Params.Options, "allow_once", "allow_always", "allow")
	deny := pickPermOption(req.Params.Options, "reject_once", "reject_always", "reject", "deny")
	opts := permChoices(req.Params.Options)
	if permissionAutoAllow(hint) || !rpcIDSet(id) {
		if allow == "" && len(req.Params.Options) > 0 {
			allow = req.Params.Options[0].OptionID
		}
		if mayAnswer {
			s.writePermResult(id, allow)
		}
		return
	}
	title := strings.TrimSpace(tc.Title)
	if hint.Command != "" && (title == "" || strings.EqualFold(title, "run_terminal_command")) {
		title = "Execute `" + hint.Command + "`"
	}
	s.offerPermOpts(id, title, hint.Command, allow, deny, opts)
}

// grok reports permission interactions here rather than as ACP updates.
// The wire uses the underscored form; the agent's own protocol table
// documents it without one, so accept either.
func isSessionNotify(method string) bool {
	return method == "_x.ai/session_notification" || method == "x.ai/session_notification"
}

// noteInteraction watches permission interactions the agent resolves by
// itself. When grok runs with permission_mode = "always-approve" (or yolo)
// it never sends session/request_permission, so pane's Allow/Deny card
// never appears and commands run unreviewed. Pane cannot override that
// from here, but it can refuse to imply a gate that is not there.
func (s *session) noteInteraction(params json.RawMessage) {
	var wrap struct {
		Update struct {
			SessionUpdate string `json:"sessionUpdate"`
			ToolCallID    string `json:"tool_call_id"`
			Kind          string `json:"kind"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &wrap) != nil {
		return
	}
	u := wrap.Update
	if u.ToolCallID == "" {
		return
	}
	switch u.SessionUpdate {
	case "pending_interaction":
		if u.Kind != "permission" {
			return
		}
		s.pendMu.Lock()
		if s.pendPerm == nil {
			s.pendPerm = map[string]bool{}
		}
		if len(s.pendPerm) < 64 {
			s.pendPerm[u.ToolCallID] = true
		}
		s.pendMu.Unlock()
	case "interaction_resolved":
		s.pendMu.Lock()
		pending := s.pendPerm[u.ToolCallID]
		handled := s.permSeen[u.ToolCallID]
		lost := s.permLost
		delete(s.pendPerm, u.ToolCallID)
		delete(s.permSeen, u.ToolCallID)
		s.pendMu.Unlock()
		// If we ever failed to record a permission we handled, we can no
		// longer tell "grok approved it" from "we forgot". Stay quiet:
		// crying wolf right after the user clicked Deny is worse than
		// missing a warning.
		if pending && !handled && !lost {
			s.warnAutoApproved()
		}
	}
}

// markPermHandled records that the agent did ask us about this tool call,
// so resolving it is not evidence of auto-approval.
func (s *session) markPermHandled(toolCallID string) {
	if toolCallID == "" {
		return
	}
	s.pendMu.Lock()
	if s.permSeen == nil {
		s.permSeen = map[string]bool{}
	}
	if len(s.permSeen) < 256 {
		s.permSeen[toolCallID] = true
	} else {
		s.permLost = true
	}
	s.pendMu.Unlock()
}

func (s *session) warnAutoApproved() {
	if !s.autoWarn.CompareAndSwap(false, true) {
		return
	}
	log.Printf("session %s: agent auto-approved a tool permission — pane's approval card is bypassed", s.id)
	_ = s.toBrowser(map[string]string{
		"type": "warn",
		"text": "Grok is set to auto-approve tool permissions, so Pane's Allow/Deny card is bypassed and tool calls run unreviewed. Set permission_mode in ~/.grok/config.toml to restore the prompt.",
	})
}

func (s *session) forwardUpdate(params json.RawMessage) {
	if sid := paramsSessionID(params); !s.owns(sid) {
		return
	}
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
		_ = s.toBrowser(map[string]any{"type": "usage", "used": u.Meta.TotalTokens, "size": s.contextSize()})
	}
	if !s.live.Load() {
		return
	}
	askTool := isAskTool(u.Title) || isAskTool(u.Kind)
	askQ := parseAskQuestions(u.RawInput)
	if askTool && len(askQ) == 0 {
		askQ = askFromTitle(u.Title)
	}
	// session/load dumps the whole transcript as updates. We already
	// painted a short chat tail — ignore the flood until the user talks.
	// Mid-turn asks still have to land, or the agent sits on working….
	// "The user" is the ACP session, not this socket: a reconnected tab, or
	// a second one, has never prompted anything itself, and used to discard
	// the answer to a turn that was already running.
	if s.resumeID != "" && !s.prompted.Load() && !s.busy.Load() && !s.hub.turnActive(s.id) && !askTool && !s.following.Load() {
		return
	}
	text := contentText(u.Content)
	switch u.SessionUpdate {
	case "agent_message_chunk":
		_ = s.toBrowser(map[string]string{"type": "out", "text": text, "session": s.id})
	case "agent_thought_chunk":
		_ = s.toBrowser(map[string]string{"type": "thought", "text": text, "session": s.id})
	case "user_message_chunk":
		// Pane already echoed what this socket typed. A TUI prompt on the
		// same session arrives only via the file tail, and only when this
		// hub did not start the turn — otherwise "continue" shows twice.
		if s.following.Load() && text != "" && !s.hub.turnActive(s.id) {
			_ = s.toBrowser(map[string]string{"type": "you", "text": text, "session": s.id})
		}
	case "tool_call", "tool_call_update":
		// A finished ask tool is not a new question. Re-offering on the
		// completion update deletes the answered card and puts an
		// identical blank one in its place.
		if askTool && !terminalToolStatus(u.Status) && (s.busy.Load() || len(askQ) > 0) {
			s.offerAsk(nil, "", askQ)
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

// contextSize reads the token budget under the lock a model switch writes it
// under. The switch runs on its own goroutine now and this read is on the
// hub's readLoop, so the two meet whenever totals stream during a switch.
func (s *session) contextSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.contextN
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

// underCwdResolved answers the question underCwd only appears to answer:
// does this path stay inside the project once the filesystem has its say.
// Both ends are resolved, since the project directory itself is reached
// through a link often enough (/tmp, /home) that comparing a resolved file
// against an unresolved root would reject legitimate attachments.
func underCwdResolved(cwd, path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		root = cwd
	}
	return underCwd(root, real)
}

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
		// From here pane reads the file itself and ships the bytes inline,
		// which skips the agent's own read-permission prompt — so the escape
		// check has to be the real one. underCwd is lexical while ReadFile
		// follows links, so <cwd>/diagram.png -> ~/.ssh/id_rsa passed it and
		// the private key went to the model as an image. Lstat on top of
		// that, so a link is judged as a link rather than as its target.
		if !underCwdResolved(cwd, path) {
			continue
		}
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Size() > maxImageEmbed {
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
	return strings.Contains(m, "ask_user") || strings.Contains(m, "askuserquestion") ||
		strings.Contains(m, "elicitation")
}

func isAskTool(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	return strings.Contains(t, "ask_user") || strings.Contains(t, "ask user") ||
		strings.HasPrefix(t, "ask:") || t == "askuserquestion"
}

func askFromTitle(title string) []askQuestion {
	t := strings.TrimSpace(title)
	if !strings.HasPrefix(strings.ToLower(t), "ask:") {
		return nil
	}
	t = strings.TrimSpace(t[4:])
	if t == "" || isAskTool(t) {
		return nil
	}
	return []askQuestion{{Question: t}}
}

type permHint struct {
	Kind     string
	Title    string
	Name     string
	Command  string
	ReadOnly *bool
}

func permissionAutoAllow(h permHint) bool {
	if isAskTool(h.Kind) || isAskTool(h.Title) || isAskTool(h.Name) {
		return true
	}
	if h.ReadOnly != nil && *h.ReadOnly {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Kind)) {
	case "execute", "edit", "write", "delete", "move", "remove":
		return false
	case "read", "search", "fetch":
		return true
	}
	blob := strings.ToLower(h.Title + " " + h.Name + " " + h.Kind)
	for _, w := range []string{
		"run_terminal", "terminal_command", "execute", "bash", "shell",
		"git push", "git commit", "search_replace", "write_file",
	} {
		if strings.Contains(blob, w) {
			return false
		}
	}
	if strings.TrimSpace(h.Command) != "" {
		return false
	}
	t := strings.ToLower(h.Title + " " + h.Name)
	if strings.Contains(t, "grep") || strings.Contains(t, "read") || strings.Contains(t, "list") {
		return true
	}
	return false
}

func permChoices(opts []struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}) []permChoice {
	out := make([]permChoice, 0, len(opts))
	for _, o := range opts {
		id := strings.TrimSpace(o.OptionID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(o.Name)
		if name == "" {
			name = permFallbackName(o.Kind, id)
		}
		out = append(out, permChoice{ID: id, Kind: o.Kind, Name: name})
	}
	return out
}

func permFallbackName(kind, id string) string {
	k := strings.ToLower(strings.TrimSpace(kind) + " " + strings.TrimSpace(id))
	switch {
	case strings.Contains(k, "allow_always"):
		return "Yes, and don't ask again for anything (always-approve mode)"
	case strings.Contains(k, "allow_once"), strings.Contains(k, "allow"):
		return "Yes, proceed"
	case strings.Contains(k, "reject"), strings.Contains(k, "deny"):
		return "No, reject"
	}
	if id != "" {
		return id
	}
	return kind
}

func pickChosenPerm(action, allow, deny string, opts []permChoice) string {
	a := strings.TrimSpace(action)
	for _, o := range opts {
		if o.ID == a || strings.EqualFold(o.Kind, a) {
			return o.ID
		}
	}
	if strings.EqualFold(a, "allow_always") {
		for _, o := range opts {
			if strings.EqualFold(o.Kind, "allow_always") || o.ID == "allow_always" {
				return o.ID
			}
		}
	}
	if isDenyAction(a) {
		return deny
	}
	return allow
}

func pickPermOption(opts []struct {
	OptionID string `json:"optionId"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}, want ...string) string {
	for _, w := range want {
		wl := strings.ToLower(w)
		for _, o := range opts {
			if strings.ToLower(o.Kind) == wl || strings.ToLower(o.OptionID) == wl {
				return o.OptionID
			}
		}
	}
	return ""
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
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return extractAskQuestions(v, 0)
}

var askSkipKeys = map[string]bool{
	"old_string": true, "new_string": true, "oldText": true, "newText": true,
	"command": true, "content": true, "text": true, "patch": true, "diff": true,
	"stdout": true, "stderr": true,
}

func extractAskQuestions(v any, depth int) []askQuestion {
	if depth > 8 || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		if qs := questionsFromArray(t); usefulAsk(qs) {
			return qs
		}
		for _, item := range t {
			if qs := extractAskQuestions(item, depth+1); usefulAsk(qs) {
				return qs
			}
		}
	case map[string]any:
		if raw, ok := t["questions"]; ok {
			if arr, ok := raw.([]any); ok {
				if qs := questionsFromArray(arr); usefulAsk(qs) {
					return qs
				}
			}
		}
		if qs := questionsFromElicitation(t); usefulAsk(qs) {
			return qs
		}
		for _, k := range []string{"rawInput", "input", "params", "update", "request", "payload", "data", "body"} {
			if qs := extractAskQuestions(t[k], depth+1); usefulAsk(qs) {
				return qs
			}
		}
		for k, inner := range t {
			if askSkipKeys[k] {
				continue
			}
			if qs := extractAskQuestions(inner, depth+1); usefulAsk(qs) {
				return qs
			}
		}
	}
	return nil
}

func usefulAsk(qs []askQuestion) bool {
	if len(qs) == 0 {
		return false
	}
	for _, q := range qs {
		if len(q.Options) > 0 || q.Question != "" {
			return true
		}
	}
	return false
}

func questionsFromArray(arr []any) []askQuestion {
	var out []askQuestion
	optOnly := true
	for _, item := range arr {
		switch q := item.(type) {
		case string:
			s := strings.TrimSpace(q)
			if s == "" {
				continue
			}
			out = append(out, askQuestion{Question: s})
			optOnly = false
		case map[string]any:
			qq := questionFromMap(q)
			if qq.Question == "" && len(qq.Options) == 0 {
				continue
			}
			if qq.Question != "" {
				optOnly = false
			}
			out = append(out, qq)
		}
	}
	if optOnly {
		return nil
	}
	return out
}

func questionFromMap(m map[string]any) askQuestion {
	q := askQuestion{
		Question:    firstAskString(m, "question", "header", "text", "prompt", "title", "message"),
		MultiSelect: askBool(m, "multiSelect", "multi_select"),
	}
	for _, key := range []string{"options", "choices", "answers", "items"} {
		arr, ok := m[key].([]any)
		if !ok {
			continue
		}
		for _, o := range arr {
			if opt, ok := optionFromAny(o); ok {
				q.Options = append(q.Options, opt)
			}
		}
		if len(q.Options) > 0 {
			break
		}
	}
	return q
}

func optionFromAny(v any) (askOption, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return askOption{}, false
		}
		return askOption{Label: s}, true
	case map[string]any:
		lab := firstAskString(t, "label", "text", "title", "value", "name", "id")
		if lab == "" {
			return askOption{}, false
		}
		return askOption{
			Label:       lab,
			Description: firstAskString(t, "description", "detail", "desc"),
			Preview:     firstAskString(t, "preview"),
		}, true
	default:
		return askOption{}, false
	}
}

func questionsFromElicitation(m map[string]any) []askQuestion {
	msg := firstAskString(m, "message")
	schema, _ := m["requestedSchema"].(map[string]any)
	if schema == nil {
		schema, _ = m["requested_schema"].(map[string]any)
	}
	if schema == nil {
		if msg == "" {
			return nil
		}
		return []askQuestion{{Question: msg}}
	}
	if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
		var out []askQuestion
		for _, p := range props {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			title := firstAskString(pm, "title", "description")
			if title == "" {
				title = msg
			}
			opts := enumsFromSchema(pm)
			if title != "" || len(opts) > 0 {
				out = append(out, askQuestion{Question: title, Options: opts})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	opts := enumsFromSchema(schema)
	if msg == "" && len(opts) == 0 {
		return nil
	}
	return []askQuestion{{Question: msg, Options: opts}}
}

func enumsFromSchema(m map[string]any) []askOption {
	if m == nil {
		return nil
	}
	names := stringList(m["enumNames"])
	if len(names) == 0 {
		names = stringList(m["enum_names"])
	}
	vals := stringList(m["enum"])
	if len(vals) == 0 {
		return nil
	}
	var out []askOption
	for i, v := range vals {
		lab := v
		if i < len(names) && names[i] != "" {
			lab = names[i]
		}
		out = append(out, askOption{Label: lab})
	}
	return out
}

func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func firstAskString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch t := m[k].(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" {
				return s
			}
		}
	}
	return ""
}

func askBool(m map[string]any, keys ...string) bool {
	for _, k := range keys {
		if b, ok := m[k].(bool); ok && b {
			return true
		}
	}
	return false
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

func buildAskResult(action string, answers []askAnswer, method string) any {
	if strings.Contains(strings.ToLower(method), "elicitation") {
		if strings.EqualFold(action, "skip") || strings.EqualFold(action, "dismiss") {
			return map[string]any{"action": "cancel"}
		}
		content := map[string]any{}
		var first []string
		for i, a := range answers {
			key := strings.TrimSpace(a.Question)
			if key == "" {
				key = "answer"
			}
			if len(a.Selected) == 1 {
				content[key] = a.Selected[0]
			} else {
				content[key] = a.Selected
			}
			if i == 0 {
				first = a.Selected
			}
		}
		if len(first) == 1 {
			content["answer"] = first[0]
		} else if len(first) > 0 {
			content["answer"] = first
		}
		return map[string]any{"action": "accept", "content": content}
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "skip", "skip_interview", "dismiss":
		return map[string]any{"outcome": "skip_interview"}
	case "chat", "chat_about_this":
		return map[string]any{"outcome": "chat_about_this"}
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
			"outcome":         "accepted",
			"answers":         payload,
			"partial_answers": []any{},
		}
	}
}

func (s *session) offerAsk(id json.RawMessage, method string, qs []askQuestion) {
	var (
		supersede    json.RawMessage
		supersedeMet string
		strand       json.RawMessage
		strandMet    string
		replyID      json.RawMessage
		reply        any
	)
	// One critical section decides who owns which id, so the browser
	// goroutine can never answer an id we have already closed out.
	s.mu.Lock()
	// A re-offer with no id is the agent restating a question we have
	// already answered this turn. Showing it again would delete the
	// answered card and put an identical blank one in its place.
	if !rpcIDSet(id) && s.askDone {
		s.mu.Unlock()
		return
	}
	if rpcIDSet(id) {
		if rpcIDSet(s.askID) && string(s.askID) != string(id) {
			supersede = s.askID
			supersedeMet = s.askMethod
		}
		s.askID = append(json.RawMessage(nil), id...)
		if method != "" {
			s.askMethod = method
		}
		if s.askReply != nil {
			reply = s.askReply
			s.askReply = nil
			replyID = s.askID
			s.askID = nil
			// The held answer belongs to this request and is now spent.
			s.askQ = nil
			s.askDone = true
		}
	}
	if reply == nil && len(qs) > 0 {
		s.askQ = qs
	}
	out := s.askQ
	if len(out) == 0 && reply == nil && rpcIDSet(s.askID) {
		// Nothing to render. Answer now rather than sit on the request
		// until the turn times out with the user staring at "working…".
		strand = s.askID
		strandMet = s.askMethod
		s.askID = nil
	}
	s.mu.Unlock()

	if rpcIDSet(supersede) {
		s.writeAskResult(supersede, buildAskResult("skip", nil, supersedeMet))
	}
	if reply != nil {
		s.writeAskResult(replyID, reply)
		return
	}
	if rpcIDSet(strand) {
		s.writeAskResult(strand, buildAskResult("skip", nil, strandMet))
		return
	}
	if len(out) == 0 {
		return
	}
	_ = s.toBrowser(map[string]any{"type": "ask", "questions": out})
}

func (s *session) completeAsk(action string, answers []askAnswer) {
	s.mu.Lock()
	method := s.askMethod
	offered := len(s.askQ) > 0
	id := s.askID
	s.askID = nil
	s.askQ = nil
	s.askDone = true
	s.mu.Unlock()

	result := buildAskResult(action, answers, method)
	if rpcIDSet(id) {
		s.writeAskResult(id, result)
		return
	}
	// The card can go up before the agent's request arrives, so an answer
	// with nothing pending is held for it. With no card up there is nothing
	// to answer, and stashing would quietly consume the next question of
	// the turn with a reply the user never gave.
	if !offered {
		return
	}
	s.mu.Lock()
	s.askReply = result
	s.mu.Unlock()
}

func (s *session) clearAsk() {
	s.mu.Lock()
	id := s.askID
	method := s.askMethod
	s.askID = nil
	s.askMethod = ""
	s.askQ = nil
	s.askReply = nil
	s.mu.Unlock()
	if rpcIDSet(id) {
		s.writeAskResult(id, buildAskResult("skip", nil, method))
	}
}

func (s *session) offerPerm(id json.RawMessage, title, command, allow, deny string) {
	s.offerPermOpts(id, title, command, allow, deny, nil)
}

func (s *session) offerPermOpts(id json.RawMessage, title, command, allow, deny string, opts []permChoice) {
	if !rpcIDSet(id) {
		return
	}
	// Claim the superseded id under the same lock that installs the new
	// one, so the browser goroutine cannot answer an id we are about to
	// close out — that would put two replies on the wire for one request.
	s.mu.Lock()
	var supersede json.RawMessage
	supersedeDeny := ""
	if rpcIDSet(s.permID) && string(s.permID) != string(id) {
		supersede = s.permID
		supersedeDeny = s.permDeny
	}
	s.permID = append(json.RawMessage(nil), id...)
	s.permAllow = allow
	s.permDeny = deny
	s.permOpts = opts
	s.mu.Unlock()
	if rpcIDSet(supersede) {
		s.writePermResult(supersede, supersedeDeny)
	}
	if title == "" {
		title = "run a command"
	}
	frame := map[string]any{
		"type":    "perm",
		"title":   title,
		"command": command,
	}
	if len(opts) > 0 {
		frame["options"] = opts
	}
	_ = s.toBrowser(frame)
}

func (s *session) completePerm(action string) {
	s.mu.Lock()
	id := s.permID
	allow := s.permAllow
	deny := s.permDeny
	opts := s.permOpts
	s.permID = nil
	s.permAllow = ""
	s.permDeny = ""
	s.permOpts = nil
	s.mu.Unlock()
	if !rpcIDSet(id) {
		return
	}
	// A fanned-out card is up in more than one tab. Take it away everywhere
	// else before answering: the first click is the user's answer, and the
	// other tab's turn ending would otherwise reject the same request a
	// second time, after it was allowed.
	s.hub.forgetPerm(s, id)
	s.writePermResult(id, pickChosenPerm(action, allow, deny, opts))
}

// forgetPerm takes a card that has just been answered away from every other
// browser holding it, so only one reply for that request id ever reaches the
// agent.
func (h *agentHub) forgetPerm(answered *session, id json.RawMessage) {
	if h == nil || !rpcIDSet(id) {
		return
	}
	h.mu.Lock()
	seen := map[*session]struct{}{}
	others := make([]*session, 0, len(h.sessions))
	for _, set := range h.sessions {
		for s := range set {
			if s == nil || s == answered {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			others = append(others, s)
		}
	}
	h.mu.Unlock()
	for _, s := range others {
		s.dropPerm(id)
	}
}

// dropPerm forgets a card another browser has already answered. The stale
// card is left on screen — clicking it now does nothing — rather than
// putting a second answer on the wire for a request that is settled.
func (s *session) dropPerm(id json.RawMessage) {
	s.mu.Lock()
	if string(s.permID) == string(id) {
		s.permID = nil
		s.permAllow = ""
		s.permDeny = ""
		s.permOpts = nil
	}
	s.mu.Unlock()
}

// isDenyAction is deliberately the inverse of a short allow list: an
// action pane does not recognise must not run the command.
func isDenyAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "allow", "allow_once", "allow_always", "accept", "approve", "yes":
		return false
	}
	return true
}

func terminalToolStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "rejected":
		return true
	}
	return false
}

// clearPerm closes out a card nobody answered — the turn ended, the RPC
// timed out, or the browser went away. Silence is not consent: reject when
// the agent gave us a reject option, otherwise cancel.
func (s *session) clearPerm() {
	s.mu.Lock()
	id := s.permID
	deny := s.permDeny
	s.permID = nil
	s.permAllow = ""
	s.permDeny = ""
	s.permOpts = nil
	s.mu.Unlock()
	s.writePermResult(id, deny)
}

// writePermOutcome answers a session/request_permission. An empty option is
// not a selection: anything we cannot map to a real option is answered
// "cancelled", the ACP way to say the user did not choose. Falling back to
// the allow option would silently turn Deny — and a card nobody answered —
// into an approval.
func (h *agentHub) writePermOutcome(id json.RawMessage, option string) {
	if h == nil || !rpcIDSet(id) {
		return
	}
	outcome := map[string]any{"outcome": "cancelled"}
	if strings.TrimSpace(option) != "" {
		outcome = map[string]any{"outcome": "selected", "optionId": option}
	}
	_ = h.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  map[string]any{"outcome": outcome},
	})
}

func (h *agentHub) writeAskOutcome(id json.RawMessage, result any) {
	if h == nil || !rpcIDSet(id) {
		return
	}
	_ = h.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (h *agentHub) writeMethodNotFound(id json.RawMessage, method string) {
	if h == nil || !rpcIDSet(id) {
		return
	}
	_ = h.writeJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    -32601,
			"message": "Method not found: " + method,
		},
	})
}

func (s *session) writePermResult(id json.RawMessage, option string) {
	if s == nil {
		return
	}
	s.hub.writePermOutcome(id, option)
}

func (s *session) writeAskResult(id json.RawMessage, result any) {
	if s == nil {
		return
	}
	s.hub.writeAskOutcome(id, result)
}
