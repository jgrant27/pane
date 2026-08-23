package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

var runGrokServe = func(bind, secret string) error {
	cmd := exec.Command("grok", "agent", "serve", "--bind", bind, "--secret", secret)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("starting grok agent serve on %s", bind)
	return cmd.Run()
}

func serveAgent(bind, secret string, replace bool) error {
	if _, err := lookPath("grok"); err != nil {
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
	return runGrokServe(bind, secret)
}

// listenerPids returns the pids listening on exactly this bind address. The
// port alone is not enough to name a process: another daemon on the same port
// on a different interface answers `lsof -iTCP:<port>` too, and the first line
// of `lsof -t` is the lowest pid, not the one holding this address.
func listenerPids(bind string) ([]string, error) {
	host, port, err := net.SplitHostPort(bind)
	if err != nil {
		return nil, fmt.Errorf("bad bind %q: %w", bind, err)
	}
	// -F asks for one field per line — p<pid>, n<local address> — so the
	// address can be matched instead of guessed at.
	out, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fpn").Output()
	if err != nil {
		// lsof exits non-zero when nothing matches.
		return nil, nil
	}
	var pids []string
	seen := map[string]bool{}
	pid := ""
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid = line[1:]
		case 'n':
			if pid == "" || seen[pid] || !sameBindHost(host, line[1:]) {
				continue
			}
			seen[pid] = true
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// sameBindHost decides whether a socket lsof reported can be the one holding
// bind's host. A wildcard on either side conflicts with everything on the
// port, which is exactly the case where replacing the listener is right.
func sameBindHost(host, sock string) bool {
	i := strings.LastIndex(sock, ":")
	if i < 0 {
		return false
	}
	sockHost := strings.Trim(sock[:i], "[]")
	if wildcardHost(host) || wildcardHost(sockHost) {
		return true
	}
	a, b := net.ParseIP(host), net.ParseIP(sockHost)
	if a != nil && b != nil {
		return a.Equal(b)
	}
	return host == sockHost
}

func wildcardHost(h string) bool {
	return h == "" || h == "*" || h == "0.0.0.0" || h == "::" || h == "[::]"
}

func listenerInfo(bind string) (pid, cmd string) {
	pids, err := listenerPids(bind)
	// More than one listener on the address is nobody's to report as "the"
	// process holding it.
	if err != nil || len(pids) != 1 {
		return "", ""
	}
	return pids[0], processCmd(pids[0])
}

func processCmd(pid string) string {
	b, err := exec.Command("ps", "-p", pid, "-o", "command=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// isGrokAgent recognises the only process pane is entitled to stop: the grok
// agent it would otherwise be starting itself.
func isGrokAgent(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) < 3 {
		return false
	}
	if strings.TrimSuffix(filepath.Base(fields[0]), ".exe") != "grok" {
		return false
	}
	for i := 1; i+1 < len(fields); i++ {
		if fields[i] == "agent" && fields[i+1] == "serve" {
			return true
		}
	}
	return false
}

func killListener(bind string) error {
	pids, err := listenerPids(bind)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	if len(pids) > 1 {
		return fmt.Errorf("%s has %d listeners (pids %s) — refusing to guess which to stop", bind, len(pids), strings.Join(pids, ", "))
	}
	pid := pids[0]
	cmd := processCmd(pid)
	// Replacing the agent means killing a process; kill the wrong one and a
	// stranger's daemon goes down and the agent still is not replaced.
	if !isGrokAgent(cmd) {
		return fmt.Errorf("pid %s on %s is not a grok agent (%s) — refusing to stop it", pid, bind, redactSecret(cmd))
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
