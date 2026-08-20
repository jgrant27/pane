package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("../etc/passwd"); got != "passwd" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeName(""); got != "upload" {
		t.Fatalf("got %q", got)
	}
	if got := sanitizeName(".."); got != "upload" {
		t.Fatalf("got %q", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	a := uniquePath(dir, "photo.png")
	if filepath.Base(a) != "photo.png" {
		t.Fatalf("first %s", a)
	}
	if err := os.WriteFile(a, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := uniquePath(dir, "photo.png")
	if filepath.Base(b) != "photo-2.png" {
		t.Fatalf("second %s", b)
	}
}

func TestUnderCwd(t *testing.T) {
	dir := t.TempDir()
	if !underCwd(dir, filepath.Join(dir, "a.png")) {
		t.Fatal("child should be under cwd")
	}
	if underCwd(dir, filepath.Join(dir, "..", "outside")) {
		t.Fatal("parent leaked")
	}
}

func TestHandleUpload(t *testing.T) {
	dir := t.TempDir()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hi there")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	newTestProxy().handleUpload(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	var info uploadInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Name != "hello.txt" || !strings.HasPrefix(info.Path, dir) {
		t.Fatalf("%+v", info)
	}
	got, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi there" {
		t.Fatalf("wrote %q", got)
	}

	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "outside.png")
	if err := os.WriteFile(src, []byte("pngdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err = attachPath(dir, src)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "outside.png" || !strings.HasPrefix(info.Path, dir) {
		t.Fatalf("copy %+v", info)
	}
	got, err = os.ReadFile(info.Path)
	if err != nil || string(got) != "pngdata" {
		t.Fatalf("copied %q %v", got, err)
	}
	again, err := attachPath(dir, info.Path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != info.Path {
		t.Fatalf("in-cwd reattach %s vs %s", again.Path, info.Path)
	}

	// One proxy for attach-then-delete: only a copy pane made is a copy
	// pane may remove.
	px := newTestProxy()
	reqJSON := httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, strings.NewReader(`{"path":"`+src+`"}`))
	reqJSON.Header.Set("Content-Type", "application/json")
	recJSON := httptest.NewRecorder()
	px.handleUpload(recJSON, reqJSON)
	if recJSON.Code != 200 {
		t.Fatalf("json attach %d %s", recJSON.Code, recJSON.Body.String())
	}
	var made uploadInfo
	if err := json.Unmarshal(recJSON.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if !made.Copied {
		t.Fatalf("expected a copy: %+v", made)
	}

	bad := httptest.NewRequest(http.MethodPost, "/v1/upload?cwd="+dir, bytes.NewReader(nil))
	rec2 := httptest.NewRecorder()
	newTestProxy().handleUpload(rec2, bad)
	if rec2.Code == 200 {
		t.Fatal("empty upload accepted")
	}

	// A file pane did not put there stays put, whatever the caller claims.
	theirs := filepath.Join(dir, "not-an-upload.txt")
	if err := os.WriteFile(theirs, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	recKeep := httptest.NewRecorder()
	px.handleUpload(recKeep, httptest.NewRequest(http.MethodDelete, "/v1/upload?cwd="+dir+"&path="+theirs, nil))
	if recKeep.Code != http.StatusBadRequest {
		t.Fatalf("unregistered delete %d", recKeep.Code)
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Fatal("pane deleted a file it did not create")
	}

	del := httptest.NewRequest(http.MethodDelete, "/v1/upload?cwd="+dir+"&path="+made.Path, nil)
	recDel := httptest.NewRecorder()
	px.handleUpload(recDel, del)
	if recDel.Code != http.StatusNoContent {
		t.Fatalf("delete %d %s", recDel.Code, recDel.Body.String())
	}
	if _, err := os.Stat(made.Path); !os.IsNotExist(err) {
		t.Fatal("copied file still there")
	}

	if detectMIME("x.bin", []byte{0x00, 0x01}) == "" {
		t.Fatal("detectMIME")
	}
	if detectMIME("note.txt", nil) == "" {
		t.Fatal("empty head")
	}
	if _, err := attachPath(dir, ""); err == nil {
		t.Fatal("empty path")
	}
	if _, err := attachPath(dir, dir); err == nil {
		t.Fatal("dir attach")
	}
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodGet, "/v1/upload?cwd="+dir, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodPost, "/v1/upload", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
	rec = httptest.NewRecorder()
	newTestProxy().handleUpload(rec, httptest.NewRequest(http.MethodDelete, "/v1/upload", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal(rec.Code)
	}
}
