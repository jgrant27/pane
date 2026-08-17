package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/tree?path="+dir, nil)
	rec := httptest.NewRecorder()
	handleTree(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	var ents []treeEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &ents); err != nil {
		t.Fatal(err)
	}
	if len(ents) != 2 {
		t.Fatalf("got %#v", ents)
	}
	if !ents[0].Dir || ents[0].Name != "sub" {
		t.Fatalf("dirs first: %#v", ents)
	}
}
