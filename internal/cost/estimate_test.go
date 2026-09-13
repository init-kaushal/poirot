package cost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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

func TestEstimateUnmatchedWorkloadStaysMinusOneOnUsageSuccess(t *testing.T) {
	reg := connector.NewRegistry()
	// promql answers, but only for workload "web" — "api" gets no sample.
	reg.Register(fakePromql{samples: map[string][]snapshot.MetricSample{
		"promql.instant/cpu": {{Labels: map[string]string{"namespace": "t", "workload": "web"}, Value: 0.4}},
		"promql.instant/mem": {{Labels: map[string]string{"namespace": "t", "workload": "web"}, Value: 1 << 20}},
	}})
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:  snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes: []corev1.Node{node("n", "m6i.large")},
		Pods: []corev1.Pod{
			runningPod("t", "web-x", "n", ctrlRef("Deployment", "t", "web")),
			runningPod("t", "api-x", "n", ctrlRef("Deployment", "t", "api")),
		},
		Deployments: []appsv1.Deployment{deploy("t", "web", 1, "1", "1Gi"), deploy("t", "api", 1, "1", "1Gi")},
	}
	cs := Estimate(context.Background(), snap, reg, mustSheet(t))
	byName := map[string]snapshot.WorkloadCost{}
	for _, w := range cs.Workloads {
		byName[w.Name] = w
	}
	require.InDelta(t, 0.4, byName["web"].CPUUsageCores, 1e-9)
	require.Equal(t, -1.0, byName["api"].CPUUsageCores) // no sample → unknown, NOT 0
	require.Equal(t, -1.0, byName["api"].MemUsageBytes)
}

func TestEstimateDeploymentInstanceTypeViaReplicaSet(t *testing.T) {
	rsRef := ctrlRef("Deployment", "t", "web") // the RS is owned by the Deployment
	rs := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Namespace: "t", Name: "web-abc123", UID: types.UID("rs/web-abc123"),
		OwnerReferences: []metav1.OwnerReference{rsRef}}}
	podRef := metav1.OwnerReference{Kind: "ReplicaSet", Name: "web-abc123",
		UID: types.UID("rs/web-abc123"), Controller: ptr(true)}
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("big", "r6i.large"), node("small", "m6i.large")},
		ReplicaSets: []appsv1.ReplicaSet{rs},
		Pods: []corev1.Pod{
			runningPod("t", "web-abc123-1", "big", podRef),
			runningPod("t", "web-abc123-2", "big", podRef), // 2 on r6i → modal r6i
			runningPod("t", "web-abc123-3", "small", podRef),
		},
		Deployments: []appsv1.Deployment{deploy("t", "web", 3, "1", "1Gi")},
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	// r6i.large cpuHour is 0.0165; if it fell back to cluster-modal (m6i, 0.0210) this would differ
	require.InDelta(t, 3*0.0165*730, cs.Workloads[0].MonthlyCPUCost, 1e-3)
}

// TestEstimateSanitizesNonFiniteRequest locks C1: a container requesting an
// absurd-but-syntactically-valid quantity (resource.MustParse("1e400") ==
// +Inf, verified empirically) must not produce a CostSet that fails to
// json.Marshal. Estimate() alone may still surface +Inf; Collect() is the
// boundary that must sanitize it before it reaches Evidence.Value or
// report.Meta.Cost.
func TestEstimateSanitizesNonFiniteRequest(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "p", "n", ctrlRef("Deployment", "t", "huge"))},
		Deployments: []appsv1.Deployment{deploy("t", "huge", 1, "1", "1e400")}, // absurd memory request
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	require.True(t, math.IsInf(cs.Workloads[0].MonthlyMemCost, 1), "Estimate alone is expected to still surface +Inf")

	spec := config.ConnectorSpec{EstimateFallback: ptr(true)}
	collected := Collect(context.Background(), snap, connector.NewRegistry(), spec)
	require.NotNil(t, collected)
	b, err := json.Marshal(collected) // this is the assertion that matters: must not error
	require.NoError(t, err)
	require.NotContains(t, string(b), "Inf")
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
