package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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

type productUsageInfo struct {
	Product string `json:"product"`
	Pct     int    `json:"pct"`
}

type usageInfo struct {
	Used              int                 `json:"used"`
	Size              int                 `json:"size"`
	Pct               int                 `json:"pct"`
	Left              int                 `json:"left"`
	CompactAt         int                 `json:"compactAt"`
	Model             string              `json:"model"`
	Turns             int                 `json:"turns"`
	ToolCalls         int                 `json:"toolCalls"`
	Duration          int                 `json:"duration"`
	Tools             []string            `json:"tools"`
	LimitKind         string              `json:"limitKind"`
	LimitWeekly       bool                `json:"limitWeekly"`
	LimitPct          int                 `json:"limitPct"`
	LimitProducts     []productUsageInfo  `json:"limitProducts"`
	LimitMonthly      int                 `json:"limitMonthly"`
	LimitUsed         int                 `json:"limitUsed"`
	LimitOnDemand     int                 `json:"limitOnDemand"`
	LimitOnDemandUsed int                 `json:"limitOnDemandUsed"`
	LimitPrepaid      int                 `json:"limitPrepaid"`
	LimitReset        string              `json:"limitReset"`
	LimitPeriod       string              `json:"limitPeriod"`
	LimitNote         string              `json:"limitNote"`
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
		if out.Used < out.Size {
			out.Left = out.Size - out.Used
		}
		out.CompactAt = out.Size * 80 / 100
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
	applyBilling(&info, readGrokBilling())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(info)
}

var (
	billingMu    sync.Mutex
	billingCache []byte
	billingAt    time.Time
)

var (
	readGrokBilling = fetchGrokBilling
	billingURL      = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
)

func fetchGrokBilling() []byte {
	billingMu.Lock()
	defer billingMu.Unlock()
	if billingCache != nil && time.Since(billingAt) < time.Minute {
		return billingCache
	}
	key := grokAuthKey()
	if key == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, billingURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 4 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil || len(body) == 0 {
		return nil
	}
	billingCache = body
	billingAt = time.Now()
	return body
}

func grokAuthKey() string {
	b, err := os.ReadFile(filepath.Join(grokHome(), "auth.json"))
	if err != nil {
		return ""
	}
	var wrap map[string]map[string]any
	if json.Unmarshal(b, &wrap) != nil {
		return ""
	}
	for _, v := range wrap {
		if s, ok := v["key"].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

type moneyVal struct {
	Val float64 `json:"val"`
}

func moneyInt(v moneyVal) int {
	if v.Val < 0 {
		return 0
	}
	return int(v.Val + 0.5)
}

func applyBilling(u *usageInfo, raw []byte) {
	if u == nil || len(raw) == 0 {
		return
	}
	var wrap struct {
		Config struct {
			MonthlyLimit       moneyVal `json:"monthlyLimit"`
			Used               moneyVal `json:"used"`
			OnDemandCap        moneyVal `json:"onDemandCap"`
			OnDemandUsed       moneyVal `json:"onDemandUsed"`
			PrepaidBalance     moneyVal `json:"prepaidBalance"`
			CreditUsagePercent float64  `json:"creditUsagePercent"`
			IsUnified          bool     `json:"isUnifiedBillingUser"`
			PeriodStart        string   `json:"billingPeriodStart"`
			PeriodEnd          string   `json:"billingPeriodEnd"`
			CurrentPeriod      struct {
				Type  string `json:"type"`
				Start string `json:"start"`
				End   string `json:"end"`
			} `json:"currentPeriod"`
			ProductUsage []struct {
				Product string  `json:"product"`
				Pct     float64 `json:"usagePercent"`
			} `json:"productUsage"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &wrap) != nil {
		return
	}
	cfg := wrap.Config
	weekly := cfg.IsUnified || cfg.CreditUsagePercent > 0 || strings.Contains(cfg.CurrentPeriod.Type, "WEEKLY")
	if weekly {
		u.LimitKind = "credits"
		u.LimitWeekly = true
		u.LimitPct = int(cfg.CreditUsagePercent + 0.5)
		u.LimitPrepaid = moneyInt(cfg.PrepaidBalance)
		u.LimitOnDemand = moneyInt(cfg.OnDemandCap)
		u.LimitOnDemandUsed = moneyInt(cfg.OnDemandUsed)
		u.LimitPeriod = firstNonEmpty(cfg.CurrentPeriod.Start, cfg.PeriodStart)
		u.LimitReset = firstNonEmpty(cfg.CurrentPeriod.End, cfg.PeriodEnd)
		for _, p := range cfg.ProductUsage {
			name := strings.TrimSpace(p.Product)
			if name == "" {
				continue
			}
			u.LimitProducts = append(u.LimitProducts, productUsageInfo{
				Product: name,
				Pct:     int(p.Pct + 0.5),
			})
		}
		if u.LimitPct >= 100 {
			u.LimitNote = "weekly included credits used up"
		}
		return
	}
	u.LimitKind = "usd"
	u.LimitMonthly = moneyInt(cfg.MonthlyLimit)
	u.LimitUsed = moneyInt(cfg.Used)
	u.LimitOnDemand = moneyInt(cfg.OnDemandCap)
	u.LimitPeriod = cfg.PeriodStart
	u.LimitReset = cfg.PeriodEnd
	if u.LimitMonthly > 0 && u.LimitUsed >= u.LimitMonthly {
		u.LimitNote = "over monthly included limit"
	}
}
