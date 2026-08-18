package web

import (
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
}
