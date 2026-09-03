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
	"github.com/init-kaushal/poirot/internal/metrics"
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

// fakePromql is a hermetic promql connector stub for orchestrator tests.
type fakePromql struct {
	state   connector.State
	samples map[string][]snapshot.MetricSample // keyed by pack entry Expr
}

func (fakePromql) Name() string { return "promql" }

func (f fakePromql) Probe(context.Context) connector.Availability {
	return connector.Availability{State: f.state}
}

func (fakePromql) Capabilities() []connector.Capability { return nil }

func (f fakePromql) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Expr string `json:"expr"`
	}
	_ = json.Unmarshal(args, &a)
	return json.Marshal(f.samples[a.Expr])
}

func (fakePromql) Backend() string { return "fake" }

func TestRunCollectsMetricsAndRunsSLO(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p"}},
	)
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})

	pack := metrics.DefaultPack()
	fp := fakePromql{state: connector.StateAvailable, samples: map[string][]snapshot.MetricSample{
		pack[1].Expr: {{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.99}},
	}}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: src, Promql: fp})
	require.NoError(t, err)

	var found bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "slo/mem-saturation" {
			found = true
		}
	}
	require.True(t, found)
	require.GreaterOrEqual(t, res.Report.Meta.Counts.Critical, 1) // mem 0.99 => critical
}

func TestRunSkipsSLOWhenPromqlAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})
	fp := fakePromql{state: connector.StateAbsent}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: src, Promql: fp})
	require.NoError(t, err)

	var skipped bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "slo/skipped" {
			skipped = true
		}
	}
	require.True(t, skipped)
}
