package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if u.Left != 500000-18912 || u.CompactAt != 400000 {
		t.Fatalf("window leftover %+v", u)
	}
}

func TestApplyBilling(t *testing.T) {
	t.Setenv("GROK_HOME", t.TempDir())
	u := usageInfo{}
	applyBilling(&u, []byte(`{"config":{"monthlyLimit":{"val":700},"used":{"val":780},"onDemandCap":{"val":0},"billingPeriodEnd":"2026-09-01T00:00:00+00:00"}}`))
	if u.LimitKind != "usd" || u.LimitMonthly != 700 || u.LimitUsed != 780 || u.LimitNote == "" {
		t.Fatalf("%+v", u)
	}
	applyBilling(&u, []byte(`not-json`))
	if u.LimitMonthly != 700 {
		t.Fatal("bad json must not wipe")
	}
	if grokAuthKey() != "" {
		t.Fatal("test GROK_HOME must not expose a live key")
	}
}

func TestApplyBillingCredits(t *testing.T) {
	u := usageInfo{}
	applyBilling(&u, []byte(`{
		"config":{
			"creditUsagePercent":40,
			"isUnifiedBillingUser":true,
			"prepaidBalance":{"val":0},
			"onDemandCap":{"val":0},
			"onDemandUsed":{"val":0},
			"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-08-16T13:47:04Z","end":"2026-08-23T13:47:04Z"},
			"productUsage":[
				{"product":"GrokBuild","usagePercent":33},
				{"product":"GrokChat","usagePercent":4},
				{"product":"GrokImagine","usagePercent":3}
			]
		}
	}`))
	if u.LimitKind != "credits" || !u.LimitWeekly || u.LimitPct != 40 || u.LimitMonthly != 0 {
		t.Fatalf("credits %+v", u)
	}
	if u.LimitPrepaid != 0 || u.LimitNote != "" || len(u.LimitProducts) != 3 || u.LimitProducts[0].Pct != 33 {
		t.Fatalf("products %+v", u)
	}
	if u.LimitReset != "2026-08-23T13:47:04Z" {
		t.Fatalf("reset %s", u.LimitReset)
	}
	// Dollar fields on the legacy endpoint must not win for SuperGrok Plus.
	u = usageInfo{}
	applyBilling(&u, []byte(`{"config":{"monthlyLimit":{"val":700},"used":{"val":780},"isUnifiedBillingUser":true,"creditUsagePercent":40,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","end":"2026-08-23T00:00:00Z"}}}`))
	if u.LimitKind != "credits" || u.LimitMonthly != 0 || u.LimitUsed != 0 || u.LimitPct != 40 {
		t.Fatalf("unified must ignore leftover dollars %+v", u)
	}
}

func TestFetchGrokBilling(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GROK_HOME", root)
	billingMu.Lock()
	billingCache = nil
	billingAt = time.Time{}
	billingMu.Unlock()
	if fetchGrokBilling() != nil {
		t.Fatal("no key")
	}
	if grokAuthKey() != "" {
		t.Fatal("empty auth")
	}
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if grokAuthKey() != "" {
		t.Fatal("bad json")
	}
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte(`{"https://x":{"key":"k"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if grokAuthKey() != "k" {
		t.Fatal(grokAuthKey())
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"config":{"monthlyLimit":{"val":1},"used":{"val":0}}}`))
	}))
	t.Cleanup(srv.Close)
	oldURL := billingURL
	billingURL = srv.URL
	t.Cleanup(func() { billingURL = oldURL })
	billingMu.Lock()
	billingCache = nil
	billingAt = time.Time{}
	billingMu.Unlock()
	got := fetchGrokBilling()
	if string(got) == "" || !strings.Contains(string(got), "monthlyLimit") {
		t.Fatalf("%s", got)
	}
	if string(fetchGrokBilling()) != string(got) {
		t.Fatal("cache")
	}
}
