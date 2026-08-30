// Grok Pane server: HTTP + ACP proxy in front of `grok agent serve`.
// The desktop app talks to this process. Bind it on the tailnet, not
// the public internet.
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
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
		// #57: own process group so stopping pane does not SIGHUP the agent.
		// The next pane reuses :2419 instead of minting another grok.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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

	tok, err := resolveUIToken()
	if err != nil {
		return err
	}
	g := &gate{token: tok, listen: cfg.listen, tailscale: cfg.tailscale}
	if cfg.tailscale {
		g.tsDNS = tailscaleDNS()
	} else if autoTailnet && !cfg.local {
		if dns := tailscaleDNS(); dns != "" {
			g.tsDNS = dns
		}
	}

	// Behind tailscale serve the token is not the credential, so there is
	// no reason to hand a machine-local secret to every tailnet visitor.
	pageToken := tok
	if cfg.tailscale {
		pageToken = ""
	}

	mux := http.NewServeMux()
	mux.Handle("/", noStore(tokenIndex(pageToken, http.FileServer(http.FS(web.FS)))))
	mux.HandleFunc("/ws", p.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/meta", p.handleMeta)
	mux.HandleFunc("/v1/sessions", handleSessions)
	mux.HandleFunc("/v1/focus", handleFocus)
	mux.HandleFunc("/v1/projects", handleProjects)
	mux.HandleFunc("/v1/rename", handleRename)
	mux.HandleFunc("/v1/transcript", handleTranscript)
	mux.HandleFunc("/v1/usage", handleUsage)
	mux.HandleFunc("/v1/upload", p.handleUpload)
	mux.HandleFunc("/v1/remote-sessions", handleRemoteSessions)

	if cfg.tailscale {
		if _, err := tailscaleExe(); err != nil {
			return fmt.Errorf("tailscale not on PATH")
		}
	}
	h := g.wrap(mux)

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
	// #57: do not SIGTERM grok agent serve. Restarting pane used to kill
	// it and the next start minted another agent on :2419.
	return nil
}

// gate is everything standing between a stranger and the agent: which
// Host we answer to, which page may talk to us, and whether the caller
// proved it is the UI.
type gate struct {
	token     string
	listen    string
	tailscale bool

	dnsMu sync.Mutex
	tsDNS string
}

const tokenHeader = "X-Pane-Token"

