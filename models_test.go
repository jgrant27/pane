package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSessionModels(t *testing.T) {
	raw := []byte(`{
		"sessionId":"abc",
		"models":{
			"currentModelId":"grok-4.6",
			"availableModels":[
				{
					"modelId":"grok-4.6",
					"name":"Grok 4.6",
					"_meta":{
						"reasoningEffort":"xhigh",
						"totalContextTokens":500000,
						"reasoningEfforts":[{"id":"xhigh","label":"Extra High Effort"},{"id":"low","label":"Low Effort"}]
					}
				}
			]
		},
		"_meta":{"x.ai/sessionConfig":{"options":[{"category":"mode","id":"xhigh","selected":true}]}}
	}`)
	st := parseSessionModels(raw)
	if st.Current != "grok-4.6" || st.Effort != "xhigh" || st.Context != 500000 || len(st.Models) != 1 {
		t.Fatalf("%+v", st)
	}
	if st.Models[0].Efforts[0].ID != "xhigh" {
		t.Fatalf("%+v", st.Models[0].Efforts)
	}
}

func TestReadSessionUsage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	cwd := "/Users/jgrant/stuff/demo"
	dir := filepath.Join(sessionGroupDir(cwd), "01use")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
		"contextTokensUsed":18912,
		"contextWindowTokens":500000,
		"contextWindowUsage":3,
		"primaryModelId":"grok-4.6",
		"turnCount":2,
		"toolCallCount":4,
		"sessionDurationSeconds":50,
		"toolsUsed":["read_file","run_terminal_command"]
	}`)
	if err := os.WriteFile(filepath.Join(dir, "signals.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	u := readSessionUsage(cwd, "01use")
	if u.Used != 18912 || u.Size != 500000 || u.Model != "grok-4.6" || u.Turns != 2 {
		t.Fatalf("%+v", u)
	}
	if len(u.Tools) != 2 {
		t.Fatalf("tools %+v", u.Tools)
	}
}
