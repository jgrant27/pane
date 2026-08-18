package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

func agentWSBase(bind string) string {
	return "ws://" + bind
}

func dialAgent(agentBase, secret string, timeout time.Duration) (*websocket.Conn, error) {
	u, err := url.Parse(strings.TrimRight(agentBase, "/") + "/ws")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("server-key", secret)
	u.RawQuery = q.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: timeout}
	c, resp, err := dialer.Dial(u.String(), nil)
	if err != nil {
		return nil, agentDialError(agentBase, err, resp)
	}
	return c, nil
}

func agentDialError(base string, err error, resp *http.Response) error {
	if resp == nil {
		msg := err.Error()
		if strings.Contains(msg, "connection refused") || strings.Contains(msg, "connect: connection refused") {
			return fmt.Errorf("no grok agent at %s — start one with: make agent", base)
		}
		return fmt.Errorf("agent: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 240))
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("agent at %s rejected this secret (http %d). Something else is already bound there with a different --secret. Replace it: make agent-restart", base, resp.StatusCode)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("agent: %v (http %d %s)", err, resp.StatusCode, msg)
	}
	return fmt.Errorf("agent: %w", err)
}

func probeAgent(agentBase, secret string) error {
	c, err := dialAgent(agentBase, secret, 2*time.Second)
	if c != nil {
		_ = c.Close()
	}
	return err
}

func serveAgent(bind, secret string, replace bool) error {
	if _, err := exec.LookPath("grok"); err != nil {
		return fmt.Errorf("grok not on PATH")
	}
	if replace && tcpBusy(bind) {
		if err := killListener(bind); err != nil {
			return err
		}
	}
	if tcpBusy(bind) {
		if err := probeAgent(agentWSBase(bind), secret); err != nil {
			pid, cmd := listenerInfo(bind)
			if pid != "" {
				return fmt.Errorf("%v\n  pid %s  %s", err, pid, redactSecret(cmd))
			}
			return err
		}
		log.Printf("already running on %s — secret matches", bind)
		return nil
	}
	cmd := exec.Command("grok", "agent", "serve", "--bind", bind, "--secret", secret)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("starting grok agent serve on %s", bind)
	return cmd.Run()
}

func listenerInfo(bind string) (pid, cmd string) {
	port := listenPort(bind)
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		return "", ""
	}
	pid = strings.TrimSpace(strings.Split(string(out), "\n")[0])
	if pid == "" {
		return "", ""
	}
	b, err := exec.Command("ps", "-p", pid, "-o", "command=").Output()
	if err != nil {
		return pid, ""
	}
	return pid, strings.TrimSpace(string(b))
}

func killListener(bind string) error {
	pid, cmd := listenerInfo(bind)
	if pid == "" {
		return nil
	}
	n, err := strconv.Atoi(pid)
	if err != nil {
		return fmt.Errorf("pid %s: %w", pid, err)
	}
	p, err := os.FindProcess(n)
	if err != nil {
		return err
	}
	log.Printf("stopping pid %s on %s (%s)", pid, bind, redactSecret(cmd))
	_ = p.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !tcpBusy(bind) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = p.Kill()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !tcpBusy(bind) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("pid %s still listening on %s", pid, bind)
}

func redactSecret(cmd string) string {
	fields := strings.Fields(cmd)
	out := make([]string, 0, len(fields))
	skip := false
	for _, f := range fields {
		if skip {
			out = append(out, "***")
			skip = false
			continue
		}
		if f == "--secret" || f == "-secret" {
			out = append(out, f)
			skip = true
			continue
		}
		if strings.HasPrefix(f, "--secret=") {
			out = append(out, "--secret=***")
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}
