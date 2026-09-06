package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
)

func synthTextResp(s string) llm.Response {
	return llm.Response{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: s}}}
}

func TestSynthesizeKeepsHeadlineDropsBogusAction(t *testing.T) {
	findings := []analyzer.Finding{{
		RuleID:   "reliability/crashloop",
		Domain:   "reliability",
		Severity: analyzer.SeverityCritical,
		Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "api-1"},
		Summary:  "api-1 crashlooping",
		Analysis: &analyzer.Analysis{ProbableCause: "OOMKilled", Confidence: "high"},
	}}
	l := &fakeLLM{responses: []llm.Response{synthTextResp(
		`{"headline":"payments is down","actions":["Fix reliability/crashloop@Pod/p/api-1 first","Investigate ghost/0@Pod/none"]}`,
	)}}

	headline, actions, warnings := Synthesize(context.Background(), l, findings, SynthCounts{Critical: 1}, []string{"k8s"}, 512)

	require.Equal(t, 1, l.calls)
	require.Equal(t, "payments is down", headline)
	require.Equal(t, []string{"Fix reliability/crashloop@Pod/p/api-1 first"}, actions)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "ghost/0@Pod/none")

	// Input must not be mutated.
	require.Equal(t, "OOMKilled", findings[0].Analysis.ProbableCause)
}

func TestSynthesizeKeepsProseAction(t *testing.T) {
	findings := []analyzer.Finding{{
		RuleID:   "a/1",
		Severity: analyzer.SeverityWarning,
		Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "x"},
		Summary:  "s",
	}}
	l := &fakeLLM{responses: []llm.Response{synthTextResp(
		`{"headline":"h","actions":["Roll back the recent deploy","Check node capacity"]}`,
	)}}

	headline, actions, warnings := Synthesize(context.Background(), l, findings, SynthCounts{}, nil, 512)

	require.Equal(t, "h", headline)
	require.Equal(t, []string{"Roll back the recent deploy", "Check node capacity"}, actions)
	require.Empty(t, warnings)
}

func TestSynthesizeLLMErrorEmpty(t *testing.T) {
	findings := []analyzer.Finding{{
		RuleID:   "a/1",
		Severity: analyzer.SeverityWarning,
		Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "x"},
		Summary:  "s",
	}}
	l := &fakeLLM{errs: []error{errors.New("boom")}}

	headline, actions, warnings := Synthesize(context.Background(), l, findings, SynthCounts{}, nil, 512)

	require.Empty(t, headline)
	require.Nil(t, actions)
	require.Equal(t, []string{"synthesize: boom"}, warnings)
}

func TestSynthesizeNoFindingsNoCall(t *testing.T) {
	l := &fakeLLM{}

	headline, actions, warnings := Synthesize(context.Background(), l, nil, SynthCounts{}, nil, 512)

	require.Equal(t, 0, l.calls)
	require.Empty(t, headline)
	require.Nil(t, actions)
	require.Nil(t, warnings)
}