// hostOK rejects a request whose Host we do not serve. Without this a page
// on any domain can point its own DNS at 127.0.0.1 and reach us with an
// origin the browser believes is its own.
func (g *gate) hostOK(host string) bool {
	if host == "" {
		return false
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if n := g.tailnetName(); n != "" && h == strings.ToLower(n) {
		return true
	}
	if h == "localhost" {
		return true
	}
	// A bare IP cannot be rebound — an attacker's page has to reach us
	// through a *name* it controls — so addressing pane by address is
	// always allowed. That keeps the LAN and phone flows working when
	// pane is bound to a wildcard address.
	if net.ParseIP(strings.Trim(h, "[]")) != nil {
		return true
	}
	if lh, _, err := net.SplitHostPort(g.listen); err == nil && lh != "" && lh != "0.0.0.0" && lh != "::" {
		return h == strings.ToLower(lh)
	}
	return false
}

// originOK decides whether a page may read our answers. Same-origin needs
// no allowance; the desktop app runs on its own scheme and identifies
// itself with the token instead.
func (g *gate) originOK(origin, host string) bool {
	if origin == "" {
		return true // not a browser, or a same-origin navigation
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Host != "" && strings.EqualFold(u.Host, host) {
		return true
	}
	// The desktop app's page is served by Wails, not by pane, so it is
	// cross-origin by construction: wails:// on macOS and Linux,
	// http://wails.localhost on Windows. Nothing else is allowed in —
	// the phone shells load pane's own page and are same-origin.
	if strings.EqualFold(u.Scheme, "wails") {
		return true
	}
	if strings.EqualFold(u.Hostname(), "wails.localhost") {
		return true
	}
	// Guard the empty case: an origin like file:// has no hostname, and
	// must not match an unset tailnet name.
	n := g.tailnetName()
	return n != "" && strings.EqualFold(u.Hostname(), n)
}

// tailnetName is looked up lazily and remembered. Reading it once at
// startup meant a single failed `tailscale status` left pane refusing its
// own tailnet hostname for the rest of the run.
func (g *gate) tailnetName() string {
	g.dnsMu.Lock()
	defer g.dnsMu.Unlock()
	if g.tsDNS == "" && g.tailscale {
		g.tsDNS = tailscaleDNS()
	}
	return g.tsDNS
}

// tailnetOK is only satisfied by a request that really came through the
// local `tailscale serve` proxy. The identity header alone proves nothing:
// anything that can reach the port can set it.
func (g *gate) tailnetOK(r *http.Request) bool {
	if !g.tailscale || r.Header.Get("Tailscale-User-Login") == "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// tokenOK reads the token from a header, and from the query string only on
// /ws — a WebSocket handshake cannot carry headers. Everywhere else the
// header is required, so a bare <img>, <script> or link cannot present a
// credential even if the token has leaked into a URL somewhere.
func (g *gate) tokenOK(r *http.Request) bool {
	if g.token == "" {
		return false
	}
	got := r.Header.Get(tokenHeader)
	if got == "" && gatePath(r) == "/ws" {
		got = r.URL.Query().Get("t")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(g.token)) == 1
}

// gatePath is the path the router will actually dispatch on, so the gate
// and ServeMux cannot disagree about which handler a request reaches.
func gatePath(r *http.Request) string {
	p := r.URL.Path
	if p == "" {
		return "/"
	}
	c := path.Clean(p)
	if c == "." {
		return "/"
	}
	if !strings.HasPrefix(c, "/") {
		c = "/" + c
	}
	return c
}

func (g *gate) authOK(r *http.Request) bool {
	return g.tokenOK(r) || g.tailnetOK(r)
}

// wrap applies the gate. Static files stay readable so the UI can boot —
// they carry the token, and without a matching origin no other page can
// read the response — but everything that reaches the agent needs auth.
func (g *gate) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !g.hostOK(r.Host) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		origin := r.Header.Get("Origin")
		if !g.originOK(origin, r.Host) {
			http.Error(w, "bad origin", http.StatusForbidden)
			return
		}
		// Only an origin we already accept gets this far, so it is the
		// origin — not the token — that decides CORS. A preflight never
		// carries the token (browsers do not send custom headers on it),
		// and withholding the headers here would block the real request
		// before it could present one. The token still guards the data.
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, "+tokenHeader)
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		}
		// The page holds a credential and drives a shell. Nobody frames it.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Behind tailscale serve the tailnet identity is the whole gate,
		// on every path: a loopback caller has not come through the proxy
		// and has no business here.
		if g.tailscale {
			if !g.tailnetOK(r) {
				http.Error(w, "tailnet only", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// Otherwise the page and its assets are open — they have to be, to
		// bootstrap — and everything that can reach the agent is not.
		if needsAuth(gatePath(r)) && !g.authOK(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func needsAuth(path string) bool {
	return path == "/ws" || path == "/meta" || strings.HasPrefix(path, "/v1/")
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

// resolveUIToken is the browser's half of the story: the agent secret
// authenticates pane to grok, and this authenticates the UI to pane.
func resolveUIToken() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".grok", "pane.token")
	given := strings.TrimSpace(env("PANE_TOKEN", ""))
	if given == "" {
		if b, err := os.ReadFile(path); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return s, nil
			}
		}
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", err
		}
		given = hex.EncodeToString(raw[:])
	}
	// The file is how the desktop app and cmd/probe find the token, so an
	// operator-supplied one is written there too. Otherwise they would
	// read a stale token and be refused by the server that set it.
	if b, err := os.ReadFile(path); err != nil || strings.TrimSpace(string(b)) != given {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(given+"\n"), 0o600); err != nil {
			return "", err
		}
		log.Printf("wrote %s", path)
	}
	return given, nil
}

const tokenMeta = `<meta name="pane-token" content="">`

// tokenIndex hands the page its token. Only a same-origin document can
// read the result, so this is how the UI gets in without the token ever
// being readable by another site.
func tokenIndex(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			next.ServeHTTP(w, r)
			return
		}
		b, err := web.FS.ReadFile("index.html")
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		out := strings.Replace(string(b), tokenMeta,
			`<meta name="pane-token" content="`+html.EscapeString(token)+`">`, 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, out)
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
