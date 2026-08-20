// Package mobile carries no Go code — the phone shells are Swift and Kotlin,
// neither of which can be built here. These tests pin the shell behaviour the
// same way web/layout_test.go pins the frontend: by asserting on the source
// text, so a revert of any of these fixes goes red instead of silently
// shipping in a build nobody on this machine can run.
package mobile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	mainActivity = "android/app/src/main/java/com/jgrant27/grokpane/MainActivity.kt"
	manifest     = "android/app/src/main/AndroidManifest.xml"
	netConfig    = "android/app/src/main/res/xml/network_security_config.xml"
	stringsXML   = "android/app/src/main/res/values/strings.xml"
	rootView     = "ios/GrokPane/RootView.swift"
	paneWebView  = "ios/GrokPane/PaneWebView.swift"
	iosPlist     = "ios/GrokPane/Info.plist"
)

// An implicit ACTION_VIEW throws when nothing resolves it, and an uncaught
// throw out of shouldOverrideUrlLoading kills the app.
func TestAndroidExternalIntentIsGuarded(t *testing.T) {
	src := read(t, mainActivity)
	if !strings.Contains(src, "catch (e: android.content.ActivityNotFoundException)") {
		t.Error("startActivity must catch ActivityNotFoundException")
	}
	if !strings.Contains(src, "R.string.no_handler") {
		t.Error("a failed launch must tell the user, not fail silently")
	}
	if !strings.Contains(read(t, stringsXML), `name="no_handler"`) {
		t.Error("no_handler string resource is missing")
	}
	// only these schemes may reach an external app; the old code handed over any
	// URL with a foreign host, whatever its scheme.
	for _, want := range []string{`scheme == "mailto"`, `scheme == "tel"`, `scheme == "http"`, `scheme == "https"`} {
		if !strings.Contains(src, want) {
			t.Errorf("external-intent allowlist is missing %s", want)
		}
	}
}

// Back must walk WebView history before it finishes the activity.
func TestAndroidHandlesBack(t *testing.T) {
	src := read(t, mainActivity)
	if !strings.Contains(src, "onBackPressedDispatcher.addCallback") {
		t.Error("no back callback registered")
	}
	if !strings.Contains(src, "if (web.canGoBack()) web.goBack() else finish()") {
		t.Error("back must navigate history before finishing")
	}
	if !strings.Contains(src, "import androidx.activity.OnBackPressedCallback") {
		t.Error("OnBackPressedCallback keeps this off the activity-ktx artifact")
	}
}

// Cleartext is for the pane on your desk, not for a host on the internet.
func TestAndroidKeepsCleartextLocal(t *testing.T) {
	if m := read(t, manifest); strings.Contains(m, "usesCleartextTraffic") {
		t.Error("the network security config is the one place that decides cleartext")
	}
	if !strings.Contains(read(t, netConfig), "MainActivity.load()") {
		t.Error("network_security_config must point at where cleartext is actually enforced")
	}
	src := read(t, mainActivity)
	if !strings.Contains(src, `if (scheme != "http" && scheme != "https")`) {
		t.Error("load() must reject schemes other than http and https")
	}
	if !strings.Contains(src, `if (scheme == "http" && !plainHost(`) {
		t.Error("load() must upgrade http to https for non-local hosts")
	}
	if !strings.Contains(src, "R.string.forced_https") || !strings.Contains(src, "R.string.bad_scheme") {
		t.Error("a rewritten or rejected URL must say so")
	}
}

// A bare LAN address is http; pane only gets TLS from a tailscale-serve front end.
func TestBothShellsDefaultLanToHTTP(t *testing.T) {
	kt := read(t, mainActivity)
	if strings.Contains(kt, `s = "https://$s"`) {
		t.Error("Android still forces https on a schemeless entry")
	}
	if !strings.Contains(kt, `s = (if (plainHost(hostOf(s))) "http" else "https") + "://" + s`) {
		t.Error("Android must pick the scheme from the host")
	}
	sw := read(t, rootView)
	if strings.Contains(sw, `s = "https://" + s`) {
		t.Error("iOS still forces https on a schemeless entry")
	}
	if !strings.Contains(sw, `plainHost(authorityHost(s)) ? "http://" : "https://"`) {
		t.Error("iOS must pick the scheme from the host")
	}
	// the two shells have to agree on what "local" means, so pin the ranges in both.
	for _, want := range []string{"127", "10", "172", "192", "168", "169", "254", "100", "localhost", ".local"} {
		if !strings.Contains(kt, want) {
			t.Errorf("Kotlin plainHost is missing %s", want)
		}
		if !strings.Contains(sw, want) {
			t.Errorf("Swift plainHost is missing %s", want)
		}
	}
	if !strings.Contains(read(t, stringsXML), "192.168.1.5:7420") || !strings.Contains(sw, "192.168.1.5:7420") {
		t.Error("the help text still promises LAN support without showing a LAN example")
	}
}

// term.js cancels anchor taps and calls window.open, which WKWebView answers
// with nil unless a WKUIDelegate is wired up; the same delegate backs prompt.
func TestIOSHasUIDelegate(t *testing.T) {
	src := read(t, paneWebView)
	for _, want := range []string{
		"WKUIDelegate",
		"view.uiDelegate = context.coordinator",
		"createWebViewWith configuration",
		"runJavaScriptTextInputPanelWithPrompt",
		"runJavaScriptAlertPanelWithMessage",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("PaneWebView is missing %s", want)
		}
	}
	// every path out of a JS panel must answer WebKit exactly once.
	if strings.Count(src, "completionHandler(nil)") < 1 || strings.Count(src, "completionHandler()") < 1 {
		t.Error("the no-presenter path must still call the completion handler")
	}
}

// NSAllowsLocalNetworking already covers every cleartext pane; the blanket key
// is ignored beside it and only costs an App Review justification.
func TestIOSDropsArbitraryLoads(t *testing.T) {
	src := read(t, iosPlist)
	if strings.Contains(src, "<key>NSAllowsArbitraryLoads</key>") {
		t.Error("NSAllowsArbitraryLoads is dead config beside NSAllowsLocalNetworking")
	}
	if !strings.Contains(src, "<key>NSAllowsLocalNetworking</key>") {
		t.Error("the LAN case still needs NSAllowsLocalNetworking")
	}
}
