package web

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestWorkspaceIsSiblingOfRail(t *testing.T) {
	b, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	shell := strings.Index(html, `id="shell"`)
	rail := strings.Index(html, `id="rail"`)
	asideEnd := strings.Index(html, `</aside>`)
	ws := strings.Index(html, `id="workspace"`)
	if shell < 0 || rail < 0 || asideEnd < 0 || ws < 0 {
		t.Fatalf("missing landmarks shell=%d rail=%d aside=%d workspace=%d", shell, rail, asideEnd, ws)
	}
	if !(shell < rail && rail < asideEnd && asideEnd < ws) {
		t.Fatalf("workspace must be a sibling of #rail inside #shell (shell=%d rail=%d </aside>=%d workspace=%d)", shell, rail, asideEnd, ws)
	}
	if strings.Count(html, `id="workspace"`) != 1 || strings.Count(html, `id="shell"`) != 1 {
		t.Fatal("expected one shell and one workspace")
	}
	css, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `grid-template-areas: "rail workspace"`) {
		t.Fatal("style.css must keep a two-column rail/workspace grid")
	}
}

func TestRailIsResizable(t *testing.T) {
	htmlBytes, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	rail := strings.Index(html, `id="rail"`)
	split := strings.Index(html, `id="rail-split"`)
	asideEnd := strings.Index(html, `</aside>`)
	if rail < 0 || split < 0 || asideEnd < 0 {
		t.Fatal("missing #rail or #rail-split")
	}
	if !(rail < split && split < asideEnd) {
		t.Fatal("rail-split must live inside #rail")
	}
	if !strings.Contains(html, "pane-rail-w") {
		t.Fatal("index.html must restore rail width before paint")
	}

	cssBytes, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	if strings.Contains(css, "248px") {
		t.Fatal("default rail must be wider than 248px so the overlay scrollbar misses ×")
	}
	if !strings.Contains(css, "--rail-w: 280px") {
		t.Fatal("default rail width is 280px")
	}
	if !strings.Contains(css, `grid-template-columns: var(--rail-w)`) {
		t.Fatal("shell grid must size the rail from --rail-w")
	}
	if !strings.Contains(css, "#rail-split") {
		t.Fatal("style.css must style the rail splitter")
	}
	lists := strings.Index(css, "#sessions, #remote, #projects")
	if lists < 0 {
		t.Fatal("expected shared overflow rule for rail lists")
	}
	block := css[lists:]
	if j := strings.Index(block, "}"); j >= 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "padding-right") {
		t.Fatal("project/session lists must pad the overlay scrollbar off the delete control")
	}

	jsBytes, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	if !strings.Contains(js, "function bindRailResize") {
		t.Fatal("term.js must drag-resize the rail")
	}
	if !strings.Contains(js, "pane-rail-w") {
		t.Fatal("rail width must persist")
	}
}

func TestYouMessagesSitOnTheRight(t *testing.T) {
	cssBytes, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	i := strings.Index(css, ".msg.you {")
	if i < 0 {
		t.Fatal("style.css must place your messages on the right")
	}
	block := css[i:]
	if j := strings.Index(block, "}"); j >= 0 {
		block = block[:j]
	}
	if !strings.Contains(block, "margin-left: auto") || !strings.Contains(block, "text-align: right") {
		t.Fatal("your messages must sit on the right; grok stays on the left")
	}
}

func TestPhoneBrowserOpensRailDrawer(t *testing.T) {
	htmlBytes, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	if !strings.Contains(html, `id="menu"`) || !strings.Contains(html, `id="rail-backdrop"`) {
		t.Fatal("phone layout must have a Menu control and a rail backdrop")
	}
	cssBytes, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssBytes)
	phone := css
	if i := strings.Index(css, "@media (max-width: 900px)"); i >= 0 {
		phone = css[i:]
	} else {
		t.Fatal("phone media query must overlay the rail, not hide it")
	}
	if strings.Contains(phone, `#rail { display: none`) {
		t.Fatal("phone browser must keep the rail reachable")
	}
	if !strings.Contains(css, "html.rail-open #rail") {
		t.Fatal("style.css must slide the rail in as a drawer")
	}
	jsBytes, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(jsBytes)
	if !strings.Contains(js, "function bindRailMenu") || !strings.Contains(js, "rail-open") {
		t.Fatal("term.js must toggle the rail drawer")
	}
}

func TestDesktopForcesWindowSize(t *testing.T) {
	b, err := os.ReadFile("../desktop/main.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "OnDomReady") || !strings.Contains(src, "WindowSetSize(ctx, 1040, 680)") {
		t.Fatal("desktop must force 1040×680 on launch (macOS restores the last frame)")
	}
}

