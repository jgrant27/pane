package uitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

type mockACP struct {
	mu       sync.Mutex
	prompts  []string
	sessions int
}

func startMockACP(t *testing.T, secret string, m *mockACP) string {
	t.Helper()
	if m == nil {
		m = &mockACP{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != secret {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		go m.serve(c)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func (m *mockACP) serve(c *websocket.Conn) {
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
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{
				"agentCapabilities": map[string]any{"promptCapabilities": map[string]any{"image": true}},
			}})
		case "session/new", "session/load":
			m.mu.Lock()
			m.sessions++
			n := m.sessions
			m.mu.Unlock()
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{
				"sessionId": "01uitestsessionxxxxxxxxxxxxxxx" + itoa(n),
				"models": map[string]any{
					"currentModelId": "grok-4.6",
					"availableModels": []any{
						map[string]any{"modelId": "grok-4.6", "name": "Grok"},
					},
				},
			}})
		case "session/prompt":
			var p struct {
				Prompt []struct {
					Text string `json:"text"`
					Type string `json:"type"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(env.Params, &p)
			text := ""
			for _, part := range p.Prompt {
				text += part.Text
			}
			m.mu.Lock()
			m.prompts = append(m.prompts, text)
			m.mu.Unlock()
			_ = c.WriteJSON(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"text": "hello-from-agent"},
					},
				},
			})
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		default:
			_ = c.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": *env.ID, "result": map[string]any{}})
		}
	}
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
