// Probe Pane's browser socket: handshake, then one prompt.
package main

import (
	"encoding/json"
	"fmt"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// uiToken is what pane serves into its own page; a command-line client
// reads it off disk instead.
func uiToken() string {
	if v := strings.TrimSpace(os.Getenv("PANE_TOKEN")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "pane.token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func main() {
	url := "ws://127.0.0.1:7420/ws"
	if len(os.Args) > 1 {
		url = os.Args[1]
	}
	if t := uiToken(); t != "" {
		if u, err := neturl.Parse(url); err == nil && u.Query().Get("t") == "" {
			q := u.Query()
			q.Set("t", t)
			u.RawQuery = q.Encode()
			url = u.String()
		}
	}
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(45 * time.Second))

	gotReady := false
	for {
		_, data, err := c.ReadMessage()
		if err != nil {
			fmt.Fprintf(os.Stderr, "read: %v\n", err)
			os.Exit(1)
		}
		var msg map[string]any
		_ = json.Unmarshal(data, &msg)
		t, _ := msg["type"].(string)
		fmt.Printf("<- %s %s\n", t, trim(string(data), 200))
		switch t {
		case "err":
			os.Exit(2)
		case "ready":
			gotReady = true
			_ = c.WriteJSON(map[string]string{"type": "in", "text": "Reply with exactly: PANE_OK"})
		case "out":
			// keep reading
		case "idle":
			if gotReady {
				fmt.Println("OK handshake+turn")
				return
			}
		}
	}
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
