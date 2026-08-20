package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testGate() *gate {
	return &gate{token: "secret-token", listen: "127.0.0.1:7420"}
}

func req(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Host = "127.0.0.1:7420"
	r.RemoteAddr = "127.0.0.1:54321"
	return r
}

func serve(g *gate, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	g.wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("reached"))
	})).ServeHTTP(rec, r)
	return rec
}

// The whole point: a page on another site must not be able to drive the
// agent, and must not be able to read anything it says back.
func TestGateRejectsUnauthenticatedAPI(t *testing.T) {
	g := testGate()
	for _, path := range []string{"/ws", "/meta", "/v1/sessions", "/v1/upload", "/v1/projects"} {
		if code := serve(g, req(http.MethodGet, path)).Code; code != http.StatusUnauthorized {
			t.Fatalf("%s answered %d without a token", path, code)
		}
	}
	// The page itself has to load, or nothing can bootstrap.
	for _, path := range []string{"/", "/term.js", "/healthz"} {
		if code := serve(g, req(http.MethodGet, path)).Code; code != http.StatusOK {
			t.Fatalf("%s answered %d", path, code)
		}
	}
}

func TestGateAcceptsToken(t *testing.T) {
	g := testGate()
	r := req(http.MethodGet, "/v1/sessions")
	r.Header.Set(tokenHeader, "secret-token")
	if code := serve(g, r).Code; code != http.StatusOK {
		t.Fatalf("header token rejected: %d", code)
	}
	// A WebSocket cannot set headers, so /ws takes the token in the query.
	if code := serve(g, req(http.MethodGet, "/ws?t=secret-token")).Code; code != http.StatusOK {
		t.Fatalf("query token rejected: %d", code)
	}
	if code := serve(g, req(http.MethodGet, "/ws?t=wrong")).Code; code != http.StatusUnauthorized {
		t.Fatalf("wrong token accepted: %d", code)
	}
}

