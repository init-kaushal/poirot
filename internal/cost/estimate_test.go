package cost

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestEstimateWorkloadCostFromRequests(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n1", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("team", "web-abc", "n1", ctrlRef("Deployment", "team", "web"))},
		Deployments: []appsv1.Deployment{deploy("team", "web", 3, "500m", "1Gi")},
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	require.Equal(t, snapshot.CostEstimated, cs.Basis)
	require.Equal(t, "estimate", cs.Source)
	require.Equal(t, "USD", cs.Currency)
	require.Equal(t, "7d", cs.Window)
	require.Equal(t, time.Unix(0, 0).UTC(), cs.CollectedAt)

	w := cs.Workloads[0]
	require.Equal(t, int32(3), w.Replicas)
	require.InDelta(t, 1.5, w.CPURequestCores, 1e-6)             // 0.5 * 3
	require.InDelta(t, float64(3*(1<<30)), w.MemRequestBytes, 1) // 1Gi * 3
	require.InDelta(t, 1.5*0.0210*730, w.MonthlyCPUCost, 1e-3)   // m6i.large cpuHour
	require.Equal(t, -1.0, w.CPUUsageCores)                      // no promql
	require.Equal(t, -1.0, w.MemUsageBytes)
	require.Contains(t, cs.Note, "usage unavailable")

	require.Len(t, cs.Namespaces, 1)
	require.Equal(t, "team", cs.Namespaces[0].Namespace)
	require.InDelta(t, w.MonthlyCost, cs.Namespaces[0].MonthlyCost, 1e-9)
	require.Equal(t, -1.0, cs.Namespaces[0].PriorMonthlyCost)
}

func TestEstimateDaemonSetUsesRunningPodCountNotNodes(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:       snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:      []corev1.Node{node("n1", "m6i.large"), node("n2", "m6i.large"), node("n3", "m6i.large")},
		Pods:       []corev1.Pod{runningPod("kube-system", "agent-1", "n1", ctrlRef("DaemonSet", "kube-system", "agent"))}, // only 1 of 3 nodes
		DaemonSets: []appsv1.DaemonSet{ds("kube-system", "agent", "100m", "128Mi")},
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	require.Equal(t, int32(1), cs.Workloads[0].Replicas)
}

func TestEstimateInstanceTypeTieBreakIsDeterministic(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:  snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes: []corev1.Node{node("a", "c6i.large"), node("b", "m6i.large")},
		Pods: []corev1.Pod{
			runningPod("t", "p1", "a", ctrlRef("Deployment", "t", "d")),
			runningPod("t", "p2", "b", ctrlRef("Deployment", "t", "d")),
		},
		Deployments: []appsv1.Deployment{deploy("t", "d", 2, "1", "1Gi")},
	}
	var first float64
	for i := 0; i < 20; i++ {
		cs := Estimate(context.Background(), snap, nil, mustSheet(t))
		if i == 0 {
			first = cs.Workloads[0].MonthlyCPUCost
		}
		require.Equal(t, first, cs.Workloads[0].MonthlyCPUCost) // c6i.large wins (smaller string)
	}
	require.InDelta(t, 2*0.0250*730, first, 1e-3) // c6i.large cpuHour, cores=2
}

func TestEstimateFillsUsageFromPromql(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakePromql{samples: map[string][]snapshot.MetricSample{
		"promql.instant/cpu": {{Labels: map[string]string{"namespace": "t", "workload": "web"}, Value: 0.42}},
		"promql.instant/mem": {{Labels: map[string]string{"namespace": "t", "workload": "web"}, Value: 5 << 20}},
	}})
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "web-7d9f8b6c5-x", "n", ctrlRef("Deployment", "t", "web"))},
		Deployments: []appsv1.Deployment{deploy("t", "web", 2, "1", "1Gi")},
	}
	cs := Estimate(context.Background(), snap, reg, mustSheet(t))
	require.InDelta(t, 0.42, cs.Workloads[0].CPUUsageCores, 1e-9)
	require.InDelta(t, float64(5<<20), cs.Workloads[0].MemUsageBytes, 1)
	require.NotContains(t, cs.Note, "usage unavailable")
}

func TestEstimateUsageErrorFallsToMinusOne(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakePromql{err: true})
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "p", "n", ctrlRef("Deployment", "t", "d"))},
		Deployments: []appsv1.Deployment{deploy("t", "d", 1, "500m", "512Mi")},
	}
	cs := Estimate(context.Background(), snap, reg, mustSheet(t))
	require.Equal(t, -1.0, cs.Workloads[0].CPUUsageCores)
	require.Equal(t, -1.0, cs.Workloads[0].MemUsageBytes)
	require.Contains(t, cs.Note, "usage unavailable")
}

// fakePromql is a minimal connector.Connector for the usage path.
type fakePromql struct {
	samples map[string][]snapshot.MetricSample
	err     bool
}

func (fakePromql) Name() string { return "promql" }

func (fakePromql) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAvailable}
}

func (fakePromql) Capabilities() []connector.Capability { return nil }

func (f fakePromql) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	if f.err {
		return nil, errors.New("promql: backend unreachable")
	}
	var a struct {
		Expr string `json:"expr"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(a.Expr, "container_cpu_usage"):
		return json.Marshal(f.samples["promql.instant/cpu"])
	case strings.Contains(a.Expr, "container_memory_working_set"):
		return json.Marshal(f.samples["promql.instant/mem"])
	}
	return json.Marshal([]snapshot.MetricSample{})
}
