package slo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func ids(fs []analyzer.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RuleID
	}
	return out
}

func mset(results ...snapshot.MetricResult) *snapshot.MetricSet {
	return &snapshot.MetricSet{Backend: "test", CollectedAt: time.Unix(1735732800, 0), Results: results}
}

func TestCPUSaturationWarns(t *testing.T) {
	m := mset(snapshot.MetricResult{
		Name: "cpu_saturation", Expr: "rate(...)",
		Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "p", "pod": "api-1", "container": "api"}, Value: 0.95},
			{Labels: map[string]string{"namespace": "p", "pod": "api-2", "container": "api"}, Value: 0.20},
		},
	})
	fs := checkCPUSaturation(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/cpu-saturation", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "Pod/p/api-1", fs[0].Object.String())
	require.Equal(t, "promql", fs[0].Evidence[0].Source)
}

func TestMemSaturationCriticalAt98(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "mem_saturation", Samples: []snapshot.MetricSample{
		{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.99},
	}})
	fs := checkMemSaturation(m)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
}

func TestTargetsDown(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "targets_down", Samples: []snapshot.MetricSample{
		{Labels: map[string]string{"job": "kubelet", "instance": "10.0.0.1:10250"}, Value: 0},
	}})
	fs := checkTargetsDown(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/target-down", fs[0].RuleID)
}

func TestPodNotReady(t *testing.T) {
	m := mset(snapshot.MetricResult{
		Name: "pod_not_ready", Expr: `max by (namespace, pod) (kube_pod_status_ready{condition="true"} == 0)`,
		Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "shop", "pod": "cart-7d9"}, Value: 0},
		},
	})
	fs := checkPodNotReady(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/not-ready", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "Pod/shop/cart-7d9", fs[0].Object.String())
	require.Equal(t, "promql", fs[0].Evidence[0].Source)
}

func TestRestartRate(t *testing.T) {
	m := mset(snapshot.MetricResult{
		Name: "restart_rate", Expr: "rate(...)",
		Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "p", "pod": "api-1"}, Value: 2.5},
			{Labels: map[string]string{"namespace": "p", "pod": "api-2"}, Value: 0.4},
		},
	})
	fs := checkRestartRate(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/restart-rate", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "Pod/p/api-1", fs[0].Object.String())
}

func TestQueryErrorProducesInfoFinding(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "targets_down", Expr: "up == 0", Error: "boom"})
	fs := checkQueryErrors(m)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityInfo, fs[0].Severity)
}

func TestAnalyzeNilMetricsIsEmpty(t *testing.T) {
	fs, err := New().Analyze(context.Background(), &snapshot.Snapshot{})
	require.NoError(t, err)
	require.Empty(t, fs)
}

func TestAnalyzeRunsAllRules(t *testing.T) {
	snap := &snapshot.Snapshot{Metrics: mset(
		snapshot.MetricResult{Name: "mem_saturation", Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.95},
		}},
	)}
	fs, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	require.Contains(t, ids(fs), "slo/mem-saturation")
}