func TestLiveStatusSitsAboveComposer(t *testing.T) {
	b, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	header := strings.Index(html, "<header>")
	headerEnd := strings.Index(html, "</header>")
	footer := strings.Index(html, "<footer>")
	queue := strings.Index(html, `id="queue"`)
	live := strings.Index(html, `id="live"`)
	status := strings.Index(html, `id="status"`)
	spin := strings.Index(html, `id="spin"`)
	composer := strings.Index(html, `class="composer"`)
	if header < 0 || headerEnd < 0 || footer < 0 || queue < 0 || live < 0 || status < 0 || spin < 0 || composer < 0 {
		t.Fatalf("missing landmarks header=%d headerEnd=%d footer=%d queue=%d live=%d status=%d spin=%d composer=%d",
			header, headerEnd, footer, queue, live, status, spin, composer)
	}
	if live < headerEnd {
		t.Fatal("live status must not sit in the header")
	}
	if !(footer < queue && queue < live && live < spin && spin < status && status < composer) {
		t.Fatalf("expected footer → queue → live(spin,status) → composer (footer=%d queue=%d live=%d spin=%d status=%d composer=%d)",
			footer, queue, live, spin, status, composer)
	}
	css, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `#live[hidden]`) {
		t.Fatal("style.css must hide #live when idle")
	}
	js, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "function liveToolTitle") {
		t.Fatal("term.js must surface the latest in-flight tool as live status")
	}
}

func TestToolCallsUseDetails(t *testing.T) {
	js, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, `className = 'msg tools'`) || !strings.Contains(src, "createElement('details')") {
		t.Fatal("tool calls must collapse into a details/summary block")
	}
	if !strings.Contains(src, "function normPath") || !strings.Contains(src, "function samePath") {
		t.Fatal("term.js must normalize project paths (trailing slash)")
	}
	if !strings.Contains(src, "function startProjectRename") || !strings.Contains(src, "function commitProjectRename") {
		t.Fatal("term.js must let you rename a project")
	}
	if !strings.Contains(src, "Session.prototype.addAsk") || !strings.Contains(src, "waiting for you") {
		t.Fatal("term.js must render ask_user_question as a card")
	}
	if strings.Contains(src, "Grok has a question.") {
		t.Fatal("ask card must not fall back to an empty skip-only stub")
	}
	if !strings.Contains(src, "Session.prototype.addPerm") || !strings.Contains(src, "allow this?") {
		t.Fatal("term.js must wait on execute permissions")
	}
	if !strings.Contains(src, "msg.options") {
		t.Fatal("permission card must list the agent's options, including always-approve")
	}
	css, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".msg.tools summary") {
		t.Fatal("style.css must style the tools summary")
	}
	if !strings.Contains(string(css), ".msg.ask") || !strings.Contains(string(css), ".ask-opt") {
		t.Fatal("style.css must style the ask card")
	}
}

