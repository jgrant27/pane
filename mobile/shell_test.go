// Package mobile carries no Go code — the phone shells are Swift and Kotlin,
// neither of which can be built here. These tests pin the shell behaviour the
// same way web/layout_test.go pins the frontend: by asserting on the source
// text, so a revert of any of these fixes goes red instead of silently
// shipping in a build nobody on this machine can run. Each assertion names the
// expression that does the work, not a fragment that any rewrite would still
// contain by accident.
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

// want asserts on a whole expression rather than a token, so a rewrite that
// drops the behaviour cannot keep the test green by coincidence.
func want(t *testing.T, src, needle, why string) {
	t.Helper()
	if !strings.Contains(src, needle) {
		t.Errorf("%s\n  missing: %s", why, needle)
	}
}

const (
	mainActivity = "android/app/src/main/java/com/jgrant27/grokpane/MainActivity.kt"
	manifest     = "android/app/src/main/AndroidManifest.xml"
	netConfig    = "android/app/src/main/res/xml/network_security_config.xml"
	stringsXML   = "android/app/src/main/res/values/strings.xml"
	rootView     = "ios/GrokPane/RootView.swift"
	paneWebView  = "ios/GrokPane/PaneWebView.swift"
	iosPlist     = "ios/GrokPane/Info.plist"
	xcodeProj    = "ios/GrokPane.xcodeproj/project.pbxproj"
	versionFile  = "../VERSION"
)

// An implicit ACTION_VIEW throws when nothing resolves it, and an uncaught
// throw out of shouldOverrideUrlLoading kills the app.
func TestAndroidExternalIntentIsGuarded(t *testing.T) {
	src := read(t, mainActivity)
	want(t, src, "catch (e: android.content.ActivityNotFoundException)",
		"startActivity must catch ActivityNotFoundException")
	want(t, src, "getString(R.string.no_handler, dest.toString())",
		"a failed launch must name the URL it could not open")
	want(t, read(t, stringsXML), `<string name="no_handler">`,
		"no_handler string resource is missing")
	// only these schemes may reach an external app; the old code handed over any
	// URL with a foreign host, whatever its scheme.
	want(t, src, `val external = scheme == "mailto" || scheme == "tel" ||`,
		"external-intent allowlist must start at mailto and tel")
	want(t, src, `((scheme == "http" || scheme == "https") &&`,
		"http and https reach another app only when the host is foreign")
}

// Back must walk WebView history before it finishes the activity.
func TestAndroidHandlesBack(t *testing.T) {
	src := read(t, mainActivity)
	want(t, src, "onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {",
		"no back callback registered")
	want(t, src, "if (web.canGoBack()) web.goBack() else finish()",
		"back must navigate history before finishing")
	want(t, src, "import androidx.activity.OnBackPressedCallback",
		"OnBackPressedCallback keeps this off the activity-ktx artifact")
}

// term.js cancels every anchor tap and opens the link with window.open. Android
// with multiple windows off turns that into a navigation in the same WebView, so
// a relative markdown link replaces the pane and cancels the running turn; the
// project folder prompt is the same story with window.prompt.
func TestAndroidAnswersWindowOpenAndJSPanels(t *testing.T) {
	src := read(t, mainActivity)
	if strings.Contains(src, "webChromeClient = WebChromeClient()") {
		t.Error("the stock chrome client neither creates windows nor shows JS panels")
	}
	want(t, src, "settings.setSupportMultipleWindows(true)",
		"without multiple windows, window.open navigates the pane away instead of opening a link")
	want(t, src, "override fun onCreateWindow(",
		"window.open needs onCreateWindow to reach an external app")
	// the popup's URL is delivered to the new window, so something has to stand in
	// for it and report where it was told to go.
	want(t, src, "resultMsg.obj as? WebView.WebViewTransport ?: return false",
		"onCreateWindow must take the transport, and bail rather than crash without one")
	want(t, src, "transport.webView = probe", "the transport needs a WebView to fill in")
	want(t, src, "resultMsg.sendToTarget()", "the transport message must be sent or the popup hangs")
	want(t, src, "probe.post { probe.destroy() }", "the stand-in WebView must not outlive the hand-off")
	want(t, src, `if (scheme == "http" || scheme == "https" || scheme == "mailto") openOutside(dest)`,
		"a popup may only reach the same schemes a tapped link may")

	want(t, src, "override fun onJsAlert(", "window.alert needs a handler")
	want(t, src, "override fun onJsPrompt(", "Open/Change project is a window.prompt")
	want(t, src, "if (text == null) result.cancel() else result.confirm(text)",
		"the prompt must hand the typed text back to JavaScript")
	// a tap outside or a Back press closes an AlertDialog with no button pressed;
	// JavaScript stays blocked until the result is answered.
	if n := strings.Count(src, "setOnDismissListener"); n < 2 {
		t.Errorf("both JS panels must answer on dismiss, found %d dismiss listeners", n)
	}
	want(t, src, "if (answered) return", "the prompt must answer WebKit exactly once")
}

