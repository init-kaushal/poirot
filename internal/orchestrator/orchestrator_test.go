package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func testConfig() *config.Config {
	c := config.Default()
	c.LLM.Provider = "none"
	return c
}

func TestRunProducesReliabilityReport(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "api", RestartCount: 9,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}}},
		},
	)
	src := k8s.NewWithClient(cs, "test-ctx", k8s.Scope{Lookback: 24 * time.Hour})

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: src})
	require.NoError(t, err)

	require.Equal(t, "test-ctx", res.Report.Meta.Context)
	require.GreaterOrEqual(t, res.Report.Meta.Counts.Critical, 1)
	require.Equal(t, 2, res.ExitCode) // default failOn=critical, a critical finding present

	var found bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "reliability/crashloop" {
			found = true
		}
	}
	require.True(t, found)
}

func TestRunFailsWhenK8sUnavailable(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Config:  testConfig(),
		Version: "test",
		K8s:     unreachableK8s{},
	})
	require.ErrorContains(t, err, "not reachable")
}

// unreachableK8s stubs a k8s source whose API server cannot be reached.
type unreachableK8s struct{}

func (unreachableK8s) Name() string { return "k8s" }

func (unreachableK8s) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAbsent, Detail: "dial tcp: refused"}
}

func (unreachableK8s) Capabilities() []connector.Capability { return nil }

func (unreachableK8s) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func (unreachableK8s) Collect(context.Context) (*snapshot.Snapshot, error) {
	return nil, errors.New("unreachable")
}

func (unreachableK8s) ContextName() string { return "" }

func (unreachableK8s) Clientset() kubernetes.Interface { return fake.NewSimpleClientset() }
