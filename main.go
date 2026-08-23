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

type paneCfg struct {
	listen, agent, agentBind, secret, cwd string
	tailscale, local, noAgent, noOpen     bool
	serveAgent, replaceAgent              bool
}

func paneUsage(fs *flag.FlagSet) {
	fmt.Fprintf(fs.Output(), `Grok Pane — local server for the Grok Pane desktop app

  pane
  pane -cwd ~/src/my-project
  pane -tailscale -cwd ~/src/my-project
  pane -local
  pane -serve-agent
  pane -serve-agent -replace-agent

Serves the UI on :7420. make run is -no-open -no-agent (no tab, no
spawn). make agent is -serve-agent. Secret: -secret,
$GROK_AGENT_SECRET, $PANE_SECRET, or ~/.grok/pane.secret (created on
first run). An agent already bound on :2419 is reused only if the
secret matches; otherwise it tells you to run make agent-restart.

Ctrl-C stops this process and anything it started. It will not kill an
agent that was already running unless -replace-agent. Never use
tailscale funnel.

`)
	fs.PrintDefaults()
}

func parsePaneArgs(args []string) (paneCfg, error) {
	var cfg paneCfg
	fs := flag.NewFlagSet("pane", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() { paneUsage(fs) }
	fs.StringVar(&cfg.listen, "listen", "127.0.0.1:7420", "HTTP listen address")
	fs.StringVar(&cfg.agent, "agent", "ws://127.0.0.1:2419", "grok agent serve base (no /ws)")
	fs.StringVar(&cfg.agentBind, "agent-bind", "127.0.0.1:2419", "bind for a spawned grok agent serve")
	fs.StringVar(&cfg.secret, "secret", env("GROK_AGENT_SECRET", env("PANE_SECRET", "")), "agent server-key")
	fs.StringVar(&cfg.cwd, "cwd", env("PANE_CWD", ""), "ACP working directory (default: $HOME)")
	fs.BoolVar(&cfg.tailscale, "tailscale", false, "require Tailscale identity (403 loopback) and fail if serve cannot start")
	fs.BoolVar(&cfg.local, "local", false, "loopback only — do not Tailscale-serve")
	fs.BoolVar(&cfg.noAgent, "no-agent", false, "do not start grok agent serve; only connect")
	fs.BoolVar(&cfg.noOpen, "no-open", false, "do not open a browser")
	fs.BoolVar(&cfg.serveAgent, "serve-agent", false, "start or check grok agent serve, then exit (no HTTP UI)")
	fs.BoolVar(&cfg.replaceAgent, "replace-agent", false, "with -serve-agent, replace whatever is on -agent-bind")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("pane: ")
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	cfg, err := parsePaneArgs(args)
	if err != nil {
		return err
	}
	if cfg.replaceAgent && !cfg.serveAgent {
		return fmt.Errorf("-replace-agent requires -serve-agent")
	}
	if cfg.local && cfg.tailscale {
		return fmt.Errorf("-local and -tailscale conflict")
	}
	if cfg.serveAgent {
		sec, err := resolveSecret(cfg.secret)
		if err != nil {
			return err
		}
		return serveAgent(cfg.agentBind, sec, cfg.replaceAgent)
	}
	stop := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		close(stop)
	}()
	return servePane(cfg, stop)
}

var (
	lookPath         = exec.LookPath
	lookTailscaleApp = findTailscaleApp
	openBrowser      = openURL
	tailscaleRun     = func(args ...string) error {
		bin, err := tailscaleExe()
		if err != nil {
			return err
		}
		return exec.Command(bin, args...).Run()
	}
	tailscaleJSON = func() ([]byte, error) {
		bin, err := tailscaleExe()
		if err != nil {
			return nil, err
		}
		return exec.Command(bin, "status", "--json").Output()
	}
	grokReadyFor   = 8 * time.Second
	listenReadyFor = 3 * time.Second
	autoTailnet    = true
	startGrok      = func(bind, secret string) (*exec.Cmd, error) {
		cmd := exec.Command("grok", "agent", "serve", "--bind", bind, "--secret", secret)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	}
)

