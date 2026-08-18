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

type usageInfo struct {
	Used      int      `json:"used"`
	Size      int      `json:"size"`
	Pct       int      `json:"pct"`
	Model     string   `json:"model"`
	Turns     int      `json:"turns"`
	ToolCalls int      `json:"toolCalls"`
	Duration  int      `json:"duration"`
	Tools     []string `json:"tools"`
}

func readSessionUsage(cwd, id string) usageInfo {
	out := usageInfo{Tools: []string{}}
	if !validSessionID(id) {
		return out
	}
	b, err := os.ReadFile(filepath.Join(sessionGroupDir(cwd), id, "signals.json"))
	if err != nil {
		return out
	}
	var raw struct {
		ContextTokensUsed   int      `json:"contextTokensUsed"`
		ContextWindowTokens int      `json:"contextWindowTokens"`
		ContextWindowUsage  int      `json:"contextWindowUsage"`
		TotalTokens         int      `json:"totalTokens"`
		PrimaryModelID      string   `json:"primaryModelId"`
		TurnCount           int      `json:"turnCount"`
		ToolCallCount       int      `json:"toolCallCount"`
		SessionDuration     int      `json:"sessionDurationSeconds"`
		ToolsUsed           []string `json:"toolsUsed"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return out
	}
	out.Used = raw.ContextTokensUsed
	out.Size = raw.ContextWindowTokens
	if out.Used == 0 && raw.TotalTokens > 0 {
		out.Used = raw.TotalTokens
	}
	if out.Size == 0 && raw.ContextWindowUsage > 0 && out.Used > 0 {
		out.Size = out.Used * 100 / raw.ContextWindowUsage
	}
	if out.Size > 0 {
		out.Pct = (out.Used * 100) / out.Size
	} else if raw.ContextWindowUsage > 0 {
		out.Pct = raw.ContextWindowUsage
	}
	out.Model = raw.PrimaryModelID
	out.Turns = raw.TurnCount
	out.ToolCalls = raw.ToolCallCount
	out.Duration = raw.SessionDuration
	if raw.ToolsUsed != nil {
		out.Tools = raw.ToolsUsed
	}
	return out
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
	info := readSessionUsage(cwd, id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}
