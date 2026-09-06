package report

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

// TestBuildOutputIsByteStable guards the whole report pipeline (Build -> JSON /
// Markdown) against run-to-run ordering nondeterminism. At least one finding's
// Evidence[0].Value is a map[string]any assembled by *ranging a Go map* (whose
// iteration order Go deliberately randomises) with >=4 keys; the findings also
// span 2 domains and 2+ severities. Build + JSON + Markdown are rendered 50x and
// every result must equal the first.
//
// What actually makes this pass: encoding/json marshals map keys in sorted
// order, so a map reaching the report renders stably regardless of how it was
// built. The *upstream* producers that flatten maps into finding-bound slices
// (metrics.Collect sorting every result's Samples; the change analyzer sorting
// owned ReplicaSets) are where real determinism is enforced — this test only
// catches a regression that reintroduces map-iteration order into report
// rendering itself.
func TestBuildOutputIsByteStable(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// Built by ranging a Go map with >4 keys — iteration order is randomised
	// per run, so a non-sorting render path would diverge across the 50 loops.
	labelSet := map[string]string{
		"namespace": "prod",
		"pod":       "api-0",
		"container": "api",
		"node":      "node-3",
		"reason":    "BackOff",
	}
	eventEvidence := map[string]any{"eventCount": 9}
	for k, v := range labelSet {
		eventEvidence[k] = v
	}
	eventEvidence["reasons"] = []string{"BackOff", "FailedMount", "FailedScheduling", "Unhealthy"}

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
				Value:  eventEvidence,
				At:     at,
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

// sortFixture is a small unsorted finding set spanning 2 domains and 3
// severities, with evidence, used by the analysis-independence tests.
func sortFixture(at time.Time) []analyzer.Finding {
	return []analyzer.Finding{
		{
			RuleID: "reliability/no-limits", Domain: "reliability", Severity: analyzer.SeverityInfo,
			Title: "No resource limits", Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "b"},
			Summary: "Pod/n/b has no limits", Evidence: []analyzer.Evidence{ev("pod.spec.containers[].resources", nil, at)},
		},
		{
			RuleID: "reliability/crashloop", Domain: "reliability", Severity: analyzer.SeverityCritical,
			Title: "CrashLoopBackOff", Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "z"},
			Summary: "Pod/n/z crashlooping", Evidence: []analyzer.Evidence{ev("pod.status", "CrashLoopBackOff", at)},
		},
		{
			RuleID: "security/privileged", Domain: "security", Severity: analyzer.SeverityWarning,
			Title: "Privileged container", Object: analyzer.ObjectRef{Kind: "DaemonSet", Namespace: "kube-system", Name: "agent"},
			Summary: "DaemonSet/kube-system/agent is privileged", Evidence: []analyzer.Evidence{ev("pod.spec.containers[].securityContext.privileged", true, at)},
		},
		{
			RuleID: "reliability/oomkilled", Domain: "reliability", Severity: analyzer.SeverityWarning,
			Title: "OOMKilled", Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "a"},
			Summary: "Pod/n/a OOMKilled", Evidence: []analyzer.Evidence{ev("pod.status.lastState", "OOMKilled", at)},
		},
	}
}

// TestReportSortingIgnoresAnalysis proves the deterministic core (finding order
// + every non-Analysis field) is byte-for-byte independent of whether an
// *Analysis / Summary is attached: the LLM layer decorates, it never reorders.
func TestReportSortingIgnoresAnalysis(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	meta := Meta{Version: "1.0.0", GeneratedAt: at}

	plain := sortFixture(at)

	decorated := sortFixture(at)
	for i := range decorated {
		decorated[i].Analysis = &analyzer.Analysis{
			ProbableCause: "cause-" + decorated[i].RuleID,
			Confidence:    "high",
			Remediation:   "fix-" + decorated[i].RuleID,
		}
	}

	a := Build(meta, nil, plain)
	b := Build(meta, nil, decorated)
	b.Summary = &Summary{Headline: "things are on fire", Actions: []string{"page someone"}}

	require.Equal(t, len(a.Findings), len(b.Findings))
	for i := range a.Findings {
		require.Nil(t, a.Findings[i].Analysis)
		require.NotNil(t, b.Findings[i].Analysis, "fixture should decorate every finding")

		require.Equal(t, a.Findings[i].RuleID, b.Findings[i].RuleID, "order diverged at %d", i)
		require.Equal(t, a.Findings[i].Domain, b.Findings[i].Domain)
		require.Equal(t, a.Findings[i].Severity, b.Findings[i].Severity)
		require.True(t, reflect.DeepEqual(a.Findings[i].Object, b.Findings[i].Object), "Object differs at %d", i)
		require.True(t, reflect.DeepEqual(a.Findings[i].Evidence, b.Findings[i].Evidence), "Evidence differs at %d", i)
	}
	require.Equal(t, a.Meta.Counts, b.Meta.Counts)
}

// TestBuildOutputIsByteStableWithFixedLLMContent is the sibling of
// TestBuildOutputIsByteStable: byte-stability holds for the deterministic core
// plus any *fixed* LLM content; live LLM content varies and is out of scope for
// this test. It renders 50x with a fixed *Analysis on every finding and a fixed
// Summary, blanking Meta.LLM before each JSON() call.
func TestBuildOutputIsByteStableWithFixedLLMContent(t *testing.T) {
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	meta := Meta{Version: "1.2.3", GeneratedAt: at, Context: "prod", Lookback: "24h"}

	fixed := sortFixture(at)
	for i := range fixed {
		fixed[i].Analysis = &analyzer.Analysis{
			ProbableCause:      "fixed cause",
			CorrelatedFindings: []string{"reliability/crashloop@Pod/n/z"},
			Confidence:         "medium",
			Remediation:        "fixed remediation",
		}
	}
	fixedSummary := &Summary{Headline: "fixed headline", Actions: []string{"a", "b"}}

	render := func() string {
		r := Build(meta, nil, fixed)
		r.Summary = fixedSummary
		r.Meta.LLM = nil // live LLM meta varies run-to-run; excluded from the byte-stability guarantee
		j, err := r.JSON()
		require.NoError(t, err)
		return string(j)
	}

	first := render()
	require.Contains(t, first, `"analysis"`)
	require.Contains(t, first, `"summary"`)
	require.NotContains(t, first, `"llm"`)
	for i := 0; i < 50; i++ {
		require.Equal(t, first, render(), "JSON output differs on iteration %d", i)
	}
}
