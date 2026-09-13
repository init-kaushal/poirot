package cost

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const hoursPerMonth = 730.0

const (
	noInstanceTypeNote    = "no instance-type labels found; using blended default rate"
	usageUnavailableNote  = "usage unavailable — rightsizing limited to requests vs limits"
	instanceTypeLabel     = "node.kubernetes.io/instance-type"
	instanceTypeLabelBeta = "beta.kubernetes.io/instance-type"
)

// Two-hop PromQL: label_replace resolves the ReplicaSet-hash suffix to the bare
// Deployment name server-side, so the returned `workload` label already carries
// the Deployment/StatefulSet/DaemonSet name and the client-side join is a plain
// map lookup on (namespace, workload).
const cpuExpr = `sum by (namespace, workload) (
  rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[7d])
  * on (namespace, pod) group_left(workload)
  label_replace(
    label_replace(kube_pod_owner{owner_kind="ReplicaSet"}, "rs", "$1", "owner_name", "(.*)"),
    "workload", "$1", "rs", "^(.*)-[0-9a-f]{6,10}$")
)
or
sum by (namespace, workload) (
  rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[7d])
  * on (namespace, pod) group_left(workload)
  label_replace(kube_pod_owner{owner_kind=~"StatefulSet|DaemonSet"}, "workload", "$1", "owner_name", "(.*)")
)`

const memExpr = `sum by (namespace, workload) (
  container_memory_working_set_bytes{container!="",container!="POD"}
  * on (namespace, pod) group_left(workload)
  label_replace(
    label_replace(kube_pod_owner{owner_kind="ReplicaSet"}, "rs", "$1", "owner_name", "(.*)"),
    "workload", "$1", "rs", "^(.*)-[0-9a-f]{6,10}$")
)
or
sum by (namespace, workload) (
  container_memory_working_set_bytes{container!="",container!="POD"}
  * on (namespace, pod) group_left(workload)
  label_replace(kube_pod_owner{owner_kind=~"StatefulSet|DaemonSet"}, "workload", "$1", "owner_name", "(.*)")
)`

