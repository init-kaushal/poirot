package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// fakeLLM replays a scripted list of responses; each Complete pops the next.
type fakeLLM struct {
	responses []llm.Response
	errs      []error
	calls     int
	reqs      []llm.Request // every request seen, in order
}

func (f *fakeLLM) Model() string { return "fake" }
func (f *fakeLLM) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	f.reqs = append(f.reqs, req)
	i := f.calls
	f.calls++
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.responses) {
		return f.responses[i], err
	}
	return llm.Response{StopReason: "end_turn"}, err
}

func warn(f analyzer.Finding) analyzer.Finding { f.Severity = analyzer.SeverityWarning; return f }

func TestInvestigateEnrichesFromToolThenJSON(t *testing.T) {
	findings := []analyzer.Finding{warn(analyzer.Finding{
		RuleID: "reliability/crashloop", Domain: "reliability",
		Object:  analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "api-1"},
		Summary: "crashlooping",
	})}
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.Block{{Type: "tool_use", ToolName: "k8s.logs", ToolID: "t1", Input: json.RawMessage(`{"namespace":"p","pod":"api-1"}`)}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"reliability/crashloop","probableCause":"bad config env var","confidence":"high","remediation":"fix FOO"}]}`}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, findings)

	out, meta := Investigate(context.Background(), l, tp, findings, InvestigateConfig{
		MaxGroups: 10, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096,
	})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "bad config env var", out[0].Analysis.ProbableCause)
	require.Equal(t, "high", out[0].Analysis.Confidence)
	require.Equal(t, 1, meta.ToolCalls)
	require.Nil(t, findings[0].Analysis, "input slice must not be mutated")
}

func TestInvestigateBudgetForcesFinalAnswer(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	toolResp := llm.Response{StopReason: "tool_use", Blocks: []llm.Block{{Type: "tool_use", ToolName: "snapshot.events", ToolID: "t", Input: json.RawMessage(`{"kind":"Pod","name":"p"}`)}}}
	l := &fakeLLM{responses: []llm.Response{toolResp, toolResp, // 2 tool calls hits MaxToolCalls:2
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"x/y","probableCause":"c","confidence":"low","remediation":"r"}]}`}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})
	out, _ := Investigate(context.Background(), l, tp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 2, MaxTokens: 40000}, MaxTokensPerCall: 4096})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "low", out[0].Analysis.Confidence)
}

func TestInvestigateBadJSONTwiceStubs(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: "not json"}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: "still not json"}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})
	out, meta := Investigate(context.Background(), l, tp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "low", out[0].Analysis.Confidence)
	require.NotEmpty(t, meta.Warnings)
}

func TestInvestigateHardIterationCeiling(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	// Every Complete returns a tool_use with zero Usage, so Budget.Exceeded()
	// never trips (tokens stay 0, MaxToolCalls is 0). The hard ceiling must break
	// the loop. Provide plenty of scripted turns; the forced final Complete after
	// the ceiling fires falls through to the fakeLLM's default end_turn{}.
	toolResp := llm.Response{StopReason: "tool_use", Usage: llm.Usage{},
		Blocks: []llm.Block{{Type: "tool_use", ToolName: "snapshot.events", ToolID: "t", Input: json.RawMessage(`{"kind":"Pod","name":"p"}`)}}}
	resps := make([]llm.Response, 25)
	for i := range resps {
		resps[i] = toolResp
	}
	l := &fakeLLM{responses: resps}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})

	out, meta := Investigate(context.Background(), l, tp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 0, MaxTokens: 0}, MaxTokensPerCall: 4096})

	require.NotNil(t, out[0].Analysis, "group still gets an Analysis after the ceiling fires")
	require.Contains(t, meta.Warnings, "investigate Pod/p: hit hard iteration ceiling (20)")
}

// countingTP wraps a ToolProvider and counts Invoke calls.
type countingTP struct {
	inner ToolProvider
	calls int
}

