package uitest

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDriverIsWebKitNotChrome(t *testing.T) {
	src, err := os.ReadFile("webkit.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if strings.Contains(s, "pw.Chromium") || strings.Contains(s, "pw.Firefox") || strings.Contains(s, ".Chromium.Launch") {
		t.Fatal("UI tests must launch WebKit only")
	}
	if !strings.Contains(s, "pw.WebKit.Launch") {
		t.Fatal("must launch playwright WebKit")
	}
}

func TestUISendShowsYouAndAgentOnScreen(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	sendText(t, pg, "ping from webkit")
	if !strings.Contains(textOf(t, pg, ".msg.you"), "ping from webkit") {
		t.Fatalf("you bubble: %q", textOf(t, pg, ".msg.you"))
	}
	waitSel(t, pg, ".msg.agent", 10*time.Second)
	if !strings.Contains(textOf(t, pg, ".msg.agent"), "hello-from-agent") {
		t.Fatalf("agent bubble: %q", textOf(t, pg, ".msg.agent"))
	}
}

func TestUIFollowsLiveTUISession(t *testing.T) {
	const id = "01uitestlivexxxxxxxxxxxxxxxxx"
	s := startStackWith(t, func(home, cwd string) {
		plantSession(t, home, cwd, id, "Terminal Live")
		plantLiveTUI(t, home, cwd, id)
		remember := []byte(`{"cwd":"` + cwd + `","sid":"01deletedstalexxxxxxxxxxxxxxxx","title":"stale"}`)
		_ = os.WriteFile(home+"/pane-last.json", remember, 0o600)
	})
	pg := s.Page
	waitSel(t, pg, "#sessions", 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	var rail string
	for time.Now().Before(deadline) {
		rail = textOf(t, pg, "#sessions")
		if strings.Contains(rail, "Terminal Live") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("rail did not follow live TUI, got %q", rail)
}

func TestUIDeleteOneSessionLeavesTheOther(t *testing.T) {
	keep := "01uitestkeepxxxxxxxxxxxxxxxxx"
	drop := "01uitestdropxxxxxxxxxxxxxxxxx"
	s := startStackWith(t, func(home, cwd string) {
		plantSession(t, home, cwd, keep, "Keep Me")
		plantSession(t, home, cwd, drop, "Drop Me")
	})
	pg := s.Page
	waitSel(t, pg, "#sessions", 10*time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rail := textOf(t, pg, "#sessions")
		if strings.Contains(rail, "Keep Me") && strings.Contains(rail, "Drop Me") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	clicked := eval(t, pg, `(() => {
      var rows = document.querySelectorAll('#sessions .sess-row');
      for (var i = 0; i < rows.length; i++) {
        var b = rows[i].querySelector('button.sess');
        if (b && (b.textContent || '').indexOf('Drop Me') >= 0) {
          var x = rows[i].querySelector('.sess-close');
          if (x) { x.click(); return true; }
        }
      }
      return false;
    })()`)
	if clicked != true {
		t.Fatalf("could not click × on Drop Me; rail=%q", textOf(t, pg, "#sessions"))
	}
	waitSel(t, pg, "#modal-ok", 5*time.Second)
	ok, err := pg.QuerySelector("#modal-ok")
	if err != nil || ok == nil {
		t.Fatal("confirm missing")
	}
	if err := ok.Click(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	rail := textOf(t, pg, "#sessions")
	if strings.Contains(rail, "Drop Me") {
		t.Fatalf("Drop Me still in the rail: %q", rail)
	}
	if !strings.Contains(rail, "Keep Me") && !strings.Contains(rail, "Session") {
		t.Fatalf("other session vanished with the delete: %q", rail)
	}
}

func TestUIFocusAndPageshowKeepOneTranscript(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	sendText(t, pg, "once")
	waitSel(t, pg, ".msg.agent", 10*time.Second)
	before := countSel(t, pg, ".msg.you")
	if before != 1 {
		t.Fatalf("expected one ask before resume, got %d", before)
	}
	_, err := pg.Evaluate("() => { window.dispatchEvent(new Event('focus')); window.dispatchEvent(new Event('pageshow')); }")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	after := countSel(t, pg, ".msg.you")
	if after > 1 {
		t.Fatalf("focus/pageshow duplicated the ask: before=%d after=%d", before, after)
	}
	if _, err := pg.QuerySelector("#in"); err != nil {
		t.Fatal("composer gone after resume")
	}
}

func TestUISendEnablesAfterReady(t *testing.T) {
	s := startStack(t)
	pg := s.Page
	waitSel(t, pg, "#send", 10*time.Second)
	el, _ := pg.QuerySelector("#send")
	in, _ := pg.QuerySelector("#in")
	if in == nil || el == nil {
		t.Fatal("composer")
	}
	if err := in.Fill("x"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		dis, _ := el.IsDisabled()
		if !dis {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("send stayed disabled — the page never became ready")
}
