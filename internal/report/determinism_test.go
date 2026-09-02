package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

// TestBuildOutputIsByteStable guards the whole report pipeline (Build -> JSON /
// Markdown) against run-to-run ordering nondeterminism. The findings below
// include one whose evidence value is a map[string]any{"reasons": []string{...}}
// with several elements and a Summary that mentions them; if any stage ranged a
// map without sorting, repeated renders would diverge.
func TestBuildOutputIsByteStable(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	findings := []analyzer.Finding{
		{
			RuleID:   "reliability/warning-events",
			Domain:   "reliability",
			Severity: analyzer.SeverityInfo,
			Title:    "Object has repeated Warning events",
			Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "prod", Name: "api-0"},
			Summary:  "Pod/prod/api-0 has 9 Warning events in the lookback window (BackOff, FailedMount, FailedScheduling, Unhealthy)",
			Evidence: []analyzer.Evidence{{
				Source: "k8s",
				Query:  "events[type=Warning] grouped by involvedObject",
				Value: map[string]any{
					"eventCount": 9,
					"reasons":    []string{"BackOff", "FailedMount", "FailedScheduling", "Unhealthy"},
				},
				At: at,
			}},
		},
		{
			RuleID:   "reliability/crashloop",
			Domain:   "reliability",
			Severity: analyzer.SeverityCritical,
			Title:    "Container in CrashLoopBackOff",
			Object:   analyzer.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: "prod", Name: "worker-2"},
			Summary:  "container \"worker\" in Pod/prod/worker-2 is in CrashLoopBackOff",
			Evidence: []analyzer.Evidence{ev("pod.status", "CrashLoopBackOff", at)},
		},
		{
			RuleID:   "security/privileged",
			Domain:   "security",
			Severity: analyzer.SeverityWarning,
			Title:    "Privileged container",
			Object:   analyzer.ObjectRef{APIVersion: "apps/v1", Kind: "DaemonSet", Namespace: "kube-system", Name: "agent"},
			Summary:  "DaemonSet/kube-system/agent runs a privileged container",
			Evidence: []analyzer.Evidence{ev("pod.spec.containers[].securityContext.privileged", true, at)},
		},
		{
			RuleID:   "security/no-netpol",
			Domain:   "security",
			Severity: analyzer.SeverityInfo,
			Title:    "Namespace has no NetworkPolicy",
			Object:   analyzer.ObjectRef{Kind: "Namespace", Name: "prod"},
			Summary:  "Namespace prod has no NetworkPolicy",
			Evidence: []analyzer.Evidence{ev("networkpolicies", 0, at)},
		},
	}

	meta := Meta{Version: "1.2.3", GeneratedAt: at, Context: "prod-cluster", Lookback: "24h", Namespaces: []string{"prod", "kube-system"}}
	statuses := []connector.Status{
		{Name: "k8s", Availability: connector.Availability{State: connector.StateAvailable}},
	}

	firstJSON, err := Build(meta, statuses, findings).JSON()
	require.NoError(t, err)
	firstMD, err := Build(meta, statuses, findings).Markdown()
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		r := Build(meta, statuses, findings)
		j, err := r.JSON()
		require.NoError(t, err)
		require.Equal(t, string(firstJSON), string(j), "JSON output differs on iteration %d", i)

		m, err := r.Markdown()
		require.NoError(t, err)
		require.Equal(t, string(firstMD), string(m), "Markdown output differs on iteration %d", i)
	}
}

func ev(query string, value any, at time.Time) analyzer.Evidence {
	return analyzer.Evidence{Source: "k8s", Query: query, Value: value, At: at}
}
