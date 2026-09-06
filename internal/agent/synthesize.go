package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/llm/prompts"
)

// SynthCounts is the severity tally handed to the synthesis model.
type SynthCounts struct{ Critical, Warning, Info int }

// synthFindingView is the compact per-finding shape handed to the model.
type synthFindingView struct {
	Key           string `json:"key"`
	Severity      string `json:"severity"`
	Summary       string `json:"summary"`
	ProbableCause string `json:"probableCause"`
	Confidence    string `json:"confidence"`
}

type synthPayload struct {
	Counts     SynthCounts        `json:"counts"`
	Connectors []string           `json:"connectors"`
	Findings   []synthFindingView `json:"findings"`
}

type synthResponse struct {
	Headline string   `json:"headline"`
	Actions  []string `json:"actions"`
}

// Synthesize makes ONE LLM call that turns the full finding set into an
// executive headline plus an ordered list of prioritised actions.
//
// findings is never mutated. On an empty finding set no call is made and
// ("", nil, nil) is returned. A load/Complete error or an unparseable response
// yields ("", nil, [one warning]). The headline is returned verbatim; each
// action is returned verbatim unless it clearly cites a finding key absent from
// the input, in which case it is dropped and one warning recorded per drop.
// keptActions may be nil (all dropped) — that is fine.
func Synthesize(ctx context.Context, l llm.LLM, findings []analyzer.Finding, counts SynthCounts, connectors []string, maxTokens int) (headline string, actions []string, warnings []string) {
	if len(findings) == 0 {
		return "", nil, nil
	}

	views := make([]synthFindingView, 0, len(findings))
	keys := make(map[string]bool, len(findings))
	for i := range findings {
		key := findings[i].RuleID + "@" + findings[i].Object.String()
		keys[key] = true
		v := synthFindingView{
			Key:      key,
			Severity: string(findings[i].Severity),
			Summary:  findings[i].Summary,
		}
		if a := findings[i].Analysis; a != nil {
			v.ProbableCause = a.ProbableCause
			v.Confidence = a.Confidence
		}
		views = append(views, v)
	}

	system, _, err := prompts.Load("synthesize")
	if err != nil {
		return "", nil, []string{fmt.Sprintf("synthesize: %v", err)}
	}

	payload, _ := json.Marshal(synthPayload{Counts: counts, Connectors: connectors, Findings: views})
	resp, err := l.Complete(ctx, llm.Request{
		System:      system,
		Messages:    []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: string(payload)}}}},
		Tools:       nil,
		MaxTokens:   maxTokens,
		Temperature: 0,
	})
	if err != nil {
		return "", nil, []string{fmt.Sprintf("synthesize: %v", err)}
	}

	parsed, perr := parseSynth(firstText(resp.Blocks))
	if perr != nil {
		return "", nil, []string{"synthesize: could not parse response"}
	}

	var kept []string
	for _, action := range parsed.Actions {
		if tok, bogus := unresolvedRef(action, keys); bogus {
			warnings = append(warnings, fmt.Sprintf("synthesize: dropped action citing unknown finding %q", tok))
			continue
		}
		kept = append(kept, action)
	}
	return parsed.Headline, kept, warnings
}

// unresolvedRef scans an action's whitespace-split words for "ref tokens": a
// word containing '@' whose text after the last '@' contains '/'. Trailing
// punctuation (. , : )) is stripped before comparing. It returns
// (firstRefToken, true) when the action has at least one ref token and none of
// them match a known finding key — meaning the action should be dropped.
// Otherwise it returns ("", false) and the action is kept (prose, or a ref that
// resolves).
func unresolvedRef(action string, keys map[string]bool) (string, bool) {
	var first string
	hasRef := false
	for _, w := range strings.Fields(action) {
		at := strings.LastIndex(w, "@")
		if at < 0 || !strings.Contains(w[at+1:], "/") {
			continue
		}
		tok := strings.TrimRight(w, ".,:)")
		if !hasRef {
			first = tok
			hasRef = true
		}
		if keys[tok] {
			return "", false // a cited ref resolves — keep the action
		}
	}
	if hasRef {
		return first, true
	}
	return "", false
}

// parseSynth reads the model's text as the synthesis response JSON, tolerating
// ``` fences or surrounding prose by extracting the outermost {...} span.
func parseSynth(text string) (*synthResponse, error) {
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
	var r synthResponse
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return nil, err
	}
	return &r, nil
}