// Estimate builds an "estimated"-basis CostSet from pod requests × sheet.
// reg is used only to fetch the "promql" connector for usage; a nil registry
// or absent promql yields CPUUsageCores/MemUsageBytes = -1.
func Estimate(ctx context.Context, snap *snapshot.Snapshot, reg *connector.Registry, sheet *Sheet) *snapshot.CostSet {
	// node name -> instance type; and cluster-wide instance-type frequency.
	nodeIT := make(map[string]string, len(snap.Nodes))
	nodeCounts := map[string]int{}
	for i := range snap.Nodes {
		it := instanceTypeOf(&snap.Nodes[i])
		nodeIT[snap.Nodes[i].Name] = it
		if it != "" {
			nodeCounts[it]++
		}
	}
	clusterModal := modalString(nodeCounts)

	specs := collectWorkloadSpecs(snap)
	workloads := make([]snapshot.WorkloadCost, 0, len(specs))
	missingInstanceType := false

	for _, sp := range specs {
		running := runningPodsOf(snap, sp.kind, sp.uid)

		var replicas int32
		switch {
		case sp.kind == "DaemonSet":
			replicas = int32(len(running)) // R11: never node count
		case sp.replicas != nil:
			replicas = *sp.replicas
		default:
			replicas = 1
		}

		var cpuPerReplica, memPerReplica float64
		for _, c := range sp.containers {
			q := c.Resources.Requests[corev1.ResourceCPU]
			cpuPerReplica += q.AsApproximateFloat64()
			m := c.Resources.Requests[corev1.ResourceMemory]
			memPerReplica += m.AsApproximateFloat64()
		}
		cpuReq := cpuPerReplica * float64(replicas)
		memReq := memPerReplica * float64(replicas)

		it := workloadInstanceType(running, nodeIT, clusterModal)
		if it == "" {
			missingInstanceType = true
		}
		rate := sheet.Rate(it)
		cpuCost := cpuReq * rate.CPUHour * hoursPerMonth
		memCost := memReq / (1 << 30) * rate.MemGiBHour * hoursPerMonth

		workloads = append(workloads, snapshot.WorkloadCost{
			Namespace:       sp.ns,
			Kind:            sp.kind,
			Name:            sp.name,
			Replicas:        replicas,
			CPURequestCores: cpuReq,
			MemRequestBytes: memReq,
			MonthlyCost:     cpuCost + memCost,
			MonthlyCPUCost:  cpuCost,
			MonthlyMemCost:  memCost,
			// "unknown" until a PromQL sample raises it; fillUsage only ever
			// lifts a matched workload to its real value, so an unmatched one
			// must not read as measured-as-zero.
			CPUUsageCores: -1,
			MemUsageBytes: -1,
		})
	}

	sort.Slice(workloads, func(i, j int) bool {
		a, b := workloads[i], workloads[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})

	// Usage: only when promql is registered AND probed available.
	usageOK := false
	if reg != nil && reg.Satisfied([]string{"promql"}) {
		if pc, ok := reg.Get("promql"); ok {
			usageOK = fillUsage(ctx, pc, workloads)
		}
	}
	if !usageOK {
		for i := range workloads {
			workloads[i].CPUUsageCores = -1
			workloads[i].MemUsageBytes = -1
		}
	}

	// Namespaces: one per distinct namespace, in sorted order.
	nsCost := map[string]float64{}
	var nsOrder []string
	for _, w := range workloads {
		if _, seen := nsCost[w.Namespace]; !seen {
			nsOrder = append(nsOrder, w.Namespace)
		}
		nsCost[w.Namespace] += w.MonthlyCost
	}
	sort.Strings(nsOrder)
	namespaces := make([]snapshot.NamespaceCost, 0, len(nsOrder))
	for _, ns := range nsOrder {
		namespaces = append(namespaces, snapshot.NamespaceCost{
			Namespace:        ns,
			MonthlyCost:      nsCost[ns],
			PriorMonthlyCost: -1,
		})
	}

	// Note: instance-type note (if any) then usage note (if any), joined "; ".
	var parts []string
	if missingInstanceType {
		parts = append(parts, noInstanceTypeNote)
	}
	if !usageOK {
		parts = append(parts, usageUnavailableNote)
	}

	return &snapshot.CostSet{
		Basis:       snapshot.CostEstimated,
		Source:      "estimate",
		Currency:    "USD",
		Window:      "7d",
		CollectedAt: snap.Meta.CollectedAt,
		Workloads:   workloads,
		Namespaces:  namespaces,
		Note:        strings.Join(parts, "; "),
	}
}

// wlSpec is the kind-agnostic view of a workload used by the estimator.
type wlSpec struct {
	ns, kind, name, uid string
	replicas            *int32 // nil for DaemonSet
	containers          []corev1.Container
}

func collectWorkloadSpecs(snap *snapshot.Snapshot) []wlSpec {
	out := make([]wlSpec, 0, len(snap.Deployments)+len(snap.StatefulSets)+len(snap.DaemonSets))
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		out = append(out, wlSpec{
			ns: d.Namespace, kind: "Deployment", name: d.Name, uid: string(d.UID),
			replicas: d.Spec.Replicas, containers: d.Spec.Template.Spec.Containers,
		})
	}
	for i := range snap.StatefulSets {
		s := &snap.StatefulSets[i]
		out = append(out, wlSpec{
			ns: s.Namespace, kind: "StatefulSet", name: s.Name, uid: string(s.UID),
			replicas: s.Spec.Replicas, containers: s.Spec.Template.Spec.Containers,
		})
	}
	for i := range snap.DaemonSets {
		d := &snap.DaemonSets[i]
		out = append(out, wlSpec{
			ns: d.Namespace, kind: "DaemonSet", name: d.Name, uid: string(d.UID),
			replicas: nil, containers: d.Spec.Template.Spec.Containers,
		})
	}
	return out
}

