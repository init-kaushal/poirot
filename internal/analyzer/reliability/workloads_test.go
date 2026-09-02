package reliability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func i32(v int32) *int32 { return &v }

func TestSingleReplicaAndNoPDB(t *testing.T) {
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
	}
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Now()},
		Deployments: []appsv1.Deployment{dep},
	}
	require.Empty(t, checkSingleReplica(snap))
	nopdb := checkNoPDB(snap)
	require.Len(t, nopdb, 1)
	require.Equal(t, "reliability/no-pdb", nopdb[0].RuleID)

	snap.PDBs = []policyv1.PodDisruptionBudget{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
	}}
	require.Empty(t, checkNoPDB(snap))
}

func TestHPAMaxed(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now()},
		HPAs: []autoscalingv2.HorizontalPodAutoscaler{{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
			Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
			Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 10, DesiredReplicas: 12},
		}},
	}
	fs := checkHPAMaxed(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
}

func TestNodePressureCriticalWhenNotReady(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now()},
		Nodes: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "w1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			}},
		}},
	}
	fs := checkNodePressure(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
	require.Equal(t, "Node/w1", fs[0].Object.String())
}

func TestWarningEventClustersSkipWhenSpecificExists(t *testing.T) {
	now := time.Now()
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Events: []corev1.Event{{
			Type:           corev1.EventTypeWarning,
			Reason:         "BackOff",
			Count:          5,
			LastTimestamp:  metav1.NewTime(now.Add(-5 * time.Minute)),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "n", Name: "api-1"},
		}},
	}
	existing := []analyzer.Finding{{
		RuleID: "reliability/crashloop",
		Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "api-1"},
	}}
	require.Empty(t, checkWarningEventClusters(snap, existing))
	require.Len(t, checkWarningEventClusters(snap, nil), 1)
}

func TestWarningEventClustersReasonsAreSorted(t *testing.T) {
	now := time.Now()
	seen := metav1.NewTime(now.Add(-time.Minute))
	obj := corev1.ObjectReference{Kind: "Pod", Namespace: "n", Name: "api-1"}
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Events: []corev1.Event{
			{Type: corev1.EventTypeWarning, Reason: "Unhealthy", Count: 1, LastTimestamp: seen, InvolvedObject: obj},
			{Type: corev1.EventTypeWarning, Reason: "BackOff", Count: 1, LastTimestamp: seen, InvolvedObject: obj},
			{Type: corev1.EventTypeWarning, Reason: "FailedMount", Count: 1, LastTimestamp: seen, InvolvedObject: obj},
		},
	}

	fs := checkWarningEventClusters(snap, nil)
	require.Len(t, fs, 1)

	val, ok := fs[0].Evidence[0].Value.(map[string]any)
	require.True(t, ok)
	reasons, ok := val["reasons"].([]string)
	require.True(t, ok)
	require.Equal(t, []string{"BackOff", "FailedMount", "Unhealthy"}, reasons)
	require.Contains(t, fs[0].Summary, "BackOff, FailedMount, Unhealthy")
}
