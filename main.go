// Grok Pane server: HTTP + ACP proxy in front of `grok agent serve`.
// The desktop app talks to this process. Bind it on the tailnet, not
// the public internet.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jgrant27/pane/web"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("pane: ")

	listen := flag.String("listen", "127.0.0.1:7420", "HTTP listen address")
	agent := flag.String("agent", "ws://127.0.0.1:2419", "grok agent serve base (no /ws)")
	agentBind := flag.String("agent-bind", "127.0.0.1:2419", "bind for a spawned grok agent serve")
	secret := flag.String("secret", env("GROK_AGENT_SECRET", env("PANE_SECRET", "")), "agent server-key")
	cwd := flag.String("cwd", env("PANE_CWD", ""), "ACP working directory (default: $HOME)")
	tailscaleFront := flag.Bool("tailscale", false, "run `tailscale serve` in front and require Tailscale identity")
	noAgent := flag.Bool("no-agent", false, "do not start grok agent serve; only connect")
	noOpen := flag.Bool("no-open", false, "do not open a browser")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), `Grok Pane — local server for the Grok Pane desktop app

  pane
  pane -cwd ~/src/my-project
  pane -tailscale -cwd ~/src/my-project

Starts grok agent serve if :2419 is free, serves the UI on :7420, opens
the browser. The desktop app talks to this process. Secret: -secret,
$GROK_AGENT_SECRET, $PANE_SECRET, or ~/.grok/pane.secret (created on
first run).

Ctrl-C stops this process and anything it started. It will not kill an
agent that was already running. Never use tailscale funnel.

`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		*cwd = home
	}
	abs, err := filepath.Abs(*cwd)
	if err != nil {
		log.Fatal(err)
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		log.Fatalf("cwd: %s", abs)
	}
	*cwd = abs

	sec, err := resolveSecret(*secret)
	if err != nil {
		log.Fatal(err)
	}

	var agentCmd *exec.Cmd
	if !*noAgent {
		if tcpBusy(*agentBind) {
			log.Printf("reusing grok agent serve on %s", *agentBind)
		} else {
			if _, err := exec.LookPath("grok"); err != nil {
				log.Fatal("grok not on PATH")
			}
			agentCmd = exec.Command("grok", "agent", "serve",
				"--bind", *agentBind,
				"--secret", sec,
			)
			agentCmd.Stdout = os.Stdout
			agentCmd.Stderr = os.Stderr
			if err := agentCmd.Start(); err != nil {
				log.Fatalf("start grok agent serve: %v", err)
			}
			log.Printf("started grok agent serve pid=%d on %s", agentCmd.Process.Pid, *agentBind)
			if err := waitTCP(*agentBind, 8*time.Second); err != nil {
				_ = agentCmd.Process.Signal(syscall.SIGTERM)
				log.Fatalf("grok agent serve: %v", err)
			}
		}
	}

	p := &proxy{
		agentBase: strings.TrimRight(*agent, "/"),
		secret:    sec,
		cwd:       *cwd,
	}

	mux := http.NewServeMux()
	mux.Handle("/", noStore(http.FileServer(http.FS(web.FS))))
	mux.HandleFunc("/ws", p.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/meta", p.handleMeta)
	mux.HandleFunc("/v1/sessions", handleSessions)
	mux.HandleFunc("/v1/transcript", handleTranscript)

	h := http.Handler(withCORS(mux))
	if *tailscaleFront {
		if _, err := exec.LookPath("tailscale"); err != nil {
			log.Fatal("tailscale not on PATH")
		}
		h = requireTailscale(h)
	}

	if tcpBusy(*listen) {
		log.Fatalf("already listening on %s", *listen)
	}

	srv := &http.Server{Addr: *listen, Handler: h}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	if err := waitTCP(*listen, 3*time.Second); err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}

	url := "http://" + *listen
	log.Printf("local  %s", url)
	log.Printf("agent  %s/ws  cwd=%s", p.agentBase, *cwd)

	tsReset := false
	if *tailscaleFront {
		port := listenPort(*listen)
		if err := exec.Command("tailscale", "serve", "--bg", port).Run(); err != nil {
			log.Fatalf("tailscale serve: %v", err)
		}
		tsReset = true
		if dns := tailscaleDNS(); dns != "" {
			log.Printf("tailnet https://%s/", dns)
		}
		log.Printf("loopback is 403 unless the request comes through tailscale serve")
	} else if !*noOpen {
		openURL(url)
	}

	log.Printf("Ctrl-C to stop")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	if tsReset {
		_ = exec.Command("tailscale", "serve", "reset").Run()
		log.Printf("cleared tailscale serve")
	}
	_ = srv.Close()
	if agentCmd != nil && agentCmd.Process != nil {
		_ = agentCmd.Process.Signal(syscall.SIGTERM)
		_, _ = agentCmd.Process.Wait()
	}
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func resolveSecret(given string) (string, error) {
	if given != "" {
		return given, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".grok", "pane.secret")
	if b, err := os.ReadFile(path); err == nil {
		s := strings.TrimSpace(string(b))
		if s != "" {
			return s, nil
		}
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	s := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(s+"\n"), 0o600); err != nil {
		return "", err
	}
	log.Printf("wrote %s", path)
	return s, nil
}

func tcpBusy(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return true
	}
	_ = ln.Close()
	return false
}

func waitTCP(addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	var last error
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return nil
		}
		last = err
		time.Sleep(100 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

func listenPort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "7420"
	}
	return port
}

func tailscaleDNS() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var st struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return ""
	}
	return strings.TrimSuffix(st.Self.DNSName, ".")
}

func requireTailscale(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Tailscale-User-Login") == "" {
			http.Error(w, "tailnet only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func openURL(u string) {
	cmd := exec.Command("open", u)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