// runningPodsOf returns the Running pods controlled by the workload uid.
// Deployment pods are owned by a ReplicaSet, not the Deployment, so for that
// kind we first collect the UIDs of ReplicaSets the Deployment controls and
// match pods against that set. StatefulSet/DaemonSet keep the direct-UID match.
func runningPodsOf(snap *snapshot.Snapshot, kind, uid string) []corev1.Pod {
	owners := map[string]bool{uid: true}
	if kind == "Deployment" {
		for i := range snap.ReplicaSets {
			rs := &snap.ReplicaSets[i]
			if c := metav1.GetControllerOf(rs); c != nil && string(c.UID) == uid {
				owners[string(rs.UID)] = true
			}
		}
	}
	var out []corev1.Pod
	for i := range snap.Pods {
		p := &snap.Pods[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		if c := metav1.GetControllerOf(p); c != nil && owners[string(c.UID)] {
			out = append(out, *p)
		}
	}
	return out
}

func instanceTypeOf(n *corev1.Node) string {
	if it := n.Labels[instanceTypeLabel]; it != "" {
		return it
	}
	return n.Labels[instanceTypeLabelBeta]
}

// workloadInstanceType picks the modal instance type over the workload's Running
// pods, falling back to the cluster-modal type when the workload has no Running
// pods on labelled nodes.
func workloadInstanceType(running []corev1.Pod, nodeIT map[string]string, clusterModal string) string {
	counts := map[string]int{}
	for _, p := range running {
		if it := nodeIT[p.Spec.NodeName]; it != "" {
			counts[it]++
		}
	}
	if len(counts) > 0 {
		return modalString(counts)
	}
	return clusterModal
}

// modalString returns the highest-count key, tie-broken by the lexicographically
// smallest key (R9 — never rely on Go map iteration order). Empty map -> "".
func modalString(counts map[string]int) string {
	best := ""
	bestN := -1
	for k, n := range counts {
		if n > bestN || (n == bestN && k < best) {
			best, bestN = k, n
		}
	}
	return best
}

// fillUsage mutates workloads[i].CPUUsageCores / MemUsageBytes in place.
// Returns false on ANY error (or no promql) — caller then sets every usage
// field to -1 and appends the "usage unavailable ..." note.
func fillUsage(ctx context.Context, pc connector.Connector, workloads []snapshot.WorkloadCost) bool {
	if pc == nil {
		return false
	}
	// keyed on (namespace, name) only — a same-named Deployment+StatefulSet in one
	// namespace will collide; fixing this needs the promql exprs to also carry
	// a workload-kind label (deferred, needs live-Prometheus validation).
	byKey := make(map[string]int, len(workloads))
	for i := range workloads {
		byKey[workloads[i].Namespace+"\x00"+workloads[i].Name] = i
	}

	cpu, err := queryUsage(ctx, pc, cpuExpr)
	if err != nil {
		return false
	}
	mem, err := queryUsage(ctx, pc, memExpr)
	if err != nil {
		return false
	}

	for _, s := range cpu {
		if i, ok := byKey[s.Labels["namespace"]+"\x00"+s.Labels["workload"]]; ok {
			workloads[i].CPUUsageCores = s.Value
		}
	}
	for _, s := range mem {
		if i, ok := byKey[s.Labels["namespace"]+"\x00"+s.Labels["workload"]]; ok {
			workloads[i].MemUsageBytes = s.Value
		}
	}
	return true
}

func queryUsage(ctx context.Context, pc connector.Connector, expr string) ([]snapshot.MetricSample, error) {
	args, err := json.Marshal(map[string]string{"expr": expr})
	if err != nil {
		return nil, err
	}
	raw, err := pc.Query(ctx, "promql.instant", args)
	if err != nil {
		return nil, err
	}
	var out []snapshot.MetricSample
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
