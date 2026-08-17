package main

import "testing"

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
