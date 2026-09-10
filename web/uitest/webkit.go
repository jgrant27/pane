// Package uitest drives Grok Pane's real page in WebKit.
//
// WebKit only. Never Chromium, never Chrome, never Firefox.
package uitest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mxschmitt/playwright-go"
)

var (
	wkOnce sync.Once
	wkPW   *playwright.Playwright
	wkBr   playwright.Browser
	wkErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if wkBr != nil {
		_ = wkBr.Close()
	}
	if wkPW != nil {
		_ = wkPW.Stop()
	}
	os.Exit(code)
}

func webKitBrowser(t *testing.T) playwright.Browser {
	t.Helper()
	wkOnce.Do(func() {
		wkPW, wkErr = playwright.Run()
		if wkErr != nil {
			return
		}
		wkBr, wkErr = wkPW.WebKit.Launch(playwright.BrowserTypeLaunchOptions{
			Headless: playwright.Bool(true),
			Timeout:  playwright.Float(60000),
		})
	})
	if wkErr != nil {
		t.Fatalf("launch webkit: %v (install WebKit with: make test-ui)", wkErr)
	}
	if wkBr.BrowserType().Name() != "webkit" {
		t.Fatalf("browser is %q; UI tests must use WebKit", wkBr.BrowserType().Name())
	}
	return wkBr
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func buildPane(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "pane")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build pane: %v\n%s", err, out)
	}
	return bin
}

func mustWebKit(t *testing.T, token string) (playwright.Page, func()) {
	t.Helper()
	br := webKitBrowser(t)
	var last error
	for i := 0; i < 3; i++ {
		ctx, err := br.NewContext(playwright.BrowserNewContextOptions{
			ExtraHttpHeaders: map[string]string{"X-Pane-Token": token},
		})
		if err != nil {
			last = err
			time.Sleep(time.Second)
			continue
		}
		pg, err := ctx.NewPage()
		if err != nil {
			_ = ctx.Close()
			last = err
			time.Sleep(time.Second)
			continue
		}
		return pg, func() {
			_ = pg.Close()
			_ = ctx.Close()
		}
	}
	t.Fatalf("webkit NewPage: %v", last)
	return nil, func() {}
}

func waitSel(t *testing.T, pg playwright.Page, sel string, timeout time.Duration) {
	t.Helper()
	if _, err := pg.WaitForSelector(sel, playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(float64(timeout.Milliseconds())),
	}); err != nil {
		t.Fatalf("wait %s: %v", sel, err)
	}
}

func textOf(t *testing.T, pg playwright.Page, sel string) string {
	t.Helper()
	el, err := pg.QuerySelector(sel)
	if err != nil || el == nil {
		t.Fatalf("missing %s: %v", sel, err)
	}
	s, err := el.InnerText()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func eval(t *testing.T, pg playwright.Page, js string) any {
	t.Helper()
	v, err := pg.Evaluate(js)
	if err != nil {
		t.Fatalf("eval %s: %v", js, err)
	}
	return v
}

func waitReady(t *testing.T, pg playwright.Page) {
	t.Helper()
	waitSel(t, pg, "#in", 10*time.Second)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		v, err := pg.Evaluate(`(() => {
      var s = document.getElementById('status');
      return s ? String(s.textContent || '') : '';
    })()`)
		if err == nil {
			st, _ := v.(string)
			if st == "ready" || st == "" || strings.Contains(st, "ready") {
				time.Sleep(200 * time.Millisecond)
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	st, _ := pg.Evaluate(`document.getElementById('status') && document.getElementById('status').textContent`)
	t.Fatalf("page never became ready; status=%v", st)
}

func sendText(t *testing.T, pg playwright.Page, text string) {
	t.Helper()
	waitReady(t, pg)
	in, err := pg.QuerySelector("#in")
	if err != nil || in == nil {
		t.Fatal("composer missing")
	}
	if err := in.Fill(""); err != nil {
		t.Fatal(err)
	}
	if err := in.Type(text, playwright.ElementHandleTypeOptions{Delay: playwright.Float(10)}); err != nil {
		t.Fatal(err)
	}
	if err := in.Press("Enter"); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.WaitForSelector(".msg.you", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(15000),
	}); err != nil {
		logHTML, _ := pg.Evaluate(`document.getElementById('log') && document.getElementById('log').innerHTML`)
		st, _ := pg.Evaluate(`document.getElementById('status') && document.getElementById('status').textContent`)
		t.Fatalf("wait .msg.you: %v status=%v log=%v", err, st, logHTML)
	}
}

func assertHealthy(t *testing.T, pg playwright.Page) {
	t.Helper()
	waitSel(t, pg, "#in", 15*time.Second)
	v := eval(t, pg, `(() => {
      var shell = document.getElementById('shell');
      var inn = document.getElementById('in');
      var log = document.getElementById('log');
      var vvh = document.documentElement.style.getPropertyValue('--vvh');
      return {
        body: document.body.innerText || '',
        shellH: shell ? shell.clientHeight : 0,
        inH: inn ? inn.getBoundingClientRect().height : 0,
        logH: log ? log.clientHeight : 0,
        vvh: vvh,
        status: (document.getElementById('status') && document.getElementById('status').textContent) || ''
      };
    })()`)
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("healthy %T %v", v, v)
	}
	body, _ := m["body"].(string)
	if strings.Contains(body, "unknown session") || strings.Contains(body, "Invalid params") {
		t.Fatalf("agent error on screen: %q", body)
	}
	num := func(k string) float64 {
		switch n := m[k].(type) {
		case float64:
			return n
		case int:
			return float64(n)
		case int64:
			return float64(n)
		default:
			return 0
		}
	}
	if num("shellH") < 200 || num("inH") < 10 || num("logH") < 20 {
		t.Fatalf("layout collapsed (blank page): %+v", m)
	}
	if vvh, _ := m["vvh"].(string); strings.TrimSpace(vvh) == "0px" {
		t.Fatal("--vvh is 0px; that blanks WKWebView")
	}
}

func countSel(t *testing.T, pg playwright.Page, sel string) int {
	t.Helper()
	v := eval(t, pg, fmt.Sprintf("document.querySelectorAll(%q).length", sel))
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		t.Fatalf("count %s: %T %v", sel, v, v)
		return 0
	}
}
