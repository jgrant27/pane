package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentText(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hi", "hi"},
		{map[string]any{"type": "text", "text": "hi"}, "hi"},
		{map[string]any{"content": map[string]any{"text": "hi"}}, "hi"},
		{[]any{map[string]any{"text": "a"}, map[string]any{"text": "b"}}, "ab"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := contentText(c.in); got != c.want {
			t.Fatalf("contentText(%v)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRPCError(t *testing.T) {
	if err := rpcError(nil); err != nil {
		t.Fatal(err)
	}
	if err := rpcError([]byte(`{"sessionId":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if err := rpcError([]byte(`{"error":{"message":"nope"}}`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestParsePromptCaps(t *testing.T) {
	if parsePromptCaps([]byte(`{"agentCapabilities":{"promptCapabilities":{"image":true}}}`)) != true {
		t.Fatal("expected image")
	}
	if parsePromptCaps([]byte(`{}`)) {
		t.Fatal("default is false")
	}
}

func TestBuildPrompt(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(img, []byte("pngdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocks := buildPrompt("look", []promptFile{{Path: img, Name: "pic.png", Mime: "image/png", Size: 7}}, dir, true)
	if len(blocks) != 3 {
		t.Fatalf("got %d %+v", len(blocks), blocks)
	}
	if blocks[0]["type"] != "text" || blocks[1]["type"] != "resource_link" || blocks[2]["type"] != "image" {
		t.Fatalf("%+v", blocks)
	}
	outside := filepath.Join(dir, "..", "nope.txt")
	blocks = buildPrompt("x", []promptFile{{Path: outside, Name: "nope.txt"}}, dir, false)
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("outside leaked %+v", blocks)
	}
}
