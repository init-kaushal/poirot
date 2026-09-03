package promql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func svc(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func TestDiscoverFindsPrometheusByName(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "monitoring"}},
		svc("monitoring", "grafana", corev1.ServicePort{Name: "http", Port: 80}),
		svc("monitoring", "prometheus-server", corev1.ServicePort{Name: "http", Port: 9090}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.NotNil(t, tgt)
	require.Equal(t, "monitoring", tgt.Namespace)
	require.Equal(t, "prometheus-server", tgt.Name)
	require.Equal(t, "http", tgt.Port)
	require.Equal(t, "http", tgt.Scheme)
}

func TestDiscoverPicksNumericPortWhenUnnamed(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "obs"}},
		svc("obs", "vmsingle", corev1.ServicePort{Port: 8429}),
	)
	tgt, err := Discover(context.Background(), cs, []string{"obs"})
	require.NoError(t, err)
	require.Equal(t, "8429", tgt.Port)
}

func TestDiscoverPrefersExactMatchOverPrefix(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "monitoring"}},
		svc("monitoring", "prometheus-alertmanager", corev1.ServicePort{Name: "http", Port: 9093}),
		svc("monitoring", "prometheus-server", corev1.ServicePort{Name: "http", Port: 9090}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.NotNil(t, tgt)
	require.Equal(t, "prometheus-server", tgt.Name)
}

func TestDiscoverDenylistSkipsAlertmanager(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "monitoring"}},
		svc("monitoring", "prometheus-alertmanager", corev1.ServicePort{Name: "http", Port: 9093}),
		svc("monitoring", "grafana", corev1.ServicePort{Name: "http", Port: 80}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}

func TestDiscoverNoMatchIsNotAnError(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		svc("default", "web", corev1.ServicePort{Port: 80}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}
