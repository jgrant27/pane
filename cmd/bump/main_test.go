package main

import (
	"os"
	"path/filepath"
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

func TestWriteVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "desktop"), 0o755); err != nil {
		t.Fatal(err)
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
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
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
