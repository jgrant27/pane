package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseAndBump(t *testing.T) {
	v, ok := parseVer("v1.2.3")
	if !ok || v.String() != "1.2.3" {
		t.Fatalf("%v %v", v, ok)
	}
	if v.bump("patch").Tag() != "v1.2.4" {
		t.Fatal(v.bump("patch").Tag())
	}
	if v.bump("minor").Tag() != "v1.3.0" {
		t.Fatal(v.bump("minor").Tag())
	}
	if v.bump("major").Tag() != "v2.0.0" {
		t.Fatal(v.bump("major").Tag())
	}
	old, ok := parseVer("0.1.1")
	if !ok || !old.less(v) {
		t.Fatal("0.1.1 should be less than 1.2.3")
	}
	if maxVer([]ver{{0, 1, 1}, {0, 2, 0}, {0, 1, 9}}).String() != "0.2.0" {
		t.Fatal(maxVer([]ver{{0, 1, 1}, {0, 2, 0}, {0, 1, 9}}))
	}
}

// stampTree lays out the files writeVersion stamps, all at 0.2.0.
func stampTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"desktop", "mobile/ios/GrokPane", "mobile/ios/GrokPane.xcodeproj", "mobile/android/app"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(sub)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"desktop/wails.json": `{"info":{"productVersion":"0.2.0"}}`,
		"desktop/Info.plist": `<key>CFBundleShortVersionString</key>
  <string>0.2.0</string>
  <key>CFBundleVersion</key>
  <string>0.2.0</string>`,
		"proxy.go": `"name":    "grok-pane",
		"title":   "Grok Pane",
		"version": "0.2.0",`,
		"mobile/ios/GrokPane/Info.plist": `	<key>CFBundleShortVersionString</key>
	<string>0.2.0</string>
	<key>CFBundleVersion</key>
	<string>2000</string>`,
		"mobile/android/app/build.gradle.kts": `        versionCode = 2000
        versionName = "0.2.0"`,
		"mobile/ios/GrokPane.xcodeproj/project.pbxproj": `				MARKETING_VERSION = 0.2.0;
				CURRENT_PROJECT_VERSION = 2000;`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestWriteVersion(t *testing.T) {
	dir := stampTree(t)
	if err := writeVersion(dir, "0.2.1"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "VERSION"))
	if err != nil || string(got) != "0.2.1\n" {
		t.Fatalf("VERSION %q %v", got, err)
	}
	wails, _ := os.ReadFile(filepath.Join(dir, "desktop", "wails.json"))
	if string(wails) != `{"info":{"productVersion":"0.2.1"}}` {
		t.Fatalf("wails %s", wails)
	}
	plist, _ := os.ReadFile(filepath.Join(dir, "desktop", "Info.plist"))
	if !strings.Contains(string(plist), "0.2.1") || strings.Contains(string(plist), "0.2.0") {
		t.Fatalf("plist %s", plist)
	}
	proxy, _ := os.ReadFile(filepath.Join(dir, "proxy.go"))
	if !strings.Contains(string(proxy), `"version": "0.2.1"`) {
		t.Fatalf("proxy %s", proxy)
	}
}

func TestWriteVersionStampsMobile(t *testing.T) {
	dir := stampTree(t)
	if err := writeVersion(dir, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	ios, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash("mobile/ios/GrokPane/Info.plist")))
	if !strings.Contains(string(ios), "<string>0.3.0</string>") {
		t.Fatalf("ios short version not stamped: %s", ios)
	}
	if !strings.Contains(string(ios), "<string>3000</string>") {
		t.Fatalf("ios build number not stamped: %s", ios)
	}
	if strings.Contains(string(ios), "0.2.0") || strings.Contains(string(ios), "2000") {
		t.Fatalf("ios still at the old version: %s", ios)
	}
	gradle, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash("mobile/android/app/build.gradle.kts")))
	if !strings.Contains(string(gradle), `versionName = "0.3.0"`) {
		t.Fatalf("gradle versionName not stamped: %s", gradle)
	}
	if !strings.Contains(string(gradle), "versionCode = 3000") {
		t.Fatalf("gradle versionCode not stamped: %s", gradle)
	}
}

// A file that is present but has drifted out of the shape cmd/bump expects is
// worse than a missing one: nothing is stamped, nothing is said, and the
// release ships the old version.
func TestWriteVersionReportsUnmatchedStamp(t *testing.T) {
	dir := stampTree(t)
	gradle := filepath.Join(dir, filepath.FromSlash("mobile/android/app/build.gradle.kts"))
	if err := os.WriteFile(gradle, []byte("android { defaultConfig { } }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeVersion(dir, "0.2.1"); err == nil {
		t.Fatal("a gradle file with no version stamp should fail the write")
	}
	if err := replaceAll(gradle, reGradleName, "0.2.1", 2); err == nil {
		t.Fatal("a regex that matched nothing reported success")
	}
}

func TestWriteVersionReportsMissingMobile(t *testing.T) {
	dir := stampTree(t)
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash("mobile/android/app/build.gradle.kts"))); err != nil {
		t.Fatal(err)
	}
	if err := writeVersion(dir, "0.2.1"); err == nil {
		t.Fatal("missing gradle file should fail the stamp")
	}
	if err := writeVersion(dir, "nope"); err == nil {
		t.Fatal("unparseable version should fail before anything is written")
	}
}

