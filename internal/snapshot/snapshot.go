package snapshot

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
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

// CostBasis is the method used to calculate cost.
type CostBasis string

const (
	CostMeasured  CostBasis = "measured"
	CostEstimated CostBasis = "estimated"
)

// CostSet holds cost analysis results for workloads and namespaces.
type CostSet struct {
	Basis       CostBasis       `json:"basis"`
	Source      string          `json:"source"`   // "opencost" | "estimate"
	Currency    string          `json:"currency"` // "USD"
	Window      string          `json:"window"`   // "7d"
	CollectedAt time.Time       `json:"collectedAt"`
	Workloads   []WorkloadCost  `json:"workloads"`  // sorted (namespace, kind, name)
	Namespaces  []NamespaceCost `json:"namespaces"` // sorted (namespace)
	Note        string          `json:"note,omitempty"`
}

// WorkloadCost holds cost data for a single workload.
type WorkloadCost struct {
	Namespace       string  `json:"namespace"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	Replicas        int32   `json:"replicas"`
	CPURequestCores float64 `json:"cpuRequestCores"`
	CPUUsageCores   float64 `json:"cpuUsageCores"` // -1 when unavailable
	MemRequestBytes float64 `json:"memRequestBytes"`
	MemUsageBytes   float64 `json:"memUsageBytes"` // -1 when unavailable
	MonthlyCost     float64 `json:"monthlyCost"`
	MonthlyCPUCost  float64 `json:"monthlyCpuCost"`
	MonthlyMemCost  float64 `json:"monthlyMemCost"`
}

// NamespaceCost holds cost data for a namespace.
type NamespaceCost struct {
	Namespace        string  `json:"namespace"`
	MonthlyCost      float64 `json:"monthlyCost"`
	PriorMonthlyCost float64 `json:"priorMonthlyCost"` // -1 on the estimate path
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
	Jobs         []batchv1.Job `json:"jobs,omitempty"`
	Metrics      *MetricSet
	// PodLogs holds recent log captures pulled during collection for pods the
	// reliability rules would flag. The key is "Pod/<namespace>/<name>", which
	// is identical to analyzer.ObjectRef{Kind:"Pod",Namespace,Name}.String();
	// snapshot is a leaf package and must not import analyzer, so the key is
	// built by string concatenation both here and in the k8s collect phase.
	PodLogs map[string][]LogChunk `json:"podLogs,omitempty"`
	Cost    *CostSet              `json:"cost,omitempty"`
}

// LogChunk is one pod-container log capture taken during collection.
type LogChunk struct {
	Container string `json:"container"`
	Previous  bool   `json:"previous"`
	Lines     string `json:"lines"`
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
