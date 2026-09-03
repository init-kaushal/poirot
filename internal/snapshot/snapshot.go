package snapshot

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// Meta describes what was collected and over what window.
type Meta struct {
	CollectedAt time.Time
	Lookback    time.Duration
	Context     string
	Namespaces  []string // effective scope; empty means all
}

// Snapshot is the immutable set of raw cluster objects handed from
// collection to analysis. Analyzers must not mutate it.
type Snapshot struct {
	Meta         Meta
	Deployments  []appsv1.Deployment
	StatefulSets []appsv1.StatefulSet
	DaemonSets   []appsv1.DaemonSet
	ReplicaSets  []appsv1.ReplicaSet
	Pods         []corev1.Pod
	Events       []corev1.Event
	Nodes        []corev1.Node
	PDBs         []policyv1.PodDisruptionBudget
	HPAs         []autoscalingv2.HorizontalPodAutoscaler
	PVCs         []corev1.PersistentVolumeClaim
	Services     []corev1.Service
	Metrics      *MetricSet
}

// MetricSample is one time series' current value with its label set.
type MetricSample struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// MetricResult is the outcome of one query-pack entry. Error is set (and
// Samples empty) when that single query failed; a failed entry never aborts
// the collect.
type MetricResult struct {
	Name    string         `json:"name"`
	Expr    string         `json:"expr"`
	Samples []MetricSample `json:"samples"`
	Error   string         `json:"error,omitempty"`
}

// MetricSet is the fixed query pack's results, collected once per run.
type MetricSet struct {
	Backend     string         `json:"backend"`
	CollectedAt time.Time      `json:"collectedAt"`
	Results     []MetricResult `json:"results"`
}

// Result returns the pack entry by name, or nil. Safe on a nil receiver.
func (m *MetricSet) Result(name string) *MetricResult {
	if m == nil {
		return nil
	}
	for i := range m.Results {
		if m.Results[i].Name == name {
			return &m.Results[i]
		}
	}
	return nil
}