func TestNewSessionStartsDisabled(t *testing.T) {
	b, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	i := strings.Index(html, `id="new-session"`)
	if i < 0 {
		t.Fatal("missing new-session button")
	}
	tag := html[i:]
	if j := strings.Index(tag, ">"); j >= 0 {
		tag = tag[:j]
	}
	if !strings.Contains(tag, "disabled") {
		t.Fatalf("new-session must start disabled: %s", tag)
	}
	js, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(js)
	if !strings.Contains(src, "function blankSession") || !strings.Contains(src, "syncNewSession") {
		t.Fatal("term.js must gate New session on a blank unused session")
	}
	if !strings.Contains(src, "loadHistory(path, { resume: true })") {
		t.Fatal("opening a project must resume its last session")
	}
	if strings.Contains(src, "loadHistory(path, { resume: true, prune: true })") {
		t.Fatal("startup must not prune stubs then mint a new session")
	}
	resumeFn := src[strings.Index(src, "function resumeLatest"):]
	if i := strings.Index(resumeFn, "\n  function "); i > 0 {
		resumeFn = resumeFn[:i]
	}
	if strings.Contains(resumeFn, "newSession(cwd);") || strings.Contains(resumeFn, "newSession(cwd )") {
		t.Fatal("resumeLatest must not create a brand-new session")
	}
	if !strings.Contains(src, "pane-last-sid:") {
		t.Fatal("term.js must reopen the last session for the last project")
	}
	boot := chunk(t, src, "function boot()", "(function bindRailResize")
	if !strings.Contains(boot, "if (!qCwd && !qSid && meta && meta.lastCwd)") {
		t.Fatal("boot must resume grok's last session unless the URL named one")
	}
	if !strings.Contains(boot, "saved = meta.lastCwd") {
		t.Fatal("boot must take lastCwd even when this WebView already stored a project")
	}
	if !strings.Contains(boot, "qSid = meta.lastSid") {
		t.Fatal("boot must reopen grok's last session id, not mint a blank one")
	}
	if !strings.Contains(src, "function cycleSession") || !strings.Contains(src, "function cycleProject") {
		t.Fatal("term.js must cycle sessions and projects from the keyboard")
	}
	if !strings.Contains(src, "Usage limits") || !strings.Contains(src, "limitMonthly") {
		t.Fatal("usage pop must show account usage limits")
	}
	if !strings.Contains(src, "of weekly included") || !strings.Contains(src, "limitKind") {
		t.Fatal("usage pop must show SuperGrok weekly credits, not leftover dollar fields")
	}
	if !strings.Contains(src, "connecting… queued") {
		t.Fatal("send must queue when the session is not connected")
	}
	if !strings.Contains(src, "function stepComposerHistory") || !strings.Contains(src, "function rememberComposer") {
		t.Fatal("composer must bind up/down to sent-message history")
	}
	if !strings.Contains(src, "ArrowUp") || !strings.Contains(src, "ArrowDown") {
		t.Fatal("composer history must use the arrow keys")
	}
	if !strings.Contains(src, "fetchJSON(url") {
		t.Fatal("loadHistory must retry pane HTTP")
	}
	if !strings.Contains(src, "function authFetch") || !strings.Contains(src, "X-Pane-Token") {
		t.Fatal("API calls must carry the UI token")
	}
	if !strings.Contains(src, "function ownsToken") {
		t.Fatal("the token must not be sent to a pane the page does not belong to")
	}
	if strings.Contains(src, "\n    return fetch(paneHTTP()") {
		t.Fatal("every pane call must go through authFetch")
	}
	if !strings.Contains(src, "opts.prune ? { method: 'POST' }") {
		t.Fatal("pruning deletes sessions and must not ride on a GET")
	}
	if !strings.Contains(src, "function paintProjectList") {
		t.Fatal("projects rail must paint the current project even when pane is down")
	}
	if !strings.Contains(src, "function liveSessionFor") || !strings.Contains(src, "function deactivateView") {
		t.Fatal("switching projects must hide the other project's streaming transcript")
	}
	if !strings.Contains(src, "function ensureConnected") {
		t.Fatal("switching projects must dial if that project's socket is down")
	}
	if !strings.Contains(src, "if (!liveSessionFor(cwd)) newSession(cwd)") {
		t.Fatal("opening a project with no live tab must create a connection")
	}
	if !strings.Contains(src, "if (project && !samePath(cwd, project)) return") {
		t.Fatal("a late history fetch must not clobber the project you just switched to")
	}
	if !strings.Contains(src, "function kickReconnects") || !strings.Contains(src, "visibilitychange") {
		t.Fatal("the page must redial when it becomes visible after pane/agent restart")
	}
	// #59 shared focus: POST /v1/focus on switch; resume applies /meta lastCwd/lastSid.
	kick := chunk(t, src, "function kickReconnects", "function activate")
	if !strings.Contains(src, "function applyServerFocus") || !strings.Contains(src, "function reportFocus") {
		t.Fatal("#59: clients must publish and follow the shared pane focus")
	}
	if !strings.Contains(src, "'/v1/focus'") {
		t.Fatal("#59: switching sessions must POST /v1/focus so other clients can follow")
	}
	if !strings.Contains(kick, "applyServerFocus") {
		t.Fatal("#59: resume must apply /meta lastCwd/lastSid, not stay on this WebView's tab")
	}
	// #58: iPhone resume left the socket OPEN and skipped replay after the first handshake.
	redial := chunk(t, src, "function redialAll", "function kickReconnects")
	if !strings.Contains(redial, "s.connect();") {
		t.Fatal("#58: kickReconnects must redial, not trust a frozen OPEN socket")
	}
	if strings.Contains(redial, "ensureConnected(s)") {
		t.Fatal("#58: ensureConnected skips OPEN sockets; iPhone resume reports OPEN after freeze")
	}
	if strings.Contains(src, "s.resumeID && !s.seenReady") {
		t.Fatal("#58: reconnect must replay; gating on !seenReady leaves the phone on a stale paint")
	}
	if !strings.Contains(src, "var replay = !!s.resumeID") {
		t.Fatal("#58: a session id on reconnect must request replay so the tail catches up")
	}
	if !strings.Contains(src, "Session.prototype.resetPaint") {
		t.Fatal("#58: catch-up replay must replace the frozen transcript, not append a second copy")
	}
	if !strings.Contains(src, "clearTimeout(s.retry)") {
		t.Fatal("connect must cancel a pending reconnect before dialing again")
	}
	if !strings.Contains(src, "function foreignSession") {
		t.Fatal("a session socket must ignore agent chunks tagged for another session")
	}
	if !strings.Contains(src, "pane not reachable") {
		t.Fatal("a down pane must say so instead of hanging on connecting")
	}
	if !strings.Contains(src, "cwd-path") {
		t.Fatal("path label must keep the leading slash")
	}
	if !strings.Contains(src, "function nearBottom") || !strings.Contains(src, "this.stick") {
		t.Fatal("streaming must not force-tail if you scrolled up")
	}
	if !strings.Contains(html, `id="jump-bottom"`) {
		t.Fatal("log must have a jump-to-latest control")
	}
	if !strings.Contains(html, `id="autoscroll"`) || !strings.Contains(src, "pane-autoscroll") {
		t.Fatal("header must have a persistent auto-scroll setting")
	}
	if !strings.Contains(src, "function bindHeaderToggle") {
		t.Fatal("Thoughts/Follow must bind clicks that the desktop webview cannot steal")
	}
}

