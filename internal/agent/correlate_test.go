package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
)

func analysed(ruleID, ns, name, cause string) analyzer.Finding {
	return analyzer.Finding{
		RuleID:   ruleID,
		Severity: analyzer.SeverityWarning,
		Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: ns, Name: name},
		Summary:  ruleID + " summary",
		Analysis: &analyzer.Analysis{ProbableCause: cause, Confidence: "high"},
	}
}

func TestCorrelateLinksResolvedRefsDropsGhosts(t *testing.T) {
	findings := []analyzer.Finding{
		analysed("a/1", "p", "x", "disk full"),
		analysed("b/2", "p", "x", "disk full"),
	}
	l := &fakeLLM{responses: []llm.Response{{
		StopReason: "end_turn",
		Blocks:     []llm.Block{{Type: "text", Text: `{"correlations":[{"key":"a/1@Pod/p/x","related":["b/2@Pod/p/x","ghost/9@Pod/none"]}]}`}},
	}}}

	out, warnings := Correlate(context.Background(), l, findings, 1024)

	require.Equal(t, 1, l.calls)
	require.Equal(t, []string{"b/2@Pod/p/x"}, out[0].Analysis.CorrelatedFindings)
	require.Empty(t, out[1].Analysis.CorrelatedFindings)

	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "unresolved reference")

	// Input must not be mutated.
	require.Empty(t, findings[0].Analysis.CorrelatedFindings)
}

func TestCorrelateLLMErrorReturnsUnchanged(t *testing.T) {
	findings := []analyzer.Finding{
		analysed("a/1", "p", "x", "disk full"),
		analysed("b/2", "p", "x", "disk full"),
	}
	l := &fakeLLM{errs: []error{errors.New("boom")}}

	out, warnings := Correlate(context.Background(), l, findings, 1024)

	require.Len(t, warnings, 1)
	require.Equal(t, "correlate: boom", warnings[0])
	for i := range findings {
		require.Equal(t, findings[i].Analysis, out[i].Analysis)
		require.Empty(t, out[i].Analysis.CorrelatedFindings)
	}
}

func TestCorrelateFewerThanTwoAnalysed(t *testing.T) {
	withAnalysis := analysed("a/1", "p", "x", "disk full")
	noAnalysis := analyzer.Finding{
		RuleID:   "b/2",
		Severity: analyzer.SeverityWarning,
		Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "y"},
		Summary:  "no analysis",
	}
	findings := []analyzer.Finding{withAnalysis, noAnalysis}
	l := &fakeLLM{}

	out, warnings := Correlate(context.Background(), l, findings, 1024)

	require.Equal(t, 0, l.calls)
	require.Empty(t, warnings)
	require.Equal(t, findings[0].Analysis, out[0].Analysis)
	require.Nil(t, out[1].Analysis)
}
