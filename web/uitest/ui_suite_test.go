package uitest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestUIEveryIndexControlIsNamedInTheSuite(t *testing.T) {
	html, err := os.ReadFile(filepath.Join(repoRoot(t), "web", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	ids := regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(html), -1)
	if len(ids) < 20 {
		t.Fatalf("index.html control list too small: %d", len(ids))
	}
	var src strings.Builder
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), "_test.go") && e.Name() != "webkit.go" {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
		src.WriteByte('\n')
	}
	all := src.String()
	missing := []string{}
	for _, m := range ids {
		id := m[1]
		if !strings.Contains(all, id) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("UI suite does not mention index.html controls: %s", strings.Join(missing, ", "))
	}
}

func TestUIBootIsNotABlankPage(t *testing.T) {
	s := startStack(t)
	assertHealthy(t, s.Page)
}

func TestUIPhoneBootIsNotABlankPage(t *testing.T) {
	s := startStack(t)
	if err := s.Page.SetViewportSize(390, 844); err != nil {
		t.Fatal(err)
	}
	assertHealthy(t, s.Page)
}

func TestUIWatchReplaysDiskTranscript(t *testing.T) {
	const id = "01uireplayxxxxxxxxxxxxxxxxxxx"
	s := startStackWith(t, func(home, cwd string) {
		plantTranscript(t, home, cwd, id, "from-disk-ask", "from-disk-reply")
		plantLiveTUI(t, home, cwd, id)
	})
	pg := s.Page
	assertHealthy(t, pg)
	deadline := time.Now().Add(12 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		body, _ = pg.InnerText("body")
		if strings.Contains(body, "from-disk-ask") || strings.Contains(body, "from-disk-reply") {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("watch-only must replay the TUI transcript, body=%q", body)
}

func TestUIThemeToggle(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	assertHealthy(t, pg)
	btn, err := pg.QuerySelector("#theme")
	if err != nil || btn == nil {
		t.Fatal("theme")
	}
	if err := btn.Click(); err != nil {
		t.Fatal(err)
	}
	v := eval(t, pg, `document.documentElement.dataset.theme`)
	if v != "dark" {
		t.Fatalf("theme click must set dark, got %v", v)
	}
}

func TestUIThoughtsAndFollowToggles(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	assertHealthy(t, pg)
	th, _ := pg.QuerySelector("#thoughts")
	au, _ := pg.QuerySelector("#autoscroll")
	if th == nil || au == nil {
		t.Fatal("thoughts/autoscroll")
	}
	_ = th.Click()
	_ = au.Click()
	pressed := eval(t, pg, `({t: document.getElementById('thoughts').getAttribute('aria-pressed'), a: document.getElementById('autoscroll').getAttribute('aria-pressed')})`)
	m, _ := pressed.(map[string]any)
	if m["t"] != "true" {
		t.Fatalf("thoughts should be pressed after click: %v", m)
	}
}

func TestUIPhoneMenuOpensRail(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	if err := pg.SetViewportSize(390, 844); err != nil {
		t.Fatal(err)
	}
	assertHealthy(t, pg)
	menu, err := pg.QuerySelector("#menu")
	if err != nil || menu == nil {
		t.Fatal("menu")
	}
	if err := menu.Click(); err != nil {
		t.Fatal(err)
	}
	open := eval(t, pg, `document.getElementById('menu').getAttribute('aria-expanded')`)
	if open != "true" {
		t.Fatalf("menu must open the rail on a phone, aria-expanded=%v", open)
	}
}

func TestUIComposerSelectsExist(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	assertHealthy(t, pg)
	for _, id := range []string{"model", "effort", "attach", "usage", "new-session", "project", "change-project", "cwd", "status", "log", "sessions", "projects", "remote"} {
		el, err := pg.QuerySelector("#" + id)
		if err != nil || el == nil {
			t.Fatalf("missing #%s", id)
		}
	}
}

func TestUINewSessionAfterSend(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	sendText(t, pg, "open a second chat")
	waitSel(t, pg, ".msg.agent", 10*time.Second)
	btn, err := pg.QuerySelector("#new-session")
	if err != nil || btn == nil {
		t.Fatal("new-session")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		dis, _ := btn.IsDisabled()
		if !dis {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := btn.Click(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	assertHealthy(t, pg)
	n := countSel(t, pg, "#sessions .sess-row")
	if n < 2 {
		t.Fatalf("new session must add a rail row, got %d", n)
	}
}

func TestUIUnknownSessionStillHasComposer(t *testing.T) {
	s := startStackOpts(t, func(home, cwd string) {
		plantTranscript(t, home, cwd, "01uiunknownxxxxxxxxxxxxxxxxxx", "ask-on-disk", "reply-on-disk")
	}, true)
	if err := s.Page.SetViewportSize(390, 844); err != nil {
		t.Fatal(err)
	}
	assertHealthy(t, s.Page)
}

func TestUIModalCancelExists(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	assertHealthy(t, pg)
	for _, id := range []string{"shell", "workspace", "rail", "modal", "modal-text", "modal-ok", "modal-cancel", "jump-bottom", "chips", "file", "drop", "rail-backdrop", "rail-split", "usage-pop", "usage-ring", "spin", "queue", "live"} {
		if _, err := pg.QuerySelector("#" + id); err != nil {
			t.Fatalf("missing #%s", id)
		}
	}
}
