package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoverage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cover.out")
	body := "mode: count\n" +
		"github.com/jgrant27/pane/a.go:1.1,2.2 4 1\n" +
		"github.com/jgrant27/pane/b.go:1.1,2.2 6 0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	pct, hit, stmts, err := coverage(path)
	if err != nil {
		t.Fatal(err)
	}
	if stmts != 10 || hit != 4 {
		t.Fatalf("hit %d stmts %d", hit, stmts)
	}
	if pct < 39.9 || pct > 40.1 {
		t.Fatalf("pct %v", pct)
	}
	empty := filepath.Join(dir, "empty.out")
	if err := os.WriteFile(empty, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pct, _, _, err = coverage(empty)
	if err != nil || pct != 100 {
		t.Fatalf("empty %v %v", pct, err)
	}
	if _, _, _, err := coverage(filepath.Join(dir, "nope")); err == nil {
		t.Fatal("missing")
	}
	bad := filepath.Join(dir, "bad.out")
	_ = os.WriteFile(bad, []byte("hello\n"), 0o644)
	if _, _, _, err := coverage(bad); err == nil {
		t.Fatal("bad header")
	}
	_ = os.WriteFile(bad, []byte("mode: set\nnot-enough\n"), 0o644)
	if _, _, _, err := coverage(bad); err == nil {
		t.Fatal("bad line")
	}
	_ = os.WriteFile(bad, []byte("mode: set\nfile.go:1.1,2.2 x 1\n"), 0o644)
	if _, _, _, err := coverage(bad); err == nil {
		t.Fatal("bad stmts")
	}
	_ = os.WriteFile(bad, []byte("mode: set\nfile.go:1.1,2.2 2 y\n"), 0o644)
	if _, _, _, err := coverage(bad); err == nil {
		t.Fatal("bad count")
	}
	if err := check(90, path); err == nil {
		t.Fatal("40% should fail 90")
	}
	if err := check(30, path); err != nil {
		t.Fatal(err)
	}
	if err := check(90, filepath.Join(dir, "nope")); err == nil {
		t.Fatal("check missing")
	}
}
