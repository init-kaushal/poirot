package cost

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func evByKey(f analyzer.Finding, key string) *analyzer.Evidence {
	for i := range f.Evidence {
		if f.Evidence[i].Query == key {
			return &f.Evidence[i]
		}
	}
	return nil
}

func ruleIDs(fs []analyzer.Finding) []string {
	var ids []string
	for _, f := range fs {
		ids = append(ids, f.RuleID)
	}
	return ids
}

func TestAnalyzeNilCostGivesSkipped(t *testing.T) {
	out, err := New().Analyze(context.Background(), &snapshot.Snapshot{})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "cost/skipped", out[0].RuleID)
	require.Equal(t, analyzer.SeverityInfo, out[0].Severity)
}

func TestAnalyzeEmitsRightsizingLimitedWhenNoUsage(t *testing.T) {
	snap := &snapshot.Snapshot{Cost: &snapshot.CostSet{Basis: snapshot.CostEstimated,
		Workloads: []snapshot.WorkloadCost{{Kind: "Deployment", Name: "d", Namespace: "t", Replicas: 1,
			CPURequestCores: 1, CPUUsageCores: -1, MemRequestBytes: 1 << 30, MemUsageBytes: -1, MonthlyCost: 50}}}}
	out, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	var got *analyzer.Finding
	for i := range out {
		if out[i].RuleID == "cost/rightsizing-limited" {
			got = &out[i]
		}
	}
	require.NotNil(t, got)
	require.Equal(t, "Cluster", got.Object.Kind) // spec R15 — not empty
}

func TestOverReplicatedSuppressesRightsizing(t *testing.T) { // spec R6
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "big", Replicas: 8,
		CPURequestCores: 8, CPUUsageCores: 0.4, MemRequestBytes: 8 << 30, MemUsageBytes: 1 << 30,
		MonthlyCost: 400, MonthlyCPUCost: 300, MonthlyMemCost: 100}
	out, _ := New().Analyze(context.Background(), &snapshot.Snapshot{Cost: &snapshot.CostSet{
		Basis: snapshot.CostMeasured, Workloads: []snapshot.WorkloadCost{wl}}})
	ids := ruleIDs(out)
	require.Contains(t, ids, "cost/over-replicated")
	require.NotContains(t, ids, "cost/rightsizing")
}

func TestSafeRatio(t *testing.T) {
	_, ok := safeRatio(1, 0)
	require.False(t, ok)
	v, ok := safeRatio(1, 4)
	require.True(t, ok)
	require.Equal(t, 0.25, v)
}