func termJS(t *testing.T) string {
	t.Helper()
	b, err := FS.ReadFile("term.js")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// chunk returns the source from the first line containing start up to the
// next line containing end, so an assertion can be pinned to one function.
func chunk(t *testing.T, src, start, end string) string {
	t.Helper()
	i := strings.Index(src, start)
	if i < 0 {
		t.Fatalf("term.js no longer contains %q", start)
	}
	rest := src[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("term.js: no %q after %q", end, start)
	}
	return rest[:j]
}

// inlineScripts returns the body of every <script> element in html that
// has no src attribute — the ones a CSP has to name by hash.
func inlineScripts(html string) []string {
	var out []string
	rest := html
	for {
		i := strings.Index(rest, "<script")
		if i < 0 {
			return out
		}
		rest = rest[i:]
		j := strings.Index(rest, ">")
		if j < 0 {
			return out
		}
		open := rest[:j+1]
		body := rest[j+1:]
		k := strings.Index(body, "</script>")
		if k < 0 {
			return out
		}
		if !strings.Contains(open, "src=") {
			out = append(out, body[:k])
		}
		rest = body[k:]
	}
}

func cspMeta(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, `http-equiv="Content-Security-Policy"`)
	if i < 0 {
		t.Fatal("index.html must carry a Content-Security-Policy meta: agent markdown renders in this document")
	}
	rest := html[i:]
	open := `content="`
	j := strings.Index(rest, open)
	if j < 0 {
		t.Fatal("malformed Content-Security-Policy meta")
	}
	k := strings.Index(rest[j+len(open):], `"`)
	if k < 0 {
		t.Fatal("malformed Content-Security-Policy meta")
	}
	return rest[j+len(open) : j+len(open)+k]
}

