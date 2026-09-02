package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCollectGathersScopedObjects(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sys", Namespace: "kube-system"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"}},
	)
	c := NewWithClient(cs, "test-ctx", Scope{
		Exclude:  []string{"kube-system"},
		Lookback: 24 * time.Hour,
	})

	snap, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Equal(t, []string{"payments"}, snap.Meta.Namespaces)
	require.Equal(t, "test-ctx", snap.Meta.Context)
	require.Equal(t, 24*time.Hour, snap.Meta.Lookback)
	require.WithinDuration(t, time.Now(), snap.Meta.CollectedAt, time.Minute)

	require.Len(t, snap.Nodes, 1)
	require.Len(t, snap.Deployments, 1)
	require.Equal(t, "api", snap.Deployments[0].Name)
	require.Len(t, snap.Pods, 1)
}

func TestCollectExplicitNamespaces(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "da", Namespace: "a"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "b"}},
	)
	c := NewWithClient(cs, "x", Scope{Namespaces: []string{"a"}})

	snap, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, snap.Meta.Namespaces)
	require.Len(t, snap.Deployments, 1)
	require.Equal(t, "da", snap.Deployments[0].Name)
}
