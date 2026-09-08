package cost

import (
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

func TestOrphanedPVCFiresOnlyForBoundUnreferencedOld(t *testing.T) {
	old := metav1.NewTime(time.Now().Add(-8 * 24 * time.Hour))
	scName := "gp3"
	snap := &snapshot.Snapshot{
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "consumer"},
			Spec: corev1.PodSpec{Volumes: []corev1.Volume{{
				Name: "data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "mounted"},
				},
			}}},
		}},
		PVCs: []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "orphan", CreationTimestamp: old},
				Spec:       corev1.PersistentVolumeClaimSpec{StorageClassName: &scName},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase:    corev1.ClaimBound,
					Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "mounted", CreationTimestamp: old},
				Status: corev1.PersistentVolumeClaimStatus{
					Phase:    corev1.ClaimBound,
					Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pending", CreationTimestamp: old},
				Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimPending},
			},
		},
	}

	fs := checkOrphanedPVC(snap, snapshot.CostMeasured)
	require.Len(t, fs, 1)
	require.Equal(t, "cost/orphaned-pvc", fs[0].RuleID)
	require.Equal(t, "cost", fs[0].Domain)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "orphan", fs[0].Object.Name)
	require.Equal(t, "PersistentVolumeClaim", fs[0].Object.Kind)
	require.Equal(t, "gp3", evByKey(fs[0], "storageClass").Value)
	require.InDelta(t, 20.0, evByKey(fs[0], "capacityBytes").Value.(float64)/(1<<30), 0.5)
	require.InDelta(t, 2.0, evByKey(fs[0], "estimatedMonthlyCost").Value.(float64), 0.05)
	require.Equal(t, "measured", evByKey(fs[0], "basis").Value)
}

func TestOrphanedLBIgnoresEmptySelectorAndBackedServices(t *testing.T) {
	snap := &snapshot.Snapshot{
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "default", Name: "web-1",
				Labels: map[string]string{"app": "web", "tier": "fe"},
			},
			Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			}},
		}},
		Services: []corev1.Service{
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "lb-nosel"},
				Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "lb-backed"},
				Spec: corev1.ServiceSpec{
					Type:     corev1.ServiceTypeLoadBalancer,
					Selector: map[string]string{"app": "web"},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "lb-orphan"},
				Spec: corev1.ServiceSpec{
					Type:     corev1.ServiceTypeLoadBalancer,
					Selector: map[string]string{"app": "missing"},
				},
			},
		},
	}

	fs := checkOrphanedLB(snap, snapshot.CostMeasured)
	require.Len(t, fs, 1)
	require.Equal(t, "cost/orphaned-lb", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "lb-orphan", fs[0].Object.Name)
	require.Equal(t, "Service", fs[0].Object.Kind)
	require.Equal(t, 18.0, evByKey(fs[0], "estimatedMonthlyCost").Value)
	require.Equal(t, "measured", evByKey(fs[0], "basis").Value)
}

func TestRetainedJobsPerNamespaceAndNilSafe(t *testing.T) {
	require.Nil(t, checkRetainedJobs(&snapshot.Snapshot{Jobs: nil}))

	done := metav1.NewTime(time.Now().Add(-8 * 24 * time.Hour))
	snap := &snapshot.Snapshot{Jobs: []batchv1.Job{
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "batch", Name: "j1"},
			Status:     batchv1.JobStatus{Succeeded: 1, CompletionTime: &done},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Namespace: "batch", Name: "j2"},
			Status:     batchv1.JobStatus{Succeeded: 1, CompletionTime: &done},
		},
	}}

	fs := checkRetainedJobs(snap)
	require.Len(t, fs, 1)
	require.Equal(t, "cost/retained-jobs", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityInfo, fs[0].Severity)
	require.Equal(t, "Namespace", fs[0].Object.Kind)
	require.Equal(t, "batch", fs[0].Object.Name)
	require.Equal(t, 2, evByKey(fs[0], "count").Value)
}

func TestNamespaceSpendTrendZeroPriorNoInfEvidence(t *testing.T) {
	cs := &snapshot.CostSet{Namespaces: []snapshot.NamespaceCost{
		{Namespace: "new", MonthlyCost: 80, PriorMonthlyCost: 0},
	}}
	fs := checkNamespaceSpendTrend(cs)
	require.Len(t, fs, 1)
	require.Nil(t, evByKey(fs[0], "deltaPct"))
	for _, e := range fs[0].Evidence {
		require.NotEqual(t, "deltaPct", e.Query)
	}
	require.Equal(t, "measured", evByKey(fs[0], "basis").Value)
}

func TestNamespaceSpendTrendSkipsEstimatePath(t *testing.T) {
	require.Empty(t, checkNamespaceSpendTrend(&snapshot.CostSet{
		Namespaces: []snapshot.NamespaceCost{{Namespace: "x", MonthlyCost: 999, PriorMonthlyCost: -1}},
	}))
}
