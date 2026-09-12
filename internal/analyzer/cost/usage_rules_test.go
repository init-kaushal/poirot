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
	require.Contains(t, f.Summary, "Set requests to cpu=") // I9: concrete remediation
	require.Contains(t, f.Summary, "$")
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
	// I3: severity is always Info now — CPU alone can't prove a workload is
	// unused, even at a high MonthlyCost, so the old >=20 => Warning
	// escalation must not fire for this rule.
	require.Equal(t, analyzer.SeverityInfo, f.Severity)
	require.Contains(t, f.Summary, "verify first") // I3: hedged language
	sav := evByKey(f, "estimatedMonthlySaving")
	require.NotNil(t, sav)
	require.Equal(t, fire.MonthlyCost, sav.Value)
	require.Contains(t, f.Summary, "$30.00") // I9: concrete remediation

	ds := fire
	ds.Kind = "DaemonSet"
	_, ok = checkIdle(ds, snapshot.CostMeasured, hpa)
	require.False(t, ok)
}

func TestIdleCapsAtInfoEvenAtHighMonthlyCost(t *testing.T) { // I3
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "pricey", Replicas: 2,
		CPURequestCores: 4, CPUUsageCores: 0.001, MemRequestBytes: 4 << 30, MemUsageBytes: 1 << 20,
		MonthlyCost: 500, MonthlyCPUCost: 400, MonthlyMemCost: 100}
	f, ok := checkIdle(wl, snapshot.CostMeasured, map[string]bool{})
	require.True(t, ok)
	require.Equal(t, analyzer.SeverityInfo, f.Severity) // old code would have escalated to Warning here
}

func TestOverReplicatedFiresAndSuppressedByHPA(t *testing.T) {
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "over", Replicas: 6,
		CPURequestCores: 6, CPUUsageCores: 0.3, MemRequestBytes: 6 << 30, MemUsageBytes: 1 << 30,
		MonthlyCost: 100, MonthlyCPUCost: 80, MonthlyMemCost: 20}
	f, ok := checkOverReplicated(wl, snapshot.CostMeasured, map[string]bool{})
	require.True(t, ok)
	require.Equal(t, "cost/over-replicated", f.RuleID)
	require.NotNil(t, evByKey(f, "suggestedReplicas"))
	require.Contains(t, f.Summary, "Reduce replicas from 6 to") // I9: concrete remediation
	require.Contains(t, f.Summary, "$")

	_, ok = checkOverReplicated(wl, snapshot.CostMeasured, map[string]bool{key(wl): true})
	require.False(t, ok)
}
