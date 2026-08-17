package main

import (
	"bufio"
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
		http.Error(w, "cwd required", http.StatusBadRequest)
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
		max = 300
	}
	path := filepath.Join(sessionGroupDir(cwd), id, "updates.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var evs []replayEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var row struct {
			Method string `json:"method"`
			Params struct {
				Update struct {
					SessionUpdate string          `json:"sessionUpdate"`
					Title         string          `json:"title"`
					Content       json.RawMessage `json:"content"`
				} `json:"update"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		kind := row.Params.Update.SessionUpdate
		text := contentTextFromRaw(row.Params.Update.Content)
		switch kind {
		case "user_message_chunk":
			evs = appendReplay(evs, "you", text)
		case "agent_message_chunk":
			evs = appendReplay(evs, "out", text)
		case "agent_thought_chunk":
			evs = appendReplay(evs, "thought", text)
		case "tool_call":
			title := row.Params.Update.Title
			if title == "" {
				title = "tool"
			}
			evs = appendReplay(evs, "tool", title)
		}
	}
	if len(evs) > max {
		evs = evs[len(evs)-max:]
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
