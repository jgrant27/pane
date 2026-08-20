package web

import (
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
	if !strings.Contains(src, "fetchJSON(url)") {
		t.Fatal("loadHistory must retry pane HTTP")
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
