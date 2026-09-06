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

// InvestigateConfig tunes one Investigate pass.
type InvestigateConfig struct {
	MaxGroups        int    // how many object groups to actually investigate
	Budget           Budget // per-group template; counters are reset for each group
	MaxTokensPerCall int    // MaxTokens passed to every llm.Complete
}

// InvestigateMeta reports aggregate usage and any non-fatal warnings.
type InvestigateMeta struct {
	InputTokens  int
	OutputTokens int
	ToolCalls    int
	Warnings     []string
}

// answerEntry is one element of the model's final JSON answer.
type answerEntry struct {
	RuleID        string `json:"ruleId"`
	ProbableCause string `json:"probableCause"`
	Confidence    string `json:"confidence"`
	Remediation   string `json:"remediation"`
}

type investigateAnswer struct {
	Findings []answerEntry `json:"findings"`
}

// promptFindingView is the compact per-finding shape handed to the model. Raw
// logs are deliberately excluded: the model pulls them via snapshot.* tools.
type promptFindingView struct {
	RuleID   string              `json:"ruleId"`
	Severity string              `json:"severity"`
	Summary  string              `json:"summary"`
	Evidence []analyzer.Evidence `json:"evidence,omitempty"`
}

// Investigate enriches every warning-or-worse finding with an *Analysis using a
// bounded, batch-by-object tool-calling loop against l.
//
// The input slice and its *Analysis pointers are never mutated: the returned
// slice is a deep copy, and Analysis is only ever set on the copies.
func Investigate(ctx context.Context, l llm.LLM, tp ToolProvider, findings []analyzer.Finding, cfg InvestigateConfig) ([]analyzer.Finding, InvestigateMeta) {
	var meta InvestigateMeta

	// Deep copy so the caller's findings (and their Analysis pointers) are safe.
	out := make([]analyzer.Finding, len(findings))
	for i, f := range findings {
		c := f
		if f.Analysis != nil {
			a := *f.Analysis
			c.Analysis = &a
		}
		out[i] = c
	}

	// Group the investigable findings by object string, deterministically.
	type group struct {
		key     string
		maxRank int
		idxs    []int
	}
	warnRank := analyzer.SeverityWarning.Rank()
	byKey := map[string]*group{}
	var order []*group
	for i := range out {
		if out[i].Severity.Rank() < warnRank {
			continue // info findings pass through untouched
		}
		key := out[i].Object.String()
		g := byKey[key]
		if g == nil {
			g = &group{key: key}
			byKey[key] = g
			order = append(order, g)
		}
		g.idxs = append(g.idxs, i)
		if r := out[i].Severity.Rank(); r > g.maxRank {
			g.maxRank = r
		}
	}
	sort.Slice(order, func(a, b int) bool {
		if order[a].maxRank != order[b].maxRank {
			return order[a].maxRank > order[b].maxRank // severity desc
		}
		return order[a].key < order[b].key // object string asc
	})

	for gi, g := range order {
		if gi >= cfg.MaxGroups {
			for _, idx := range g.idxs {
				out[idx].Analysis = &analyzer.Analysis{
					ProbableCause: "not investigated — budget",
					Confidence:    "none",
					Remediation:   "",
				}
			}
			continue
		}
		investigateGroup(ctx, l, tp, cfg, out, g.idxs, &meta)
	}

	return out, meta
}

// investigateGroup runs one bounded loop for a single object's findings and
// writes an *Analysis onto each out[idx].
func investigateGroup(ctx context.Context, l llm.LLM, tp ToolProvider, cfg InvestigateConfig, out []analyzer.Finding, idxs []int, meta *InvestigateMeta) {
	obj := out[idxs[0]].Object.String()

	system, _, err := prompts.Load("investigate_system")
	if err != nil {
		meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: load prompt: %v", obj, err))
		stubGroup(out, idxs, "agent response could not be parsed", "low")
		return
	}

	// Fresh budget per group: value copy of the template resets the counters.
	budget := cfg.Budget
	b := &budget

	views := make([]promptFindingView, 0, len(idxs))
	for _, idx := range idxs {
		f := out[idx]
		views = append(views, promptFindingView{
			RuleID:   f.RuleID,
			Severity: string(f.Severity),
			Summary:  f.Summary,
			Evidence: f.Evidence,
		})
	}
	payload, _ := json.Marshal(struct {
		Findings []promptFindingView `json:"findings"`
	}{views})

	messages := []llm.Message{{
		Role: llm.RoleUser,
		Blocks: []llm.Block{{Type: "text", Text: "Investigate the following findings for object " + obj +
			". Object state, events and pod logs are available through the snapshot.* and connector tools — do not expect raw logs in this message. " +
			"When done, reply with ONLY the JSON answer object.\n\n" + string(payload)}},
	}}

	// completeNoTools appends a user nudge and runs one tool-less completion,
	// always folding its usage into the budget and meta.
	completeNoTools := func(nudge string) (llm.Response, error) {
		messages = append(messages, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: nudge}}})
		resp, cerr := l.Complete(ctx, llm.Request{System: system, Messages: messages, Tools: nil, MaxTokens: cfg.MaxTokensPerCall, Temperature: 0})
		b.Add(resp.Usage)
		meta.InputTokens += resp.Usage.InputTokens
		meta.OutputTokens += resp.Usage.OutputTokens
		return resp, cerr
	}

	var answer *investigateAnswer
	var parseErr error

