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
	for _, sub := range []string{"desktop", "mobile/ios/GrokPane", "mobile/android/app"} {
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