// Cleartext is for the pane on your desk, not for a host on the internet.
func TestAndroidKeepsCleartextLocal(t *testing.T) {
	if m := read(t, manifest); strings.Contains(m, "usesCleartextTraffic") {
		t.Error("the network security config is the one place that decides cleartext")
	}
	want(t, read(t, netConfig), "MainActivity.load()",
		"network_security_config must point at where cleartext is actually enforced")
	src := read(t, mainActivity)
	want(t, src, `if (scheme != "http" && scheme != "https") {`,
		"load() must reject schemes other than http and https")
	want(t, src, `if (scheme == "http" && !plainHost(hostOf(s.substringAfter("://")))) {`,
		"load() must upgrade http to https for non-local hosts")
	want(t, src, `s = "https://" + s.substringAfter("://")`, "the upgrade must actually rewrite the URL")
	want(t, src, "getString(R.string.forced_https)", "a rewritten URL must say so")
	want(t, src, "getString(R.string.bad_scheme)", "a rejected URL must say so")
}

// A bare LAN address is http; pane only gets TLS from a tailscale-serve front end.
func TestBothShellsDefaultLanToHTTP(t *testing.T) {
	kt := read(t, mainActivity)
	if strings.Contains(kt, `s = "https://$s"`) {
		t.Error("Android still forces https on a schemeless entry")
	}
	want(t, kt, `s = (if (plainHost(hostOf(s))) "http" else "https") + "://" + s`,
		"Android must pick the scheme from the host")
	sw := read(t, rootView)
	if strings.Contains(sw, `s = "https://" + s`) {
		t.Error("iOS still forces https on a schemeless entry")
	}
	want(t, sw, `s = (plainHost(authorityHost(s)) ? "http://" : "https://") + s`,
		"iOS must pick the scheme from the host")

	// the two shells have to agree on what "local" means, and a bare octet like
	// "10" occurs all over both files, so pin the whole comparison.
	shared := []string{
		"octets[0] == 127 || octets[0] == 10",
		"(octets[0] == 192 && octets[1] == 168)",
		"(octets[0] == 169 && octets[1] == 254)",
	}
	for _, needle := range shared {
		want(t, kt, needle, "Kotlin plainHost lost a private range")
		want(t, sw, needle, "Swift plainHost lost a private range")
	}
	want(t, kt, "(octets[0] == 172 && octets[1] in 16..31)", "Kotlin plainHost lost 172.16/12")
	want(t, sw, "(octets[0] == 172 && (16...31).contains(octets[1]))", "Swift plainHost lost 172.16/12")
	want(t, kt, "(octets[0] == 100 && octets[1] in 64..127)", "Kotlin plainHost lost the tailnet range")
	want(t, sw, "(octets[0] == 100 && (64...127).contains(octets[1]))", "Swift plainHost lost the tailnet range")
	want(t, kt, `host == "localhost" || host.endsWith(".localhost") || host.endsWith(".local")`,
		"Kotlin plainHost lost the loopback and Bonjour names")
	want(t, sw, `host == "localhost" || host.hasSuffix(".localhost") || host.hasSuffix(".local")`,
		"Swift plainHost lost the loopback and Bonjour names")
	want(t, kt, `host == "::1" || host.startsWith("fe80:")`, "Kotlin plainHost lost IPv6 loopback")
	want(t, sw, `host == "::1" || host.hasPrefix("fe80:")`, "Swift plainHost lost IPv6 loopback")

	want(t, read(t, stringsXML), "192.168.1.5:7420",
		"the Android help text promises LAN support without showing a LAN example")
	want(t, sw, "192.168.1.5:7420",
		"the iOS help text promises LAN support without showing a LAN example")
}

// term.js cancels anchor taps and calls window.open, which WKWebView answers
// with nil unless a WKUIDelegate is wired up; the same delegate backs prompt.
func TestIOSHasUIDelegate(t *testing.T) {
	src := read(t, paneWebView)
	want(t, src, "WKNavigationDelegate, WKUIDelegate", "the coordinator must be the UI delegate")
	want(t, src, "view.uiDelegate = context.coordinator", "the UI delegate must be attached")
	want(t, src, "createWebViewWith configuration", "window.open needs createWebViewWith")
	want(t, src, `scheme == "http" || scheme == "https" || scheme == "mailto"`,
		"a popup may only reach the schemes term.js opens")
	want(t, src, "runJavaScriptTextInputPanelWithPrompt", "Open/Change project is a window.prompt")
	want(t, src, "runJavaScriptAlertPanelWithMessage", "window.alert needs a panel too")
}

