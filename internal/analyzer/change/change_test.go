package change

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func i32(v int32) *int32 { return &v }

func TestRecentRolloutFires(t *testing.T) {
	now := time.Now()
	depUID := types.UID("dep-uid")
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", UID: depUID, Generation: 3,
			CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Spec:   appsv1.DeploymentSpec{Replicas: i32(3)},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 3, ReadyReplicas: 2},
	}
	rsOld := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-old", Namespace: "n", CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour)),
		OwnerReferences: []metav1.OwnerReference{{UID: depUID}},
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"}}}
	rsNew := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-new", Namespace: "n", CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
		OwnerReferences: []metav1.OwnerReference{{UID: depUID}},
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "2"}}}

	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Deployments: []appsv1.Deployment{dep},
		ReplicaSets: []appsv1.ReplicaSet{rsOld, rsNew},
	}
	fs := checkRecentRollout(snap)
	require.Len(t, fs, 1)
	require.Equal(t, "change/recent-rollout", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityInfo, fs[0].Severity)
	require.Contains(t, fs[0].Summary, "revision 2")
}

func TestRolloutStuckOnGenerationLag(t *testing.T) {
	now := time.Now()
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", Generation: 5,
			CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 4},
	}
	snap := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour}, Deployments: []appsv1.Deployment{dep}}
	fs := checkRolloutStuck(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
}

func TestReplicaSetChurn(t *testing.T) {
	now := time.Now()
	uid := types.UID("d")
	dep := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", UID: uid,
		CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))}}
	var rs []appsv1.ReplicaSet
	for i := 0; i < 4; i++ {
		rs = append(rs, appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "api-" + string(rune('a'+i)), Namespace: "n",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(i*10) * time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{UID: uid}}}})
	}
	snap := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Deployments: []appsv1.Deployment{dep}, ReplicaSets: rs}
	fs := checkReplicaSetChurn(snap)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Summary, "4 ReplicaSet revisions")
}

func TestAnalyzeRunsAllRules(t *testing.T) {
	fs, err := New().Analyze(context.Background(), &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: time.Hour}})
	require.NoError(t, err)
	require.Empty(t, fs) // nothing to report on an empty snapshot
}
