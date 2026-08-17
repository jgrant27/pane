package main

import "testing"

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