func (c *countingTP) Tools() []llm.ToolSpec { return c.inner.Tools() }
func (c *countingTP) Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	c.calls++
	return c.inner.Invoke(ctx, name, args)
}

// C1: maxToolCallsPerGroup must be enforced within a single turn — a turn with
// N parallel tool_use blocks may not run more than the remaining budget.
func TestInvestigateEnforcesToolBudgetWithinTurn(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	blk := func(id string) llm.Block {
		return llm.Block{Type: "tool_use", ToolName: "snapshot.events", ToolID: id, Input: json.RawMessage(`{"kind":"Pod","name":"p"}`)}
	}
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.Block{blk("t1"), blk("t2"), blk("t3"), blk("t4"), blk("t5")}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"x/y","probableCause":"c","confidence":"high","remediation":"r"}]}`}}},
	}}
	ctp := &countingTP{inner: NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})}

	out, meta := Investigate(context.Background(), l, ctp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 2, MaxTokens: 40000}, MaxTokensPerCall: 4096})

	require.LessOrEqual(t, ctp.calls, 2, "tool invoked at most MaxToolCalls times, not once per parallel block")
	require.Equal(t, 2, meta.ToolCalls)
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "high", out[0].Analysis.Confidence, "loop still terminates with a real Analysis")

	var isErrResults int
	for _, r := range l.reqs {
		for _, m := range r.Messages {
			for _, b := range m.Blocks {
				if b.Type == "tool_result" && b.IsError && b.Content == "tool call budget exhausted for this object" {
					isErrResults++
				}
			}
		}
	}
	require.GreaterOrEqual(t, isErrResults, 3, "over-budget tool_use blocks get IsError tool_result answers")
}

// I2: an already-done context short-circuits every remaining group with a
// "not investigated — budget" stub, one aggregated warning, and zero LLM calls.
func TestInvestigateShortCircuitsOnDoneContext(t *testing.T) {
	mk := func(id string) analyzer.Finding {
		return warn(analyzer.Finding{RuleID: "r/" + id, Object: analyzer.ObjectRef{Kind: "Pod", Name: id}})
	}
	findings := []analyzer.Finding{mk("p1"), mk("p2"), mk("p3")}
	l := &fakeLLM{}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, findings)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, meta := Investigate(ctx, l, tp, findings,
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096})

	for _, f := range out {
		require.NotNil(t, f.Analysis)
		require.Equal(t, "not investigated — budget", f.Analysis.ProbableCause)
		require.Equal(t, "none", f.Analysis.Confidence)
	}
	require.Equal(t, 0, l.calls, "no l.Complete after the context is done")
	require.Len(t, meta.Warnings, 1, "exactly one aggregated warning")
	require.Contains(t, meta.Warnings[0], "global budget exhausted")
	require.Contains(t, meta.Warnings[0], "3 group(s) not investigated")
}

func TestInvestigateSkipsInfoAndCapsGroups(t *testing.T) {
	info := analyzer.Finding{RuleID: "slo/skipped", Severity: analyzer.SeverityInfo,
		Object: analyzer.ObjectRef{Kind: "X", Name: "y"}}
	g1 := warn(analyzer.Finding{RuleID: "a/1", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p1"}})
	g2 := warn(analyzer.Finding{RuleID: "a/2", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p2"}})
	l := &fakeLLM{} // every Complete → empty end_turn → parse fails → stub
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{info, g1, g2})
	out, _ := Investigate(context.Background(), l, tp, []analyzer.Finding{info, g1, g2},
		InvestigateConfig{MaxGroups: 1, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096})

	byID := map[string]*analyzer.Analysis{}
	for _, f := range out {
		byID[f.RuleID] = f.Analysis
	}
	require.Nil(t, byID["slo/skipped"], "info findings are not investigated")
	require.NotNil(t, byID["a/1"])
	require.NotNil(t, byID["a/2"])
	require.Equal(t, "not investigated — budget", byID["a/2"].ProbableCause) // beyond MaxGroups:1
}
