package report

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

func TestMarkdownGolden(t *testing.T) {
	gen, _ := time.Parse(time.RFC3339, "2026-09-01T10:00:00Z")
	r := Build(
		Meta{Version: "1.0.0", GeneratedAt: gen, Context: "prod", Lookback: "24h"},
		[]connector.Status{
			{Name: "k8s", Availability: connector.Availability{State: connector.StateAvailable}},
			{Name: "promql", Availability: connector.Availability{State: connector.StateAbsent, Reason: "not configured"}},
		},
		[]analyzer.Finding{
			{
				RuleID: "reliability/crashloop", Domain: "reliability", Severity: analyzer.SeverityCritical,
				Title: "Container in CrashLoopBackOff", Summary: "container \"api\" in Pod/payments/api-1 is in CrashLoopBackOff (7 restarts)",
				Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "payments", Name: "api-1"},
				Evidence: []analyzer.Evidence{{Source: "k8s", Query: "pod.status.containerStatuses[].state.waiting.reason", Value: "CrashLoopBackOff", At: gen}},
			},
		},
	)

	got, err := r.Markdown()
	require.NoError(t, err)

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile("testdata/golden_basic.md", got, 0o644))
	}
	want, err := os.ReadFile("testdata/golden_basic.md")
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestMarkdownEmptyEvidenceNoDanglingHeader(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"}, nil, []analyzer.Finding{{
		RuleID: "x/y", Domain: "x", Severity: analyzer.SeverityWarning, Title: "T",
		Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}, Summary: "s", // no Evidence
	}})
	md, err := r.Markdown()
	require.NoError(t, err)
	require.NotContains(t, string(md), "Evidence:\n\n") // no header with nothing under it
	require.NotContains(t, string(md), "Evidence:")     // finding has none at all
}

func TestMarkdownAllSkippedIsNotACleanBill(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"}, nil, []analyzer.Finding{{
		RuleID: "slo/skipped", Domain: "slo", Severity: analyzer.SeverityInfo,
		Title: "Analyzer skipped", Summary: "slo analysis skipped — missing connector(s): promql",
	}})
	md, err := r.Markdown()
	require.NoError(t, err)
	require.NotContains(t, string(md), "No findings. ✅")
	require.Contains(t, string(md), "No findings from the checks that ran.")
	require.Contains(t, string(md), "slo analysis skipped")
}

func TestMarkdownEscapesPipeInConnectorDetail(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"},
		[]connector.Status{{Name: "promql", Availability: connector.Availability{
			State: connector.StateDegraded, Reason: "unreachable", Detail: "dial a|b failed"}}}, nil)
	md, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(md), `dial a\|b failed`)
}

func TestMarkdownNoFindings(t *testing.T) {
	r := Build(Meta{Version: "1.0.0", Context: "dev", Lookback: "24h"}, nil, nil)
	got, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(got), "No findings. ✅")
	require.Contains(t, string(got), "All configured checks ran.")
}
