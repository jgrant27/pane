package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProbeAgentSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("server-key") != "ok-secret" {
			http.Error(w, "Invalid or missing authorization token", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = c.Close()
	}))
	t.Cleanup(srv.Close)
	ws := "ws" + strings.TrimPrefix(srv.URL, "http")

	if err := probeAgent(ws, "ok-secret"); err != nil {
		t.Fatalf("matching secret: %v", err)
	}
	err := probeAgent(ws, "wrong")
	if err == nil {
		t.Fatal("wrong secret: expected error")
	}
	if !strings.Contains(err.Error(), "rejected this secret") {
		t.Fatalf("wrong secret: %v", err)
	}
	if !strings.Contains(err.Error(), "make agent-restart") {
		t.Fatalf("want restart hint: %v", err)
	}
}

func TestRedactSecret(t *testing.T) {
	in := "grok agent --debug serve --bind 127.0.0.1:2419 --secret pane-dev-secret --debug-file /tmp/grok-serve.log"
	got := redactSecret(in)
	if strings.Contains(got, "pane-dev-secret") {
		t.Fatalf("leaked secret: %s", got)
	}
	if !strings.Contains(got, "--secret ***") {
		t.Fatalf("got %s", got)
	}
}

func TestAgentWSBase(t *testing.T) {
	if got := agentWSBase("127.0.0.1:2419"); got != "ws://127.0.0.1:2419" {
		t.Fatal(got)
	}
}
