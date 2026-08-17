package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type modelInfo struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Context int          `json:"context"`
	Effort  string       `json:"effort"`
	Efforts []effortInfo `json:"efforts"`
}

type effortInfo struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type sessionModels struct {
	Current string      `json:"current"`
	Effort  string      `json:"effort"`
	Context int         `json:"context"`
	Models  []modelInfo `json:"models"`
}

func parseSessionModels(raw json.RawMessage) sessionModels {
	out := sessionModels{}
	if len(raw) == 0 {
		return out
	}
	var wrap struct {
		Models struct {
			CurrentModelID  string `json:"currentModelId"`
			AvailableModels []struct {
				ModelID string `json:"modelId"`
				Name    string `json:"name"`
				Meta    struct {
					ReasoningEffort         string `json:"reasoningEffort"`
					SupportsReasoningEffort bool   `json:"supportsReasoningEffort"`
					TotalContextTokens      int    `json:"totalContextTokens"`
					ReasoningEfforts        []struct {
						ID    string `json:"id"`
						Value string `json:"value"`
						Label string `json:"label"`
					} `json:"reasoningEfforts"`
				} `json:"_meta"`
			} `json:"availableModels"`
		} `json:"models"`
		Meta struct {
			Config struct {
				Options []struct {
					Category string `json:"category"`
					ID       string `json:"id"`
					Label    string `json:"label"`
					Selected bool   `json:"selected"`
				} `json:"options"`
			} `json:"x.ai/sessionConfig"`
			Detail struct {
				CurrentModelID string `json:"currentModelId"`
			} `json:"x.ai/sessionDetail"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return out
	}
	out.Current = wrap.Models.CurrentModelID
	if out.Current == "" {
		out.Current = wrap.Meta.Detail.CurrentModelID
	}
	for _, m := range wrap.Models.AvailableModels {
		if m.ModelID == "" {
			continue
		}
		info := modelInfo{
			ID:      m.ModelID,
			Name:    firstNonEmpty(m.Name, m.ModelID),
			Context: m.Meta.TotalContextTokens,
			Effort:  m.Meta.ReasoningEffort,
		}
		for _, e := range m.Meta.ReasoningEfforts {
			id := firstNonEmpty(e.ID, e.Value)
			if id == "" {
				continue
			}
			info.Efforts = append(info.Efforts, effortInfo{ID: id, Label: firstNonEmpty(e.Label, id)})
		}
		out.Models = append(out.Models, info)
		if m.ModelID == out.Current {
			out.Context = info.Context
			if out.Effort == "" {
				out.Effort = info.Effort
			}
		}
	}
	for _, o := range wrap.Meta.Config.Options {
		if !o.Selected {
			continue
		}
		switch o.Category {
		case "model":
			if out.Current == "" {
				out.Current = o.ID
			}
		case "mode":
			out.Effort = o.ID
		}
	}
	return out
}

func readSessionUsage(cwd, id string) (used, size int) {
	if !validSessionID(id) {
		return 0, 0
	}
	b, err := os.ReadFile(filepath.Join(sessionGroupDir(cwd), id, "signals.json"))
	if err != nil {
		return 0, 0
	}
	var raw struct {
		ContextTokensUsed   int `json:"contextTokensUsed"`
		ContextWindowTokens int `json:"contextWindowTokens"`
		ContextWindowUsage  int `json:"contextWindowUsage"`
		TotalTokens         int `json:"totalTokens"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return 0, 0
	}
	used = raw.ContextTokensUsed
	size = raw.ContextWindowTokens
	if used == 0 && raw.TotalTokens > 0 {
		used = raw.TotalTokens
	}
	if size == 0 && raw.ContextWindowUsage > 0 && used > 0 {
		size = used * 100 / raw.ContextWindowUsage
	}
	return used, size
}

func handleUsage(w http.ResponseWriter, r *http.Request) {
	cwd := strings.TrimSpace(r.URL.Query().Get("cwd"))
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if cwd == "" || id == "" {
		http.Error(w, "cwd and id required", http.StatusBadRequest)
		return
	}
	abs, err := filepath.Abs(cwd)
	if err == nil {
		cwd = abs
	}
	used, size := readSessionUsage(cwd, id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int{"used": used, "size": size})
}
