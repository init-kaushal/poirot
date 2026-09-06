package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/llm/prompts"
)

// correlateView is the compact per-finding shape handed to the correlate model.
type correlateView struct {
	Key           string `json:"key"`
	Severity      string `json:"severity"`
	Summary       string `json:"summary"`
	ProbableCause string `json:"probableCause"`
}

type correlateResponse struct {
	Correlations []struct {
		Key     string   `json:"key"`
		Related []string `json:"related"`
	} `json:"correlations"`
}

// refKey is the "<ruleId>@<object>" identity used to cross-reference findings.
func refKey(f analyzer.Finding) string {
	return f.RuleID + "@" + f.Object.String()
}

// Correlate makes ONE LLM call that links findings sharing a single root cause.
//
// It returns a deep copy of findings — the input slice and its *Analysis
// pointers are never mutated — plus any non-fatal warnings. Findings without an
// *Analysis are passed through untouched (nothing to correlate). If fewer than
// two findings are analysed, or the call/parse fails, the copy is returned
// unchanged. Token/meta accounting belongs to the caller, not here.
func Correlate(ctx context.Context, l llm.LLM, findings []analyzer.Finding, maxTokens int) ([]analyzer.Finding, []string) {
	// Deep copy: fresh *Analysis (and CorrelatedFindings slice) per finding.
	out := make([]analyzer.Finding, len(findings))
	for i, f := range findings {
		c := f
		if f.Analysis != nil {
			a := *f.Analysis
			if f.Analysis.CorrelatedFindings != nil {
				a.CorrelatedFindings = append([]string(nil), f.Analysis.CorrelatedFindings...)
			}
			c.Analysis = &a
		}
		out[i] = c
	}

	// Index analysed findings by ref key and build the model input.
	byKey := make(map[string]int, len(out))
	views := make([]correlateView, 0, len(out))
	for i := range out {
		if out[i].Analysis == nil {
			continue
		}
		key := refKey(out[i])
		byKey[key] = i
		views = append(views, correlateView{
			Key:           key,
			Severity:      string(out[i].Severity),
			Summary:       out[i].Summary,
			ProbableCause: out[i].Analysis.ProbableCause,
		})
	}
	if len(views) < 2 {
		return out, nil
	}

	system, _, err := prompts.Load("correlate")
	if err != nil {
		return out, []string{fmt.Sprintf("correlate: %v", err)}
	}

	payload, _ := json.Marshal(views)
	resp, err := l.Complete(ctx, llm.Request{
		System:      system,
		Messages:    []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: string(payload)}}}},
		Tools:       nil,
		MaxTokens:   maxTokens,
		Temperature: 0,
	})
	if err != nil {
		return out, []string{fmt.Sprintf("correlate: %v", err)}
	}

	parsed, perr := parseCorrelations(firstText(resp.Blocks))
	if perr != nil {
		return out, []string{"correlate: could not parse response"}
	}

	dropped := 0
	for _, c := range parsed.Correlations {
		idx, ok := byKey[c.Key]
		if !ok {
			dropped++ // unknown correlation key
			continue
		}
		seen := map[string]bool{}
		var related []string
		for _, r := range c.Related {
			if r == c.Key {
				continue // never self-reference
			}
			if _, ok := byKey[r]; !ok {
				dropped++
				continue
			}
			if seen[r] {
				continue
			}
			seen[r] = true
			related = append(related, r)
		}
		sort.Strings(related)
		out[idx].Analysis.CorrelatedFindings = related
	}

	var warnings []string
	if dropped > 0 {
		warnings = append(warnings, fmt.Sprintf("correlate: dropped %d unresolved reference(s)", dropped))
	}
	return out, warnings
}

// parseCorrelations reads the model's text as the correlate response JSON,
// tolerating ``` fences or surrounding prose by extracting the outermost {...}.
func parseCorrelations(text string) (*correlateResponse, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty response")
	}
	if !strings.HasPrefix(text, "{") {
		i := strings.Index(text, "{")
		j := strings.LastIndex(text, "}")
		if i < 0 || j <= i {
			return nil, fmt.Errorf("no JSON object in response")
		}
		text = text[i : j+1]
	}
	var r correlateResponse
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, err
	}
	return &r, nil
}