func servePane(cfg paneCfg, stop <-chan struct{}) error {
	if cfg.cwd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		cfg.cwd = home
	}
	abs, err := filepath.Abs(cfg.cwd)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("cwd: %s", abs)
	}
	cfg.cwd = abs

	sec, err := resolveSecret(cfg.secret)
	if err != nil {
		return err
	}

	agentBase := strings.TrimRight(cfg.agent, "/")
	var agentCmd *exec.Cmd
	if !cfg.noAgent {
		if tcpBusy(cfg.agentBind) {
			if err := probeAgent(agentBase, sec); err != nil {
				log.Printf("%v", err)
			} else {
				log.Printf("reusing grok agent serve on %s", cfg.agentBind)
			}
		} else {
			if _, err := lookPath("grok"); err != nil {
				return fmt.Errorf("grok not on PATH")
			}
			agentCmd, err = startGrok(cfg.agentBind, sec)
			if err != nil {
				return fmt.Errorf("start grok agent serve: %w", err)
			}
			log.Printf("started grok agent serve pid=%d on %s", agentCmd.Process.Pid, cfg.agentBind)
			if err := waitTCP(cfg.agentBind, grokReadyFor); err != nil {
				_ = agentCmd.Process.Signal(syscall.SIGTERM)
				return fmt.Errorf("grok agent serve: %w", err)
			}
		}
	} else if !tcpBusy(cfg.agentBind) {
		log.Printf("no grok agent on %s — start one with: make agent", cfg.agentBind)
	} else if err := probeAgent(agentBase, sec); err != nil {
		log.Printf("%v", err)
	}

	p := &proxy{
		agentBase: agentBase,
		secret:    sec,
		cwd:       cfg.cwd,
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
	mux.HandleFunc("/v1/projects", handleProjects)
	mux.HandleFunc("/v1/rename", handleRename)
	mux.HandleFunc("/v1/transcript", handleTranscript)
	mux.HandleFunc("/v1/usage", handleUsage)
	mux.HandleFunc("/v1/upload", handleUpload)
	mux.HandleFunc("/v1/remote-sessions", handleRemoteSessions)

	h := http.Handler(withCORS(mux))
	if cfg.tailscale {
		if _, err := tailscaleExe(); err != nil {
			return fmt.Errorf("tailscale not on PATH")
		}
		h = requireTailscale(h)
	}

	if tcpBusy(cfg.listen) {
		return fmt.Errorf("already listening on %s", cfg.listen)
	}

	srv := &http.Server{Addr: cfg.listen, Handler: h}
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errc <- err
		}
	}()
	if err := waitTCP(cfg.listen, listenReadyFor); err != nil {
		_ = srv.Close()
		return fmt.Errorf("listen %s: %w", cfg.listen, err)
	}

	url := "http://" + cfg.listen
	log.Printf("local  %s", url)
	log.Printf("agent  %s/ws  cwd=%s", p.agentBase, cfg.cwd)

	tsReset := false
	tsURL := ""
	wantServe := cfg.tailscale || (autoTailnet && !cfg.local)
	if wantServe && !cfg.tailscale {
		if _, err := tailscaleExe(); err != nil || !tailscaleRunning() {
			wantServe = false
		}
	}
	if wantServe {
		if err := tailscaleRun("serve", "--bg", listenPort(cfg.listen)); err != nil {
			if cfg.tailscale {
				_ = srv.Close()
				return fmt.Errorf("tailscale serve: %w", err)
			}
			log.Printf("tailscale serve skipped: %v", err)
		} else {
			tsReset = true
			if dns := tailscaleDNS(); dns != "" {
				tsURL = "https://" + dns + "/"
				log.Printf("tailnet %s", tsURL)
			}
			if cfg.tailscale {
				log.Printf("loopback is 403 unless the request comes through tailscale serve")
			}
		}
	}
	if !cfg.noOpen {
		if tsURL != "" {
			openBrowser(tsURL)
		} else if !cfg.tailscale {
			openBrowser(url)
		}
	}

	log.Printf("Ctrl-C to stop")
	select {
	case <-stop:
	case err := <-errc:
		return err
	}
	if tsReset {
		_ = tailscaleRun("serve", "reset")
		log.Printf("cleared tailscale serve")
	}
	_ = srv.Close()
	if agentCmd != nil && agentCmd.Process != nil {
		_ = agentCmd.Process.Signal(syscall.SIGTERM)
		_, _ = agentCmd.Process.Wait()
	}
	return nil
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

func findTailscaleApp() string {
	cands := []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		cands = append(cands, filepath.Join(home, "Applications/Tailscale.app/Contents/MacOS/Tailscale"))
	}
	for _, c := range cands {
		st, err := os.Stat(c)
		if err != nil || st.IsDir() {
			continue
		}
		return c
	}
	return ""
}

func tailscaleExe() (string, error) {
	if p, err := lookPath("tailscale"); err == nil {
		return p, nil
	}
	if p := lookTailscaleApp(); p != "" {
		return p, nil
	}
	return "", fmt.Errorf("tailscale not on PATH")
}

func tailscaleRunning() bool {
	out, err := tailscaleJSON()
	if err != nil {
		return false
	}
	var st struct {
		BackendState string `json:"BackendState"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return false
	}
	return st.BackendState == "Running"
}

func tailscaleDNS() string {
	out, err := tailscaleJSON()
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

var openCmd = func(u string) *exec.Cmd { return exec.Command("open", u) }

func openURL(u string) {
	cmd := openCmd(u)
	if err := cmd.Start(); err != nil {
		return
	}
	_ = cmd.Process.Release()
}