func TestBuildCodeClimbs(t *testing.T) {
	steps := []string{"0.2.6", "0.2.9", "0.3.0", "0.10.0", "1.0.0"}
	prev := -1
	for _, s := range steps {
		v, _ := parseVer(s)
		n, err := strconv.Atoi(buildCode(v))
		if err != nil {
			t.Fatalf("%s -> %q: %v", s, buildCode(v), err)
		}
		if n <= prev {
			t.Fatalf("%s -> %d did not climb past %d", s, n, prev)
		}
		prev = n
	}
	if v, _ := parseVer("0.2.6"); buildCode(v) != "2006" {
		t.Fatalf("0.2.6 -> %s", buildCode(v))
	}
}

// the checked-in shells must carry what cmd/bump would stamp for VERSION, or a
// release ships phone builds reporting a version the repo left behind.
func TestRepoMobileFilesMatchVERSION(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "VERSION"))
	if err != nil {
		t.Skip(err)
	}
	v, ok := parseVer(string(b))
	if !ok {
		t.Fatalf("VERSION %q", b)
	}
	ios, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("mobile/ios/GrokPane/Info.plist")))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileVer(filepath.Join(root, filepath.FromSlash("mobile/ios/GrokPane/Info.plist")), rePlistShort); len(got) != 1 || got[0] != v {
		t.Fatalf("ios CFBundleShortVersionString %v, want %v", got, v)
	}
	if !strings.Contains(string(ios), "<string>"+buildCode(v)+"</string>") {
		t.Fatalf("ios CFBundleVersion is not %s: %s", buildCode(v), ios)
	}
	gradle, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("mobile/android/app/build.gradle.kts")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(gradle), `versionName = "`+v.String()+`"`) {
		t.Fatalf("gradle versionName is not %s: %s", v, gradle)
	}
	if !strings.Contains(string(gradle), "versionCode = "+buildCode(v)) {
		t.Fatalf("gradle versionCode is not %s: %s", buildCode(v), gradle)
	}
	// Xcode build settings win over the Info.plist they are built with, so a
	// stale MARKETING_VERSION here means the app reports the old number
	// however well the plist was stamped.
	pbx, err := os.ReadFile(filepath.Join(root, filepath.FromSlash("mobile/ios/GrokPane.xcodeproj/project.pbxproj")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(pbx), "MARKETING_VERSION = "+v.String()+";") {
		t.Fatalf("xcodeproj MARKETING_VERSION is not %s", v)
	}
	if !strings.Contains(string(pbx), "CURRENT_PROJECT_VERSION = "+buildCode(v)+";") {
		t.Fatalf("xcodeproj CURRENT_PROJECT_VERSION is not %s", buildCode(v))
	}
}

// The gate a developer runs and the gate CI runs have to be one gate. They
// had drifted: make test grew -race and a coverage floor while CI still
// called go test by hand, and both of them filtered the desktop package out,
// so desktop/app_test.go ran in neither place.
func TestReleaseGateIsOneGate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skip(err)
	}
	mk, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(mk), "\n") {
		if !strings.HasPrefix(line, "\t") || !strings.Contains(line, "go test") {
			continue
		}
		if !strings.Contains(line, "-race") {
			t.Errorf("make runs a test without the race detector: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(string(mk), "$(DESKTOP_TEST)") {
		t.Error("make test does not run the desktop package's tests")
	}
	// The release gate has to see the stamped tree. Running it before the
	// bump is how a version-consistency test can pass locally and then fail
	// on the tag build, which is exactly what happened to v0.2.7.
	deploy := string(mk)[strings.Index(string(mk), "\ndeploy:"):]
	if strings.HasPrefix(deploy, "\ndeploy: test") {
		t.Error("deploy runs the gate as a prerequisite, before the version is stamped")
	}
	bump := strings.Index(deploy, "go run ./cmd/bump")
	gate := strings.Index(deploy, "$(MAKE) test")
	tag := strings.Index(deploy, "git tag ")
	switch {
	case bump < 0 || gate < 0 || tag < 0:
		t.Errorf("deploy is missing a step: bump=%d gate=%d tag=%d", bump, gate, tag)
	case !(bump < gate && gate < tag):
		t.Error("deploy must stamp, then run the gate, then tag — in that order")
	}
	if strings.Count(deploy, "$(STAMPED)") < 2 {
		t.Error("the gate's rollback and the release commit must use the same file list")
	}
	ci, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(".github/workflows/build.yml")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ci), "run: make test") {
		t.Error("CI does not run make test, so it is not running the developer's gate")
	}
	if strings.Contains(string(ci), "grep -v '/desktop$'") {
		t.Error("CI still drops the desktop package's tests on the floor")
	}
}

func TestDoBumpAndFileVer(t *testing.T) {
	if _, err := doBump("nope", false); err == nil {
		t.Fatal("expected unknown bump")
	}
	tag, err := doBump("patch", false)
	if err != nil || !strings.HasPrefix(tag, "v") {
		t.Fatal(tag, err)
	}
	root, err := repoRoot()
	if err != nil || root == "" {
		t.Fatal(root, err)
	}
	if current(root).String() == "" {
		t.Fatal("current")
	}
	if err := replaceAll(filepath.Join(t.TempDir(), "missing.json"), reWails, "1.0.0", 2); err == nil {
		t.Fatal("missing file")
	}
	_ = gitTags()
}
