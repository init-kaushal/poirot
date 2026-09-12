package cost

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func TestAnalyzeRunsAllRules(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-8 * 24 * time.Hour))
	snap := &snapshot.Snapshot{
		Cost: &snapshot.CostSet{
			Basis: snapshot.CostMeasured,
			Workloads: []snapshot.WorkloadCost{
				// wide + idle → idle + over-replicated
				{Namespace: "t", Kind: "Deployment", Name: "big", Replicas: 8,
					CPURequestCores: 8, CPUUsageCores: 0.03, MemRequestBytes: 8 << 30, MemUsageBytes: 1 << 30,
					MonthlyCost: 400, MonthlyCPUCost: 300, MonthlyMemCost: 100},
				// small + over-provisioned → rightsizing
				{Namespace: "t", Kind: "Deployment", Name: "small", Replicas: 1,
					CPURequestCores: 1, CPUUsageCores: 0.1, MemRequestBytes: 1 << 30, MemUsageBytes: 1 << 27,
					MonthlyCost: 50, MonthlyCPUCost: 40, MonthlyMemCost: 10},
			},
			Namespaces: []snapshot.NamespaceCost{
				{Namespace: "t", MonthlyCost: 300, PriorMonthlyCost: 100},
			},
		},
		PVCs: []corev1.PersistentVolumeClaim{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "t", Name: "orphan", CreationTimestamp: old},
			Status: corev1.PersistentVolumeClaimStatus{
				Phase:    corev1.ClaimBound,
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
			},
		}},
		Services: []corev1.Service{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "t", Name: "lb"},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeLoadBalancer,
				Selector: map[string]string{"app": "gone"},
			},
		}},
		Jobs: []batchv1.Job{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "t", Name: "j"},
			Status:     batchv1.JobStatus{Succeeded: 1, CompletionTime: &old},
		}},
	}

	out, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	ids := ruleIDs(out)
	for _, want := range []string{
		"cost/rightsizing", "cost/idle", "cost/over-replicated",
		"cost/orphaned-pvc", "cost/orphaned-lb", "cost/retained-jobs",
		"cost/namespace-spend-trend",
	} {
		require.Contains(t, ids, want)
	}
}

func TestAnalyzeBackfillsEvidenceAt(t *testing.T) { // I6
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	snap := &snapshot.Snapshot{Cost: &snapshot.CostSet{
		Basis: snapshot.CostMeasured, CollectedAt: at,
		Workloads: []snapshot.WorkloadCost{{Namespace: "t", Kind: "Deployment", Name: "idle", Replicas: 2,
			CPURequestCores: 1, CPUUsageCores: 0.001, MemRequestBytes: 1 << 30, MemUsageBytes: 1 << 20,
			MonthlyCost: 30, MonthlyCPUCost: 20, MonthlyMemCost: 10}},
	}}
	out, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	var got *analyzer.Finding
	for i := range out {
		if out[i].RuleID == "cost/idle" {
			got = &out[i]
		}
	}
	require.NotNil(t, got)
	require.NotEmpty(t, got.Evidence)
	for _, e := range got.Evidence {
		require.Equal(t, at, e.At)
	}
}

func TestSafeRatio(t *testing.T) {
	_, ok := safeRatio(1, 0)
	require.False(t, ok)
	v, ok := safeRatio(1, 4)
	require.True(t, ok)
	require.Equal(t, 0.25, v)
}
