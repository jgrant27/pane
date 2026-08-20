package web

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
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

func TestMarkdownSanitizerFailsClosed(t *testing.T) {
	md := chunk(t, termJS(t), "function renderMd", "\n  function openExternal")
	if !strings.Contains(md, "DOMPurify.isSupported") {
		t.Fatal("a DOMPurify that reports itself unsupported returns its input unchanged — check isSupported")
	}
	if strings.Contains(md, "\n    return html;") {
		t.Fatal("renderMd must never hand unsanitized markup to innerHTML")
	}
	for _, tag := range []string{"'style'", "'form'", "'input'", "'button'", "'img'"} {
		if !strings.Contains(md, "FORBID_TAGS") || !strings.Contains(md, tag) {
			t.Fatalf("agent markdown must not be able to emit %s", tag)
		}
	}
	if !strings.Contains(md, "FORBID_ATTR") {
		t.Fatal("renderMd must forbid attributes, not only tags")
	}
	attrs := chunk(t, md, "FORBID_ATTR", "]")
	for _, a := range []string{"'style'", "'class'", "'id'", "'src'"} {
		if !strings.Contains(attrs, a) {
			t.Fatalf("%s lets agent markdown restyle or address the permission card", a)
		}
	}
}

func TestStaleSessionIDIsNotResumedBlindly(t *testing.T) {
	src := termJS(t)
	resume := chunk(t, src, "function resumeLatest", "\n  function loadRemote")
	if !strings.Contains(resume, "function resumeLatest(cwd, list, listOK)") {
		t.Fatal("resumeLatest must know whether the session list actually arrived")
	}
	if !strings.Contains(resume, "!listOK) pick = { id: lastSid") {
		t.Fatal("a stored id missing from a list that did arrive is a deleted session, not a pick")
	}
	load := chunk(t, src, "function loadHistory", "function applyGrokTitles")
	if !strings.Contains(load, "resumeLatest(cwd, diskSessions, true)") {
		t.Fatal("a list that loaded must be treated as authoritative")
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
