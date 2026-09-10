package uitest

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

type stack struct {
	Home   string
	URL    string
	Page   playwright.Page
	Mock   *mockACP
	stopWK func()
	pane   *exec.Cmd
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func plantSession(t *testing.T, home, cwd, id, title string) {
	t.Helper()
	dir := filepath.Join(home, "sessions", url.PathEscape(cwd), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf(`{"info":{"id":%q,"cwd":%q},"generated_title":%q,"updated_at":"2026-09-10T00:00:00Z","num_messages":2}`, id, cwd, title)
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
}

func plantTranscript(t *testing.T, home, cwd, id, user, agent string) {
	t.Helper()
	plantSession(t, home, cwd, id, "Transcript")
	dir := filepath.Join(home, "sessions", url.PathEscape(cwd), id)
	userLine := `{"method":"session/update","params":{"sessionId":"` + id + `","update":{"sessionUpdate":"user_message_chunk","content":{"text":"` + user + `"}}}}` + "\n"
	agentLine := `{"method":"session/update","params":{"sessionId":"` + id + `","update":{"sessionUpdate":"agent_message_chunk","content":{"text":"` + agent + `"}}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "updates.jsonl"), []byte(userLine+agentLine), 0o644); err != nil {
		t.Fatal(err)
	}
}

func plantLiveTUI(t *testing.T, home, cwd, id string) {
	t.Helper()
	row := []map[string]any{{
		"session_id": id,
		"pid":        os.Getpid(),
		"cwd":        cwd,
	}}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "active_sessions.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func startStack(t *testing.T) *stack {
	return startStackWith(t, nil)
}

func startStackWith(t *testing.T, setup func(home, cwd string)) *stack {
	return startStackOpts(t, setup, false)
}

func startStackOpts(t *testing.T, setup func(home, cwd string), failLoad bool) *stack {
	t.Helper()
	home := t.TempDir()
	cwd := filepath.Join(home, "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(home, cwd)
	}
	secret := "ui-secret"
	mock := &mockACP{failLoad: failLoad}
	agent := startMockACP(t, secret, mock)
	listen := freeAddr(t)
	bin := buildPane(t)
	cmd := exec.Command(bin,
		"-listen", listen,
		"-agent", agent,
		"-secret", secret,
		"-cwd", cwd,
		"-no-open",
		"-no-agent",
		"-local",
	)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"GROK_HOME="+home,
		"PANE_TOKEN=ui-token",
		"PANE_SECRET="+secret,
	)
	logf, err := os.CreateTemp(t.TempDir(), "pane-ui-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = logf
	cmd.Stderr = logf
	t.Cleanup(func() {
		_ = logf.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logf.Name())
			t.Logf("pane log:\n%s", b)
		}
	})
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	base := "http://" + listen + "/"
	waitHealthy(t, base, "ui-token")
	pg, stopWK := mustWebKit(t, "ui-token")
	if _, err := pg.Goto(base, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
		Timeout:   playwright.Float(20000),
	}); err != nil {
		stopWK()
		t.Fatalf("goto %s: %v", base, err)
	}
	waitSel(t, pg, "#in", 15*time.Second)
	s := &stack{Home: home, URL: base, Page: pg, Mock: mock, stopWK: stopWK, pane: cmd}
	t.Cleanup(stopWK)
	return s
}

func waitHealthy(t *testing.T, rawURL, token string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		req.Header.Set("X-Pane-Token", token)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("pane never became healthy")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
