package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type sessionInfo struct {
	ID       string `json:"id"`
	Cwd      string `json:"cwd"`
	Title    string `json:"title"`
	Updated  string `json:"updated"`
	Created  string `json:"created"`
	Messages int    `json:"messages"`
	Model    string `json:"model"`
	LastTurn string `json:"lastTurn"`
}

type projectInfo struct {
	Cwd      string `json:"cwd"`
	Name     string `json:"name"`
	Sessions int    `json:"sessions"`
	Updated  string `json:"updated"`
}

func grokHome() string {
	if h := os.Getenv("GROK_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".grok")
}

func sessionGroupDir(cwd string) string {
	return filepath.Join(grokHome(), "sessions", url.PathEscape(cwd))
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	if cwd == "" {
		if r.Method == http.MethodDelete {
			http.Error(w, "cwd required", http.StatusBadRequest)
			return
		}
		list := listAllGrokSessions(40)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(list)
		return
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	if r.Method == http.MethodDelete {
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if err := deleteGrokSession(cwd, id); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Query().Get("prune") == "1" {
		keep := map[string]bool{}
		for _, id := range strings.Split(r.URL.Query().Get("keep"), ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				keep[id] = true
			}
		}
		pruneStubSessions(cwd, keep)
	}
	list := listGrokSessions(cwd, 40)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func validSessionID(id string) bool {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") || strings.ContainsAny(id, `'" ;`) {
		return false
	}
	return true
}

func grokBin() string {
	if p := strings.TrimSpace(os.Getenv("GROK_BIN")); p != "" {
		return p
	}
	if p, err := exec.LookPath("grok"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	cands := []string{"/opt/homebrew/bin/grok", "/usr/local/bin/grok"}
	if home != "" {
		cands = append([]string{filepath.Join(home, ".local", "bin", "grok")}, cands...)
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

func grokSessionsDelete(cwd, id string) error {
	bin := grokBin()
	if bin == "" {
		return fmt.Errorf("grok not found")
	}
	cmd := exec.Command(bin, "sessions", "delete", id)
	if cwd != "" {
		cmd.Dir = cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("grok sessions delete: %s", msg)
	}
	return nil
}

func purgeSessionSearch(id string) {
	db := filepath.Join(grokHome(), "sessions", "session_search.sqlite")
	if _, err := os.Stat(db); err != nil {
		return
	}
	bin, err := exec.LookPath("sqlite3")
	if err != nil {
		return
	}
	q := "DELETE FROM session_docs WHERE session_id = '" + id + "';"
	_ = exec.Command(bin, db, q).Run()
}

func deleteGrokSession(cwd, id string) error {
	if !validSessionID(id) {
		return os.ErrInvalid
	}
	dir := filepath.Join(sessionGroupDir(cwd), id)
	// Official path talks to the grok leader + FTS index. Filesystem
	// remove is the fallback when grok is missing or the session is
	// only a leftover directory.
	if err := grokSessionsDelete(cwd, id); err != nil {
		log.Printf("grok sessions delete %s: %v", id, err)
	}
	if st, err := os.Stat(dir); err == nil {
		if !st.IsDir() {
			return os.ErrInvalid
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	purgeSessionSearch(id)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("session %s still on disk", id)
	}
	log.Printf("deleted session %s cwd=%s", id, cwd)
	return nil
}

func decodeSessionGroup(name, dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, ".cwd")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	s, err := url.PathUnescape(name)
	if err != nil {
		return ""
	}
	return s
}

func projectDisplayName(cwd, group string) string {
	if b, err := os.ReadFile(filepath.Join(group, ".name")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return filepath.Base(strings.TrimRight(cwd, `/\`))
}

func renameGrokProject(cwd, name string) error {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return fmt.Errorf("cwd required")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return err
	}
	cwd = abs
	if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
		return fmt.Errorf("cwd not a directory")
	}
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\x00", "")
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("invalid name")
	}
	group := sessionGroupDir(cwd)
	if err := os.MkdirAll(group, 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(group, ".cwd")); err != nil {
		_ = os.WriteFile(filepath.Join(group, ".cwd"), []byte(cwd+"\n"), 0o600)
	}
	path := filepath.Join(group, ".name")
	if name == "" || name == filepath.Base(strings.TrimRight(cwd, `/\`)) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(path, []byte(name+"\n"), 0o600)
}

func listGrokProjects() []projectInfo {
	root := filepath.Join(grokHome(), "sessions")
	ents, err := os.ReadDir(root)
	if err != nil {
		return []projectInfo{}
	}
	var out []projectInfo
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		group := filepath.Join(root, e.Name())
		cwd := decodeSessionGroup(e.Name(), group)
		if cwd == "" {
			continue
		}
		if st, err := os.Stat(cwd); err != nil || !st.IsDir() {
			continue
		}
		n, latest := 0, ""
		for _, s := range listGrokSessions(cwd, 0) {
			n++
			if s.Updated > latest {
				latest = s.Updated
			}
		}
		if n == 0 {
			continue
		}
		out = append(out, projectInfo{
			Cwd:      cwd,
			Name:     projectDisplayName(cwd, group),
			Sessions: n,
			Updated:  latest,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Updated > out[j].Updated
	})
	return out
}

func deleteGrokProject(cwd string) error {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return fmt.Errorf("cwd required")
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	group := sessionGroupDir(cwd)
	root := filepath.Join(grokHome(), "sessions")
	rel, err := filepath.Rel(root, group)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("bad project")
	}
	for _, s := range listGrokSessions(cwd, 0) {
		if err := deleteGrokSession(cwd, s.ID); err != nil {
			log.Printf("delete project session %s: %v", s.ID, err)
		}
	}
	if err := os.RemoveAll(group); err != nil {
		return err
	}
	log.Printf("deleted project cwd=%s", cwd)
	return nil
}

func handleProjects(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listGrokProjects())
	case http.MethodPost:
		cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if err := renameGrokProject(cwd, body.Name); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(projectInfo{
			Cwd:  cwd,
			Name: projectDisplayName(cwd, sessionGroupDir(cwd)),
		})
	case http.MethodDelete:
		cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
		if err := deleteGrokProject(cwd); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET or DELETE", http.StatusMethodNotAllowed)
	}
}

func renameGrokSession(cwd, id, title string) error {
	if !validSessionID(id) {
		return os.ErrInvalid
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("title required")
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	path := filepath.Join(sessionGroupDir(cwd), id, "summary.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	raw["title"] = title
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func handleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if cwd == "" || id == "" {
		http.Error(w, "cwd and id required", http.StatusBadRequest)
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "title required", http.StatusBadRequest)
		return
	}
	if err := renameGrokSession(cwd, id, body.Title); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func listAllGrokSessions(limit int) []sessionInfo {
	root := filepath.Join(grokHome(), "sessions")
	ents, err := os.ReadDir(root)
	if err != nil {
		return []sessionInfo{}
	}
	var out []sessionInfo
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		group := filepath.Join(root, e.Name())
		kids, err := os.ReadDir(group)
		if err != nil {
			continue
		}
		for _, k := range kids {
			if !k.IsDir() {
				continue
			}
			info, ok := readSummary(filepath.Join(group, k.Name(), "summary.json"))
			if ok {
				out = append(out, info)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Updated > out[j].Updated
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func looksLikeSessionID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 20 {
		return false
	}
	for _, r := range s {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-' {
			continue
		}
		return false
	}
	return strings.HasPrefix(strings.ToLower(s), "01")
}

func isStubSession(s sessionInfo) bool {
	if s.Messages > 1 {
		return false
	}
	if s.Title == "" || s.Title == s.ID || looksLikeSessionID(s.Title) {
		return true
	}
	return false
}

func pruneStubSessions(cwd string, keep map[string]bool) int {
	n := 0
	for _, s := range listGrokSessions(cwd, 0) {
		if keep[s.ID] || !isStubSession(s) {
			continue
		}
		if err := deleteGrokSession(cwd, s.ID); err != nil {
			log.Printf("prune %s: %v", s.ID, err)
			continue
		}
		n++
	}
	return n
}

func listGrokSessions(cwd string, limit int) []sessionInfo {
	dir := sessionGroupDir(cwd)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return []sessionInfo{}
	}
	out := make([]sessionInfo, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		info, ok := readSummary(filepath.Join(dir, e.Name(), "summary.json"))
		if !ok {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Updated > out[j].Updated
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func readSummary(path string) (sessionInfo, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return sessionInfo{}, false
	}
	var raw struct {
		Info struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"info"`
		SessionSummary string `json:"session_summary"`
		GeneratedTitle string `json:"generated_title"`
		Title          string `json:"title"`
		CreatedAt      string `json:"created_at"`
		UpdatedAt      string `json:"updated_at"`
		LastActiveAt   string `json:"last_active_at"`
		NumMessages    int    `json:"num_messages"`
		Model          string `json:"current_model_id"`
		LastTurn       string `json:"last_turn_summary"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return sessionInfo{}, false
	}
	id := raw.Info.ID
	if id == "" {
		return sessionInfo{}, false
	}
	title := firstNonEmpty(raw.Title, raw.GeneratedTitle, raw.SessionSummary, id)
	updated := firstNonEmpty(raw.LastActiveAt, raw.UpdatedAt, raw.CreatedAt)
	return sessionInfo{
		ID:       id,
		Cwd:      raw.Info.Cwd,
		Title:    title,
		Updated:  updated,
		Created:  raw.CreatedAt,
		Messages: raw.NumMessages,
		Model:    raw.Model,
		LastTurn: raw.LastTurn,
	}, true
}

func appendReplay(evs []replayEvent, typ, text string) []replayEvent {
	text = strings.TrimRight(text, "")
	if text == "" {
		return evs
	}
	n := len(evs)
	if n > 0 && evs[n-1].Type == typ && (typ == "you" || typ == "out" || typ == "thought") {
		evs[n-1].Text += text
		return evs
	}
	return append(evs, replayEvent{Type: typ, Text: text})
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		s = strings.TrimSpace(s)
		if s != "" {
			return s
		}
	}
	return ""
}

type replayEvent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func replayUpdates(cwd, id string, max int) []replayEvent {
	if max <= 0 {
		max = 20
	}
	path := filepath.Join(sessionGroupDir(cwd), id, "updates.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	const chunk = 512 << 10
	var tail []byte
	off := st.Size()
	var evs []replayEvent
	for off > 0 {
		n := int64(chunk)
		if n > off {
			n = off
		}
		off -= n
		piece := make([]byte, n)
		if _, err := f.ReadAt(piece, off); err != nil {
			break
		}
		tail = append(piece, tail...)
		data := tail
		if off > 0 {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				continue
			}
			data = data[i+1:]
		}
		evs = parseChatReplay(data)
		if len(evs) >= max {
			return evs[len(evs)-max:]
		}
	}
	if len(evs) > max {
		return evs[len(evs)-max:]
	}
	return evs
}

func parseChatReplay(data []byte) []replayEvent {
	var evs []replayEvent
	for len(data) > 0 {
		var line []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			line = data[:i]
			data = data[i+1:]
		} else {
			line = data
			data = nil
		}
		if len(line) == 0 || len(line) > 1<<20 {
			continue
		}
		if !bytes.Contains(line, []byte(`"user_message_chunk"`)) && !bytes.Contains(line, []byte(`"agent_message_chunk"`)) {
			continue
		}
		var row struct {
			Params struct {
				Update struct {
					SessionUpdate string          `json:"sessionUpdate"`
					Content       json.RawMessage `json:"content"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		kind := row.Params.Update.SessionUpdate
		text := contentTextFromRaw(row.Params.Update.Content)
		if text == "" {
			continue
		}
		switch kind {
		case "user_message_chunk":
			evs = appendReplay(evs, "you", text)
		case "agent_message_chunk":
			evs = appendReplay(evs, "out", text)
		}
	}
	return evs
}

func contentTextFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return contentText(v)
}

func handleTranscript(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if cwd == "" || id == "" {
		http.Error(w, "cwd and id required", http.StatusBadRequest)
		return
	}
	if strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	evs := replayUpdates(cwd, id, 400)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(evs)
}