// WebKit blocks the page until a JS panel answers, and never unblocks it if the
// answer never comes: a present() onto a controller that is already presenting
// or is out of the window hierarchy is refused silently, so no alert action ever
// runs and the pane freezes.
func TestIOSAnswersEveryJSPanelExactlyOnce(t *testing.T) {
	src := read(t, paneWebView)
	want(t, src, "host.present(sheet, animated: true)", "the panel has to be presented somewhere")
	want(t, src, "if host.presentedViewController !== sheet {",
		"a refused present() must be noticed, not assumed to have worked")
	want(t, src, "private func present(_ sheet: UIAlertController, on host: UIViewController, refused: () -> Void)",
		"both panels need the same refused-present fallback")

	calls := strings.Count(src, "completionHandler(")
	guarded := strings.Count(src, "answered.run { completionHandler(")
	if calls == 0 || guarded != calls {
		t.Errorf("every answer to WebKit must go through the one-shot: %d of %d guarded", guarded, calls)
	}
	// the one-shot is what makes "exactly once" true rather than hoped for.
	want(t, src, "if used { return }", "Once must refuse a second answer")
	// four ways out of the prompt: no presenter, Cancel, OK, refused present.
	if n := strings.Count(src, "answered.run { completionHandler(nil) }"); n != 3 {
		t.Errorf("the prompt has 3 nil answers (no presenter, Cancel, refused present), found %d", n)
	}
	if n := strings.Count(src, "answered.run { completionHandler() }"); n != 3 {
		t.Errorf("the alert has 3 answers (no presenter, OK, refused present), found %d", n)
	}
}

// NSAllowsLocalNetworking covers the named-host case; ATS is not applied to an
// address literal at all, so between them every cleartext pane is reachable and
// a blanket exemption only costs an App Review justification.
func TestIOSDropsArbitraryLoads(t *testing.T) {
	src := read(t, iosPlist)
	for _, blanket := range []string{
		"<key>NSAllowsArbitraryLoads</key>",
		"<key>NSAllowsArbitraryLoadsInWebContent</key>",
	} {
		if strings.Contains(src, blanket) {
			t.Errorf("%s is a blanket exemption the LAN case does not need", blanket)
		}
	}
	want(t, src, "<key>NSAllowsLocalNetworking</key>",
		"unqualified and .local pane names still need NSAllowsLocalNetworking")
	want(t, src, "<key>NSLocalNetworkUsageDescription</key>",
		"iOS refuses the local network without a stated reason")
}

// The Xcode build settings override the Info.plist in a real build, so a stale
// MARKETING_VERSION ships an app that reports a version pane no longer is.
func TestXcodeProjectMatchesTheStampedVersion(t *testing.T) {
	version := strings.TrimSpace(read(t, versionFile))
	plist := read(t, iosPlist)
	if got := plistString(t, plist, "CFBundleShortVersionString"); got != version {
		t.Errorf("Info.plist CFBundleShortVersionString is %s, VERSION is %s", got, version)
	}
	build := plistString(t, plist, "CFBundleVersion")

	proj := read(t, xcodeProj)
	// Debug and Release each carry their own copy, and only fixing one ships the
	// stale number in exactly the configuration that reaches the store.
	if n := strings.Count(proj, "MARKETING_VERSION = "+version+";"); n != 2 {
		t.Errorf("project.pbxproj carries MARKETING_VERSION %s in %d of 2 configurations", version, n)
	}
	if n := strings.Count(proj, "CURRENT_PROJECT_VERSION = "+build+";"); n != 2 {
		t.Errorf("project.pbxproj carries CURRENT_PROJECT_VERSION %s in %d of 2 configurations", build, n)
	}
}

// plistString reads the <string> that follows a <key>, which is all the shape
// these two version keys ever have.
func plistString(t *testing.T, src, key string) string {
	t.Helper()
	at := strings.Index(src, "<key>"+key+"</key>")
	if at < 0 {
		t.Fatalf("Info.plist has no %s", key)
	}
	rest := src[at:]
	open := strings.Index(rest, "<string>")
	shut := strings.Index(rest, "</string>")
	if open < 0 || shut < open {
		t.Fatalf("Info.plist %s has no string value", key)
	}
	return rest[open+len("<string>") : shut]
}
