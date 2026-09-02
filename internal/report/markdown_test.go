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

func TestMarkdownNoFindings(t *testing.T) {
	r := Build(Meta{Version: "1.0.0", Context: "dev", Lookback: "24h"}, nil, nil)
	got, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(got), "No findings. ✅")
	require.Contains(t, string(got), "All configured checks ran.")
}
