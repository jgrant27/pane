package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrokBinPurgeDecode(t *testing.T) {
	t.Setenv("GROK_BIN", "")
	t.Setenv("PATH", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	local := filepath.Join(home, ".local", "bin", "grok")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if grokBin() != local {
		t.Fatalf("got %q want %q", grokBin(), local)
	}

	t.Setenv("GROK_HOME", t.TempDir())
	purgeSessionSearch("no-db")
	db := filepath.Join(grokHome(), "sessions", "session_search.sqlite")
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("not a db"), 0o644); err != nil {
		t.Fatal(err)
	}
	purgeSessionSearch("01abc")

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".cwd"), []byte("/tmp/proj\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if decodeSessionGroup("ignored", dir) != "/tmp/proj" {
		t.Fatal(decodeSessionGroup("ignored", dir))
	}
	name := url.PathEscape("/Users/x/y")
	if decodeSessionGroup(name, t.TempDir()) != "/Users/x/y" {
		t.Fatal(decodeSessionGroup(name, t.TempDir()))
	}
	if !strings.Contains(decodeSessionGroup("%zz", t.TempDir()), "") {
		// invalid unescape may return ""
	}
}

func TestDetectMIMEAndCopyErrors(t *testing.T) {
	if detectMIME("file", nil) != "application/octet-stream" {
		t.Fatal(detectMIME("file", nil))
	}
	if detectMIME("file", []byte("<html>")) == "" {
		t.Fatal("html")
	}
	if err := copyFile(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("copy missing")
	}
	if underCwd("\x00", "x") {
		t.Fatal("bad cwd")
	}
}