loop:
	for {
		if exceeded, _ := b.Exceeded(); exceeded {
			resp, cerr := completeNoTools("Budget reached. Reply now with the JSON object, best effort.")
			if cerr != nil {
				meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: forced final answer: %v", obj, cerr))
				stubGroup(out, idxs, "agent response could not be parsed", "low")
				return
			}
			answer, parseErr = parseAnswer(firstText(resp.Blocks))
			break loop
		}

		resp, cerr := l.Complete(ctx, llm.Request{System: system, Messages: messages, Tools: tp.Tools(), MaxTokens: cfg.MaxTokensPerCall, Temperature: 0})
		if cerr != nil {
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: %v", obj, cerr))
			stubGroup(out, idxs, "agent could not complete analysis", "low")
			return
		}
		b.Add(resp.Usage)
		meta.InputTokens += resp.Usage.InputTokens
		meta.OutputTokens += resp.Usage.OutputTokens

		if resp.StopReason == "tool_use" {
			messages = append(messages, llm.Message{Role: llm.RoleAssistant, Blocks: resp.Blocks})
			var toolResults []llm.Block
			for _, blk := range resp.Blocks {
				if blk.Type != "tool_use" {
					continue
				}
				res, isErr, ierr := tp.Invoke(ctx, blk.ToolName, blk.Input)
				if ierr != nil {
					meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: tool %s: %v", obj, blk.ToolName, ierr))
					stubGroup(out, idxs, "agent could not complete analysis", "low")
					return
				}
				toolResults = append(toolResults, llm.Block{
					Type:    "tool_result",
					ToolID:  blk.ToolID,
					Content: string(res),
					IsError: isErr,
				})
				b.ToolCall()
				meta.ToolCalls++
			}
			messages = append(messages, llm.Message{Role: llm.RoleUser, Blocks: toolResults})
			continue
		}

		// end_turn (or max_tokens): the answer should be in the text block.
		answer, parseErr = parseAnswer(firstText(resp.Blocks))
		break loop
	}

	// One parse retry with a stricter nudge and no tools.
	if parseErr != nil {
		resp, cerr := completeNoTools("Return ONLY the JSON object, nothing else.")
		if cerr != nil {
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: parse retry: %v", obj, cerr))
			stubGroup(out, idxs, "agent response could not be parsed", "low")
			return
		}
		answer, parseErr = parseAnswer(firstText(resp.Blocks))
		if parseErr != nil {
			meta.Warnings = append(meta.Warnings, fmt.Sprintf("investigate %s: agent response could not be parsed", obj))
			stubGroup(out, idxs, "agent response could not be parsed", "low")
			return
		}
	}

	applyAnswer(out, idxs, answer)
}

// applyAnswer maps answer entries onto the group's findings by RuleID.
func applyAnswer(out []analyzer.Finding, idxs []int, a *investigateAnswer) {
	for _, idx := range idxs {
		f := &out[idx]
		var matched *answerEntry
		for i := range a.Findings {
			if a.Findings[i].RuleID == f.RuleID {
				matched = &a.Findings[i]
				break
			}
		}
		if matched == nil {
			f.Analysis = &analyzer.Analysis{ProbableCause: "no analysis returned", Confidence: "low"}
			continue
		}
		f.Analysis = &analyzer.Analysis{
			ProbableCause: matched.ProbableCause,
			Confidence:    coerceConfidence(matched.Confidence),
			Remediation:   matched.Remediation,
		}
	}
}

func coerceConfidence(c string) string {
	switch c {
	case "high", "medium", "low":
		return c
	default:
		return "low"
	}
}

// stubGroup writes the same low-value Analysis onto every finding in the group.
func stubGroup(out []analyzer.Finding, idxs []int, cause, confidence string) {
	for _, idx := range idxs {
		out[idx].Analysis = &analyzer.Analysis{ProbableCause: cause, Confidence: confidence}
	}
}

// firstText returns the text of the first text block, or "".
func firstText(blocks []llm.Block) string {
	for _, b := range blocks {
		if b.Type == "text" {
			return b.Text
		}
	}
	return ""
}

// parseAnswer parses the model's final text as the answer JSON. It tolerates
// surrounding prose or ``` fences by extracting the outermost {...} span.
func parseAnswer(text string) (*investigateAnswer, error) {
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
	var a investigateAnswer
	if err := json.Unmarshal([]byte(text), &a); err != nil {
		return nil, err
	}
	return &a, nil
}
