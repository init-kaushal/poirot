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
}
