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
	"testing"
	"time"

	"github.com/playwright-community/playwright-go"
)

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
	pw, err := playwright.Run()
	if err != nil {
		t.Fatalf("playwright: %v (install WebKit with: make test-ui)", err)
	}
	br, err := pw.WebKit.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		_ = pw.Stop()
		t.Fatalf("launch webkit: %v", err)
	}
	if br.BrowserType().Name() != "webkit" {
		_ = br.Close()
		_ = pw.Stop()
		t.Fatalf("browser is %q; UI tests must use WebKit", br.BrowserType().Name())
	}
	ctx, err := br.NewContext(playwright.BrowserNewContextOptions{
		ExtraHttpHeaders: map[string]string{"X-Pane-Token": token},
	})
	if err != nil {
		_ = br.Close()
		_ = pw.Stop()
		t.Fatal(err)
	}
	pg, err := ctx.NewPage()
	if err != nil {
		_ = ctx.Close()
		_ = br.Close()
		_ = pw.Stop()
		t.Fatal(err)
	}
	stop := func() {
		_ = pg.Close()
		_ = ctx.Close()
		_ = br.Close()
		_ = pw.Stop()
	}
	return pg, stop
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