func TestPageDeniesRemoteLoadsAndInlineScript(t *testing.T) {
	b, err := FS.ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	csp := cspMeta(t, html)
	head := strings.Index(html, `http-equiv="Content-Security-Policy"`)
	if first := strings.Index(html, "<script"); first >= 0 && first < head {
		t.Fatal("the policy must be parsed before the first script it governs")
	}
	for _, want := range []string{
		"default-src 'self'",
		"img-src 'self' data: blob:",
		"style-src 'self'",
		"object-src 'none'",
		"base-uri 'none'",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("policy is missing %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "'unsafe-inline'") || strings.Contains(csp, "'unsafe-eval'") {
		t.Fatalf("an unsafe-inline policy would not stop injected markup: %s", csp)
	}
	scripts := inlineScripts(html)
	if len(scripts) == 0 {
		t.Fatal("expected inline scripts in index.html")
	}
	for _, s := range scripts {
		sum := sha256.Sum256([]byte(s))
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(csp, want) {
			t.Fatalf("script-src must list %s for the inline script starting %.40q", want, strings.TrimSpace(s))
		}
	}
}

// arrayItems returns the elements of the `name: [...]` (or `name = [...]`)
// literal that follows name in src. Membership in the array is the claim
// worth making — the same word appearing somewhere else in the function
// would satisfy a plain substring check while forbidding nothing.
func arrayItems(t *testing.T, src, name string) []string {
	t.Helper()
	i := strings.Index(src, name)
	if i < 0 {
		t.Fatalf("term.js no longer has %s", name)
	}
	rest := src[i:]
	open := strings.Index(rest, "[")
	end := strings.Index(rest, "]")
	if open < 0 || end < open {
		t.Fatalf("%s is not an array literal: %.60q", name, rest)
	}
	var out []string
	for _, p := range strings.Split(rest[open+1:end], ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func hasItem(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// bareReturnHTML matches `return html;` at any indentation: pinning this to
// one literal amount of leading space makes reindenting renderMd enough to
// hand unsanitized markup back.
var bareReturnHTML = regexp.MustCompile(`return\s+html\s*;`)

func TestMarkdownSanitizerFailsClosed(t *testing.T) {
	md := chunk(t, termJS(t), "function renderMd", "\n  function openExternal")
	if !strings.Contains(md, "DOMPurify.isSupported") {
		t.Fatal("a DOMPurify that reports itself unsupported returns its input unchanged — check isSupported")
	}
	if bareReturnHTML.MatchString(md) {
		t.Fatal("renderMd must never hand unsanitized markup to innerHTML")
	}
	// The last thing renderMd can do without a sanitizer is escape.
	if i := strings.LastIndex(md, "return "); i < 0 || !strings.HasPrefix(md[i:], "return '<p>' + escapeText(raw)") {
		t.Fatal("renderMd must fall back to escaped text, not to markup")
	}
	tags := arrayItems(t, md, "FORBID_TAGS")
	for _, tag := range []string{"'style'", "'form'", "'input'", "'button'", "'img'", "'base'"} {
		if !hasItem(tags, tag) {
			t.Fatalf("FORBID_TAGS must list %s, has %v", tag, tags)
		}
	}
	attrs := arrayItems(t, md, "FORBID_ATTR")
	for _, a := range []string{"'style'", "'class'", "'id'", "'src'", "'srcset'"} {
		if !hasItem(attrs, a) {
			t.Fatalf("%s lets agent markdown restyle or address the permission card, has %v", a, attrs)
		}
	}
}

func TestStaleSessionIDIsNotResumedBlindly(t *testing.T) {
	src := termJS(t)
	resume := chunk(t, src, "function resumeLatest", "\n  function loadRemote")
	if !strings.Contains(resume, "function resumeLatest(cwd, list, listComplete)") {
		t.Fatal("resumeLatest must know whether the session list is complete, not merely that it arrived")
	}
	if !strings.Contains(resume, "!listComplete) pick = { id: lastSid") {
		t.Fatal("a stored id missing from a complete list is a deleted session, not a pick")
	}
	load := chunk(t, src, "function loadHistory", "function applyGrokTitles")
	if !strings.Contains(load, "resumeLatest(cwd, diskSessions, diskSessions.length < sessionListCap)") {
		t.Fatal("only a list short enough to be the whole truth may be treated as authoritative")
	}
	if !strings.Contains(load, "resumeLatest(cwd, [], false)") {
		t.Fatal("an unreachable pane must still let the stored id be retried")
	}
	errCase := chunk(t, src, "case 'err':", "case 'warn':")
	if !strings.Contains(errCase, "!s.seenReady") || !strings.Contains(errCase, "forget('pane-last-sid:'") {
		t.Fatal("a handshake that errors must drop the id it tried to resume")
	}
	if !strings.Contains(errCase, "s.resumeID = ''") {
		t.Fatal("the next dial must fall back to a new session, not the same dead id")
	}
	if !strings.Contains(errCase, "s.giveUp = true") {
		t.Fatal("a pre-ready error with nothing left to try must stop the redial loop")
	}
	closeFn := chunk(t, src, "ws.onclose = function", "ws.onmessage")
	if !strings.Contains(closeFn, "if (s.giveUp) return;") {
		t.Fatal("onclose must honour a session that gave up")
	}
	sendFn := chunk(t, src, "function send() {", "sendBtn.addEventListener")
	if !strings.Contains(sendFn, "active.giveUp = false") {
		t.Fatal("giving up must not strand the tab: using it has to dial again")
	}
}

func TestShutdownDisarmsTheReconnect(t *testing.T) {
	src := termJS(t)
	closeFn := chunk(t, src, "ws.onclose = function", "ws.onmessage")
	if !strings.Contains(closeFn, "s.retry = setTimeout(") {
		t.Fatal("the reconnect timer must be held so it can be cancelled")
	}
	shut := chunk(t, src, "Session.prototype.shutdown", "function closeSession")
	if !strings.Contains(shut, "clearTimeout(this.retry)") {
		t.Fatal("shutdown must disarm the pending reconnect, or a deleted session reopens itself")
	}
	conn := chunk(t, src, "Session.prototype.connect = function", "ws.onopen")
	if !strings.Contains(conn, "if (s.dead) return;") {
		t.Fatal("connect must refuse to dial for a tab that is gone")
	}
}

func TestSocketDownDoesNotLockTheRail(t *testing.T) {
	src := termJS(t)
	rail := chunk(t, src, "function railLocked", "function grow")
	if strings.Contains(rail, "return !!(active && active.busy);") {
		t.Fatal("busy alone also means 'socket down' — the rail must not lock on it")
	}
	held := chunk(t, src, "function turnInFlight", "function railLocked")
	if !strings.Contains(held, "s.busy && s.live") {
		t.Fatal("a turn is in flight only when the socket has been ready")
	}
	if !strings.Contains(chunk(t, src, "case 'ready':", "case 'you':"), "s.live = true") {
		t.Fatal("ready is what makes a session live")
	}
	closeFn := chunk(t, src, "ws.onclose = function", "ws.onmessage")
	if !strings.Contains(closeFn, "s.live = false") {
		t.Fatal("a closed socket is not live")
	}
	closeSess := chunk(t, src, "function closeSession", "function applyCatalog")
	if strings.Contains(closeSess, "if (!s || s.busy) return;") {
		t.Fatal("⌘W must still close a tab whose pane went away")
	}
	if !strings.Contains(closeSess, "finish the current turn first") {
		t.Fatal("a refused close must say why")
	}
	open := chunk(t, src, "function openProject", "projectBtn.addEventListener")
	if !strings.Contains(open, "finish the current turn first") {
		t.Fatal("a refused Change project must say why")
	}
	if !strings.Contains(src, "changeBtn.disabled = pickingProject || locked") {
		t.Fatal("Change project must look disabled while it is refusing")
	}
}

func TestSendUsesTheSessionCwd(t *testing.T) {
	src := termJS(t)
	sendFn := chunk(t, src, "function send() {", "sendBtn.addEventListener")
	if strings.Contains(sendFn, "if (!project) {") {
		t.Fatal("send must not gate on the global project: a session can carry a server-chosen cwd")
	}
	if !strings.Contains(sendFn, "(active && !active.dead && active.cwd) || project") {
		t.Fatal("send must use the active session's cwd, the way addFiles does")
	}
	ready := chunk(t, src, "case 'ready':", "case 'you':")
	if !strings.Contains(ready, "if (s.cwd && !project) setProject(s.cwd);") {
		t.Fatal("a cwd the server chose must become the project")
	}
}

func TestQueuedAttachmentsAreReleased(t *testing.T) {
	src := termJS(t)
	rel := chunk(t, src, "function releaseFiles(files, cwd)", "function dropPending")
	if !strings.Contains(rel, "URL.revokeObjectURL") || !strings.Contains(rel, "method: 'DELETE'") {
		t.Fatal("releaseFiles must revoke the preview and delete the uploaded copy")
	}
	drop := chunk(t, src, "function dropPending", "function paintChips")
	if !strings.Contains(drop, "releaseFiles([f]") {
		t.Fatal("the composer chip × must go through the shared cleanup")
	}
	row := chunk(t, src, "x.title = 'Remove from queue';", "row.appendChild(mark);")
	if !strings.Contains(row, "releaseFiles(") {
		t.Fatal("removing a queued message leaks its uploaded copy into the project")
	}
	esc := chunk(t, src, "active.queue.pop()", "paintSessions();")
	if !strings.Contains(esc, "releaseFiles(") {
		t.Fatal("Escape popping the queue leaks its uploaded copy into the project")
	}
	shut := chunk(t, src, "Session.prototype.shutdown", "function closeSession")
	if !strings.Contains(shut, "releaseFiles(") {
		t.Fatal("closing a tab with a queue leaks every copy that queue uploaded")
	}
}

func TestEscapeCancelsARename(t *testing.T) {
	src := termJS(t)
	live := chunk(t, src, "inp.setAttribute('aria-label', 'Session name')", "row.appendChild(inp);")
	if !strings.Contains(live, "sessPaintKey = ''") {
		t.Fatal("Escape must clear the paint key or the repaint short-circuits and the box stays up")
	}
	if !strings.Contains(live, "if (cancelled) return;") {
		t.Fatal("the blur after Escape must not apply the cancelled name")
	}
	disk := chunk(t, src, "inp2.setAttribute('aria-label', 'Session name')", "row.appendChild(inp2);")
	if strings.Contains(disk, "commitRename(renaming,") {
		t.Fatal("a disk row must not read the mutable `renaming` from its handlers — Escape nulls it")
	}
	if !strings.Contains(disk, "commitRename(target,") || !strings.Contains(disk, "sessPaintKey = ''") {
		t.Fatal("the disk row must hold its own rename target and clear the paint key on Escape")
	}
	if !strings.Contains(disk, "if (cancelled2) return;") {
		t.Fatal("the blur after Escape must not apply the cancelled name")
	}
	proj := chunk(t, src, "inp.setAttribute('aria-label', 'Project name')", "row.appendChild(inp);")
	if !strings.Contains(proj, "if (cancelledProj) return;") {
		t.Fatal("a click during the async repaint must not commit the cancelled project name")
	}
}

func TestLinkGuardCoversEveryAnchor(t *testing.T) {
	guard := chunk(t, termJS(t), "closest('a[href]')", "}, true);")
	if strings.Contains(guard, "if (!/^(https?:|mailto:)/i.test(href)) return;") {
		t.Fatal("a relative link in agent markdown must not navigate the tab away")
	}
	if !strings.Contains(guard, "new URL(href, location.href)") {
		t.Fatal("the link guard must resolve the href to decide the scheme")
	}
	pd := strings.Index(guard, "e.preventDefault();")
	open := strings.Index(guard, "openExternal(")
	if pd < 0 || open < 0 || pd > open {
		t.Fatal("every anchor must be neutralized before any scheme check opens it")
	}
}

// jsRegexp compiles the `name = /…/` literal from src with Go's engine, so
// an assertion can feed it the strings the server actually sends instead of
// checking that the source happens to mention them.
func jsRegexp(t *testing.T, src, name string) *regexp.Regexp {
	t.Helper()
	head := name + " = /"
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("term.js no longer declares %s as a regexp literal", name)
	}
	rest := src[i+len(head):]
	end := strings.Index(rest, "/")
	if end < 0 {
		t.Fatalf("%s has no closing delimiter", name)
	}
	flags := ""
	if strings.HasPrefix(rest[end+1:], "i") {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + rest[:end])
	if err != nil {
		t.Fatalf("%s does not compile as a Go regexp: %v", name, err)
	}
	return re
}

// jsNumber reads the value of a `var name = 1234;` declaration in src.
func jsNumber(t *testing.T, src, name string) int {
	t.Helper()
	head := name + " = "
	i := strings.Index(src, head)
	if i < 0 {
		t.Fatalf("term.js no longer declares %s", name)
	}
	rest := src[i+len(head):]
	end := strings.IndexAny(rest, ";\n")
	if end < 0 {
		t.Fatalf("%s is not a plain declaration", name)
	}
	v, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		t.Fatalf("%s is not a number: %v", name, err)
	}
	return v
}

// unloadable mirrors term.js's unloadableSession so the two regexps can be
// judged on real error text rather than on how they are spelled.
func unloadable(gone, sess *regexp.Regexp, text, id string) bool {
	if !gone.MatchString(text) {
		return false
	}
	if sess.MatchString(text) {
		return true
	}
	return id != "" && strings.Contains(strings.ToLower(text), strings.ToLower(id))
}

func TestPreReadyErrorOnlyGivesUpOnADeadSession(t *testing.T) {
	src := termJS(t)
	errCase := chunk(t, src, "case 'err':", "case 'warn':")
	if !strings.Contains(errCase, "unloadableSession(msg.text, s.resumeID)") {
		t.Fatal("a pre-ready error must be read before the resumed id is thrown away")
	}
	gone := jsRegexp(t, src, "gonePhrase")
	sess := jsRegexp(t, src, "sessionWord")
	const id = "0199f0aa-1b2c"
	// The agent between lives. Every one of these can arrive before ready
	// and none of them says anything about the session the tab asked for.
	for _, text := range []string{
		"no grok agent at ws://127.0.0.1:2419/acp — start one with: make agent",
		"agent closed",
		"no agent",
		"agent rpc timeout",
		"browser gone",
		"dial tcp 127.0.0.1:2419: connect: connection refused",
	} {
		if unloadable(gone, sess, text, id) {
			t.Fatalf("%q is the agent restarting — the tab must keep its id and retry", text)
		}
	}
	// The session really is gone; retrying the same id only repeats this.
	for _, text := range []string{
		"session not found",
		"no such session: " + id,
		"unknown session id",
		"session " + id + " does not exist",
		"invalid session",
	} {
		if !unloadable(gone, sess, text, id) {
			t.Fatalf("%q means this id cannot be loaded — retrying it traps the tab", text)
		}
	}
	if grace := jsNumber(t, src, "handshakeGraceMs"); grace < 45000 {
		t.Fatalf("giving up after %dms is shorter than an agent restart", grace)
	}
	if !strings.Contains(errCase, "Date.now() - s.handshakeSince >= handshakeGraceMs") {
		t.Fatal("the redial backs off, so the give-up threshold must be a clock and not a count")
	}
}

func TestSessionListCapMatchesTheServer(t *testing.T) {
	b, err := os.ReadFile("../history.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`listGrokSessions\(cwd, (\d+)\)`).FindStringSubmatch(string(b))
	if m == nil {
		t.Fatal("history.go no longer caps the per-project session list")
	}
	want, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatal(err)
	}
	if got := jsNumber(t, termJS(t), "sessionListCap"); got != want {
		t.Fatalf("term.js reads a full page as %d sessions, the server sends at most %d", got, want)
	}
}

func TestComposerAttachmentsDieWithTheirTab(t *testing.T) {
	src := termJS(t)
	shut := chunk(t, src, "Session.prototype.shutdown", "function closeSession")
	if !strings.Contains(shut, "releaseFiles(pending, cwd)") {
		t.Fatal("closing a tab mid-compose leaks the copies its chips uploaded into the project")
	}
	if !strings.Contains(shut, "pending = [];") {
		t.Fatal("the chips must go with the files they point at")
	}
	drop := chunk(t, src, "function dropTab", "function wipeProject")
	shutAt := strings.Index(drop, "s.shutdown();")
	clearAt := strings.Index(drop, "active = null;")
	if shutAt < 0 || clearAt < 0 {
		t.Fatalf("dropTab no longer shuts the tab down and clears active: %s", drop)
	}
	if shutAt > clearAt {
		t.Fatal("shutdown only knows the composer is this tab's while it is still the active one")
	}
}

func TestMessagesShowLocalTime(t *testing.T) {
	src := termJS(t)
	if !strings.Contains(src, "function fmtWhen") || !strings.Contains(src, "function stampWho") {
		t.Fatal("messages must format a local clock time")
	}
	if !strings.Contains(src, "toLocaleTimeString") {
		t.Fatal("the stamp is wall-clock time, not a raw epoch")
	}
	if !strings.Contains(src, "stampWho(who, 'you', at)") || !strings.Contains(src, "stampWho(who, 'grok', at)") {
		t.Fatal("both your bubbles and grok's must carry the time")
	}
	if !strings.Contains(src, "s.addYou(msg.text || '', msg.files || [], msg.at)") {
		t.Fatal("replay/live you frames must pass at into the bubble")
	}
	if !strings.Contains(src, "s.addOut(msg.text || '', msg.at)") {
		t.Fatal("replay/live grok frames must pass at into the bubble")
	}
	if !strings.Contains(src, "when.className = 'when'") || !strings.Contains(src, `createElement('time')`) {
		t.Fatal("the stamp must be a visible <time class=\"when\">, not a title tooltip")
	}
	if !strings.Contains(src, "if (!ms) ms = Date.now()") {
		t.Fatal("a live send with no at still gets a clock time")
	}
	css, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".msg .when") || !strings.Contains(string(css), "text-transform: none") {
		t.Fatal("the time must not inherit who { text-transform: lowercase }")
	}
}

func TestAddYouDoesNotDoubleTheAsk(t *testing.T) {
	fn := chunk(t, termJS(t), "Session.prototype.addYou", "Session.prototype.addOut")
	if !strings.Contains(fn, "last.classList.contains('you')") || !strings.Contains(fn, "prev.textContent === String(text)") {
		t.Fatal("addYou must skip a consecutive duplicate ask (composer echo + disk/replay)")
	}
}

func TestReconnectMidTurnOffersQueue(t *testing.T) {
	ready := chunk(t, termJS(t), "case 'ready':", "case 'you':")
	if !strings.Contains(ready, "msg.busy === true") {
		t.Fatal("a tab that reattaches mid-turn must take the server's busy hint, not assume idle")
	}
	if !strings.Contains(ready, "s.busy = midTurn;") || !strings.Contains(ready, "setBusy(midTurn)") {
		t.Fatal("the hint has to reach the composer, which is what says Queue instead of Send")
	}
	if strings.Contains(ready, "s.busy = false;") {
		t.Fatal("ready must not overwrite the hint with an unconditional idle")
	}
	// `=== true` is the guard: a server too old to send the field leaves it
	// undefined, and undefined has to keep reading as idle.
	if strings.Contains(ready, "if (msg.busy)") || strings.Contains(ready, "!!msg.busy") {
		t.Fatal("the busy hint must be compared strictly so a server that omits it still works")
	}
}

func TestLinkGuardScrollsInPageAnchors(t *testing.T) {
	src := termJS(t)
	guard := chunk(t, src, "closest('a[href]')", "}, true);")
	if !strings.Contains(guard, "sameDocument(u)") || !strings.Contains(guard, "scrollToAnchor(u.hash)") {
		t.Fatal("an #anchor is a scroll, not a browser window")
	}
	if strings.Index(guard, "sameDocument(u)") > strings.Index(guard, "openExternal(") {
		t.Fatal("the same-document case must be settled before anything is opened externally")
	}
	if !strings.Contains(guard, "if (web && u.origin === location.origin) return;") {
		t.Fatal("opening a same-origin URL externally just moves the navigation into another window")
	}
	if !strings.Contains(guard, "externalSchemes.indexOf(u.protocol) < 0") {
		t.Fatal("the allowlist must decide every scheme, not sit beside a second one")
	}
	same := chunk(t, src, "function sameDocument", "function scrollToAnchor")
	if !strings.Contains(same, "u.hash") || !strings.Contains(same, "stripHash(location.href)") {
		t.Fatal("same-document means the same URL but for the fragment — a relative path is not one")
	}
	schemes := arrayItems(t, src, "var externalSchemes")
	for _, want := range []string{"'http:'", "'https:'", "'mailto:'", "'tel:'", "'sms:'"} {
		if !hasItem(schemes, want) {
			t.Fatalf("the shells forward %s — dropping it here makes the link do nothing, has %v", want, schemes)
		}
	}
}

func TestChangeProjectDimsOnlyWhenItRefuses(t *testing.T) {
	b, err := FS.ReadFile("style.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	if strings.Contains(css, `html[data-busy="true"] #change-project`) {
		t.Fatal("data-busy is also set when the socket is down, which is not a refusal — the CSS would undo the split")
	}
	// What is left has to still dim it, or a button that refuses looks live.
	i := strings.Index(css, "#rail > button:disabled")
	if i < 0 {
		t.Fatal("style.css no longer dims a disabled rail button")
	}
	rule := css[i:]
	if j := strings.Index(rule, "}"); j > 0 {
		rule = rule[:j]
	}
	if !strings.Contains(rule, "opacity") || !strings.Contains(rule, "pointer-events: none") {
		t.Fatalf("the disabled state railLocked sets must be the one that shows: %s", rule)
	}
	js := termJS(t)
	if !strings.Contains(js, "changeBtn.disabled = pickingProject || locked") {
		t.Fatal("with the CSS gone, changeBtn.disabled is the only thing left saying Change project refuses")
	}
}
