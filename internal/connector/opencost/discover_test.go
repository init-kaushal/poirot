package opencost

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDiscoverFindsOpenCost(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "opencost", Namespace: "opencost"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 9003}}},
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.NotNil(t, tgt)
	require.Equal(t, "opencost", tgt.Name)
	require.Equal(t, "http", tgt.Port) // the port's NAME (promql pattern)
	require.Equal(t, "http", tgt.Scheme)
}

func TestDiscoverNilWhenAbsent(t *testing.T) {
	tgt, err := Discover(context.Background(), fake.NewSimpleClientset(), nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}

func TestDiscoverPicksNamedPortOverOther(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cost-analyzer", Namespace: "kubecost"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "tcp-model", Port: 9003}, {Name: "http", Port: 9090},
		}},
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Equal(t, "http", tgt.Port) // named "http" wins over "tcp-model"
}

func TestDiscoverUnnamedPortUsesNumber(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "opencost", Namespace: "opencost"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9003}}}, // no Name
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Equal(t, "9003", tgt.Port) // portString falls back to the number
}

func TestDiscoverIgnoresNonCandidateService(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana", Namespace: "monitoring"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 3000}}},
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}