// A hostile page may be able to send a request; it must not be able to
// read the reply, which is what would leak the token and the transcript.
func TestGateWithholdsCORSFromStrangers(t *testing.T) {
	g := testGate()
	r := req(http.MethodGet, "/v1/sessions")
	r.Header.Set("Origin", "https://evil.example")
	rec := serve(g, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("foreign origin answered %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("foreign origin was granted CORS")
	}

	// The desktop app runs on its own scheme and proves itself with the token.
	r = req(http.MethodGet, "/v1/sessions")
	r.Header.Set("Origin", "wails://wails")
	r.Header.Set(tokenHeader, "secret-token")
	rec = serve(g, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("desktop origin answered %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "wails://wails" {
		t.Fatalf("desktop CORS %q", got)
	}

	// Same origin needs no allowance and gets none.
	r = req(http.MethodGet, "/v1/sessions")
	r.Header.Set("Origin", "http://127.0.0.1:7420")
	r.Header.Set(tokenHeader, "secret-token")
	if code := serve(g, r).Code; code != http.StatusOK {
		t.Fatalf("same origin answered %d", code)
	}
}

// DNS rebinding: the attacker's name resolves to us, so the browser thinks
// the page is same-origin. The Host header is what gives it away.
func TestGateRejectsForeignHost(t *testing.T) {
	g := testGate()
	r := req(http.MethodGet, "/v1/sessions")
	r.Host = "evil.example"
	r.Header.Set(tokenHeader, "secret-token")
	if code := serve(g, r).Code; code != http.StatusForbidden {
		t.Fatalf("rebound host answered %d", code)
	}
	for _, h := range []string{"127.0.0.1:7420", "localhost:7420", "127.0.0.1", "[::1]:7420"} {
		r = req(http.MethodGet, "/healthz")
		r.Host = h
		if code := serve(g, r).Code; code != http.StatusOK {
			t.Fatalf("host %s answered %d", h, code)
		}
	}
}

// Presence of the identity header proves nothing on its own: anything that
// can reach the port can set it. Only the local proxy may assert it.
func TestGateTailnetNeedsTheProxy(t *testing.T) {
	g := &gate{token: "secret-token", listen: "127.0.0.1:7420", tailscale: true, tsDNS: "box.tailnet.ts.net"}

	r := req(http.MethodGet, "/healthz")
	if code := serve(g, r).Code; code != http.StatusForbidden {
		t.Fatalf("bare loopback answered %d", code)
	}
	// Even with a valid token: in tailnet mode identity is the gate.
	r = req(http.MethodGet, "/v1/sessions")
	r.Header.Set(tokenHeader, "secret-token")
	if code := serve(g, r).Code; code != http.StatusForbidden {
		t.Fatalf("token bypassed the tailnet gate: %d", code)
	}
	// Through the proxy, which connects over loopback, with an identity.
	r = req(http.MethodGet, "/v1/sessions")
	r.Header.Set("Tailscale-User-Login", "jgrant@example.com")
	if code := serve(g, r).Code; code != http.StatusOK {
		t.Fatalf("proxied request answered %d", code)
	}
	// Same header from somewhere that is not the local proxy.
	r = req(http.MethodGet, "/v1/sessions")
	r.Header.Set("Tailscale-User-Login", "jgrant@example.com")
	r.RemoteAddr = "10.1.2.3:5555"
	if code := serve(g, r).Code; code != http.StatusForbidden {
		t.Fatalf("forged identity from off-box answered %d", code)
	}
	// The tailnet name is a Host we answer to.
	r = req(http.MethodGet, "/healthz")
	r.Host = "box.tailnet.ts.net"
	r.Header.Set("Tailscale-User-Login", "jgrant@example.com")
	if code := serve(g, r).Code; code != http.StatusOK {
		t.Fatalf("tailnet host answered %d", code)
	}
}

// The page carries the token so a same-origin document can read it, and
// nothing else can.
func TestTokenIndexInjects(t *testing.T) {
	rec := httptest.NewRecorder()
	tokenIndex("abc123", http.NotFoundHandler()).ServeHTTP(rec, req(http.MethodGet, "/"))
	body := rec.Body.String()
	if !strings.Contains(body, `<meta name="pane-token" content="abc123">`) {
		t.Fatalf("token not injected: %s", firstLines(body))
	}
	if strings.Contains(body, tokenMeta) {
		t.Fatal("placeholder left in the page")
	}
	// Anything else falls through to the file server.
	rec = httptest.NewRecorder()
	tokenIndex("abc123", http.NotFoundHandler()).ServeHTTP(rec, req(http.MethodGet, "/style.css"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-index path handled here: %d", rec.Code)
	}
}

func firstLines(s string) string {
	if len(s) > 240 {
		return s[:240]
	}
	return s
}

// "." used to pass validation and then be cleaned away by filepath.Join,
// so deleting one session deleted the whole project's history.
func TestSessionIDRejectsPathTricks(t *testing.T) {
	bad := []string{
		"", ".", "..", "...", "./x", "../x", `a/b`, `a\b`, "a'b", `a"b`, "a b", "a;b",
		"%2FUsers%2Fjgrant", "-lead", "_lead", "ab",
	}
	for _, id := range bad {
		if validSessionID(id) {
			t.Fatalf("accepted %q", id)
		}
	}
	for _, id := range []string{"01abc", "01a01f9b-c081-78b0-9373-844d85fb8aa3", "abc_def-123"} {
		if !validSessionID(id) {
			t.Fatalf("rejected %q", id)
		}
	}
}

// Belt and braces: even if an id slipped through, the resolved directory
// has to be a direct child of the project's session group.
func TestSessionDirStaysInsideTheGroup(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	cwd := "/tmp/some-project"
	if _, ok := sessionDir(cwd, "."); ok {
		t.Fatal(`"." resolved to a directory`)
	}
	if _, ok := sessionDir(cwd, ".."); ok {
		t.Fatal(`".." resolved to a directory`)
	}
	dir, ok := sessionDir(cwd, "01abc")
	if !ok {
		t.Fatal("a real id did not resolve")
	}
	if got := strings.TrimPrefix(dir, sessionGroupDir(cwd)); got != "/01abc" {
		t.Fatalf("resolved outside the group: %s", dir)
	}
}

// A browser never puts custom headers on a preflight, so the preflight
// itself cannot be authenticated. Requiring the token here would block the
// desktop app's real request before it ever got to present one.
func TestGateOptionsPreflightNeedsNoToken(t *testing.T) {
	g := testGate()
	r := req(http.MethodOptions, "/v1/sessions")
	r.Header.Set("Origin", "wails://wails")
	r.Header.Set("Access-Control-Request-Method", "GET")
	r.Header.Set("Access-Control-Request-Headers", tokenHeader)
	rec := serve(g, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "wails://wails" {
		t.Fatalf("unauthenticated preflight got no CORS: %q", got)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), tokenHeader) {
		t.Fatalf("preflight does not allow the token header: %q", rec.Header().Get("Access-Control-Allow-Headers"))
	}
	// The preflight is open; the data behind it is not.
	if code := serve(g, func() *http.Request {
		x := req(http.MethodGet, "/v1/sessions")
		x.Header.Set("Origin", "wails://wails")
		return x
	}()).Code; code != http.StatusUnauthorized {
		t.Fatalf("preflighted request served without a token: %d", code)
	}
}

// A token in a URL can leak — into logs, history, a Referer. Only the
// WebSocket handshake, which cannot carry a header, may use one.
func TestGateQueryTokenOnlyForWebSocket(t *testing.T) {
	g := testGate()
	if code := serve(g, req(http.MethodGet, "/v1/sessions?t=secret-token")).Code; code != http.StatusUnauthorized {
		t.Fatalf("query token accepted on a REST route: %d", code)
	}
	if code := serve(g, req(http.MethodGet, "/ws?t=secret-token")).Code; code != http.StatusOK {
		t.Fatalf("query token refused on /ws: %d", code)
	}
}

// The gate must judge the same path the router will dispatch on.
func TestGatePathIsNormalised(t *testing.T) {
	g := testGate()
	for _, p := range []string{"/v1/../v1/sessions", "//v1/sessions", "/v1/./sessions"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:7420"+p, nil)
		r.Host = "127.0.0.1:7420"
		r.RemoteAddr = "127.0.0.1:5555"
		if code := serve(g, r).Code; code != http.StatusUnauthorized {
			t.Fatalf("%s slipped past the gate: %d", p, code)
		}
	}
}

// The page holds a credential and drives a shell; it must not be framed.
func TestGateForbidsFraming(t *testing.T) {
	rec := serve(testGate(), req(http.MethodGet, "/"))
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options %q", rec.Header().Get("X-Frame-Options"))
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("CSP %q", rec.Header().Get("Content-Security-Policy"))
	}
}

// Only the desktop app's own scheme is let in from outside; a page served
// by some other local server is not.
func TestGateOriginAllowList(t *testing.T) {
	g := testGate()
	for _, o := range []string{"file://", "capacitor://localhost", "ionic://localhost", "http://localhost:9999", "https://evil.example"} {
		r := req(http.MethodGet, "/v1/sessions")
		r.Header.Set("Origin", o)
		r.Header.Set(tokenHeader, "secret-token")
		if code := serve(g, r).Code; code != http.StatusForbidden {
			t.Fatalf("origin %s answered %d", o, code)
		}
	}
	for _, o := range []string{"wails://wails", "http://wails.localhost"} {
		r := req(http.MethodGet, "/v1/sessions")
		r.Header.Set("Origin", o)
		r.Header.Set(tokenHeader, "secret-token")
		if code := serve(g, r).Code; code != http.StatusOK {
			t.Fatalf("desktop origin %s answered %d", o, code)
		}
	}
}

// Binding to a wildcard address is how the LAN and phone flows work; the
// Host is then a LAN address we cannot predict.
func TestGateWildcardBindAllowsAddresses(t *testing.T) {
	g := &gate{token: "secret-token", listen: "0.0.0.0:7420"}
	for _, h := range []string{"192.168.1.5:7420", "10.0.0.9:7420", "127.0.0.1:7420", "localhost:7420", "[fe80::1]:7420"} {
		r := req(http.MethodGet, "/healthz")
		r.Host = h
		if code := serve(g, r).Code; code != http.StatusOK {
			t.Fatalf("host %s answered %d", h, code)
		}
	}
	// A name is what rebinding needs, and a name we do not serve is refused.
	r := req(http.MethodGet, "/healthz")
	r.Host = "evil.example"
	if code := serve(g, r).Code; code != http.StatusForbidden {
		t.Fatalf("foreign name answered %d", code)
	}
}

// An operator-supplied token still has to reach the desktop app and
// cmd/probe, which read it from disk.
func TestResolveUITokenPersistsEnvToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PANE_TOKEN", "operator-chosen")
	got, err := resolveUIToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "operator-chosen" {
		t.Fatalf("token %q", got)
	}
	b, err := os.ReadFile(filepath.Join(home, ".grok", "pane.token"))
	if err != nil {
		t.Fatalf("token not written where local clients look: %v", err)
	}
	if strings.TrimSpace(string(b)) != "operator-chosen" {
		t.Fatalf("wrote %q", strings.TrimSpace(string(b)))
	}
}

func TestResolveUITokenGeneratesAndReuses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PANE_TOKEN", "")
	first, err := resolveUIToken()
	if err != nil || len(first) < 32 {
		t.Fatalf("%q %v", first, err)
	}
	again, err := resolveUIToken()
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatal("a restart invalidated every page already served")
	}
}
