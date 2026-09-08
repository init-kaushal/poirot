package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func TestRightsizingFiresBelowHalfUtil(t *testing.T) {
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "d", Replicas: 2,
		CPURequestCores: 2, CPUUsageCores: 0.4, MemRequestBytes: 1 << 30, MemUsageBytes: 900 << 20,
		MonthlyCost: 60, MonthlyCPUCost: 40, MonthlyMemCost: 20}
	f, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.True(t, ok)
	require.Equal(t, "cost/rightsizing", f.RuleID)
	require.Equal(t, "cost", f.Domain)
	require.Equal(t, analyzer.SeverityWarning, f.Severity) // saving on cpu alone >= 20
	require.Equal(t, wl.Kind, f.Object.Kind)
	require.NotNil(t, evByKey(f, "estimatedMonthlySaving"))
	require.NotNil(t, evByKey(f, "basis"))
}

func TestRightsizingDoesNotFireAt51Pct(t *testing.T) {
	wl := snapshot.WorkloadCost{Kind: "Deployment", Replicas: 1, CPURequestCores: 1, CPUUsageCores: 0.51,
		MemRequestBytes: 1 << 30, MemUsageBytes: 1 << 30, MonthlyCost: 60, MonthlyCPUCost: 40, MonthlyMemCost: 20}
	_, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.False(t, ok)
}

func TestRightsizingFiresOnMemWhenNoCPURequest(t *testing.T) { // spec R8
	wl := snapshot.WorkloadCost{Kind: "Deployment", Replicas: 1, CPURequestCores: 0, CPUUsageCores: 0.05,
		MemRequestBytes: 2 << 30, MemUsageBytes: 100 << 20, MonthlyCost: 40, MonthlyCPUCost: 0, MonthlyMemCost: 40}
	f, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.True(t, ok)
	require.NotNil(t, evByKey(f, "suggestedMemRequest"))
}

func TestIdleFiresAndNotForDaemonSet(t *testing.T) {
	hpa := map[string]bool{}
	fire := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "idle", Replicas: 2,
		CPURequestCores: 1, CPUUsageCores: 0.002, MemRequestBytes: 1 << 30, MemUsageBytes: 1 << 20,
		MonthlyCost: 30, MonthlyCPUCost: 20, MonthlyMemCost: 10}
	f, ok := checkIdle(fire, snapshot.CostMeasured, hpa)
	require.True(t, ok)
	require.Equal(t, "cost/idle", f.RuleID)
	require.Equal(t, analyzer.SeverityWarning, f.Severity) // MonthlyCost >= 20
	sav := evByKey(f, "estimatedMonthlySaving")
	require.NotNil(t, sav)
	require.Equal(t, fire.MonthlyCost, sav.Value)

	ds := fire
	ds.Kind = "DaemonSet"
	_, ok = checkIdle(ds, snapshot.CostMeasured, hpa)
	require.False(t, ok)
}

func TestOverReplicatedFiresAndSuppressedByHPA(t *testing.T) {
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "over", Replicas: 6,
		CPURequestCores: 6, CPUUsageCores: 0.3, MemRequestBytes: 6 << 30, MemUsageBytes: 1 << 30,
		MonthlyCost: 100, MonthlyCPUCost: 80, MonthlyMemCost: 20}
	f, ok := checkOverReplicated(wl, snapshot.CostMeasured, map[string]bool{})
	require.True(t, ok)
	require.Equal(t, "cost/over-replicated", f.RuleID)
	require.NotNil(t, evByKey(f, "suggestedReplicas"))

	_, ok = checkOverReplicated(wl, snapshot.CostMeasured, map[string]bool{key(wl): true})
	require.False(t, ok)
}
