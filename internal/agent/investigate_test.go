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
}

func (f *fakeLLM) Model() string { return "fake" }
func (f *fakeLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
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
