package reliability

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func workloadRef(kind, namespace, name string) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "apps/v1", Kind: kind, Namespace: namespace, Name: name}
}

type workload struct {
	kind     string
	name     string
	ns       string
	replicas int32
	selector map[string]string
	template corev1.PodTemplateSpec
}

func workloads(snap *snapshot.Snapshot) []workload {
	var out []workload
	for _, d := range snap.Deployments {
		out = append(out, workload{
			kind: "Deployment", name: d.Name, ns: d.Namespace,
			replicas: derefI32(d.Spec.Replicas), selector: selLabels(d.Spec.Selector), template: d.Spec.Template,
		})
	}
	for _, s := range snap.StatefulSets {
		out = append(out, workload{
			kind: "StatefulSet", name: s.Name, ns: s.Namespace,
			replicas: derefI32(s.Spec.Replicas), selector: selLabels(s.Spec.Selector), template: s.Spec.Template,
		})
	}
	return out
}

func daemonsetTemplates(snap *snapshot.Snapshot) []workload {
	var out []workload
	for _, d := range snap.DaemonSets {
		out = append(out, workload{kind: "DaemonSet", name: d.Name, ns: d.Namespace, template: d.Spec.Template})
	}
	return out
}

func derefI32(p *int32) int32 {
	if p == nil {
		return 1 // k8s default for Deployment/StatefulSet replicas
	}
	return *p
}

func selLabels(s *metav1.LabelSelector) map[string]string {
	if s == nil {
		return nil
	}
	return s.MatchLabels
}

func checkSingleReplica(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		if w.replicas == 1 {
			out = append(out, finding(
				"reliability/single-replica", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Workload runs a single replica",
				fmt.Sprintf("%s %s/%s has replicas=1; no redundancy for node loss or rollout", w.kind, w.ns, w.name),
				ev("workload.spec.replicas", 1, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoResourceLimits(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	all := append(workloads(snap), daemonsetTemplates(snap)...)
	for _, w := range all {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			_, hasCPU := c.Resources.Limits[corev1.ResourceCPU]
			_, hasMem := c.Resources.Limits[corev1.ResourceMemory]
			if !hasCPU && !hasMem {
				bad = append(bad, c.Name)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/no-limits", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Containers have no resource limits",
				fmt.Sprintf("%s %s/%s: containers without CPU/memory limits: %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].resources.limits", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoProbes(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			if c.ReadinessProbe == nil && c.LivenessProbe == nil {
				bad = append(bad, c.Name)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/no-probes", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Containers have no health probes",
				fmt.Sprintf("%s %s/%s: containers without readiness or liveness probes: %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].{readinessProbe,livenessProbe}", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoPDB(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		if w.replicas < 2 {
			continue
		}
		if hasMatchingPDB(snap, w) {
			continue
		}
		out = append(out, finding(
			"reliability/no-pdb", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
			"Multi-replica workload has no PodDisruptionBudget",
			fmt.Sprintf("%s %s/%s has %d replicas but no PDB; voluntary disruptions can take it fully down", w.kind, w.ns, w.name, w.replicas),
			ev("poddisruptionbudgets (namespace match)", w.replicas, snap.Meta.CollectedAt),
		))
	}
	return out
}

func hasMatchingPDB(snap *snapshot.Snapshot, w workload) bool {
	for _, pdb := range snap.PDBs {
		if pdb.Namespace != w.ns || pdb.Spec.Selector == nil {
			continue
		}
		match := len(pdb.Spec.Selector.MatchLabels) > 0
		for k, v := range pdb.Spec.Selector.MatchLabels {
			if w.selector[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func checkLatestTag(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	all := append(workloads(snap), daemonsetTemplates(snap)...)
	for _, w := range all {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			if isFloatingTag(c.Image) {
				bad = append(bad, c.Image)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/latest-tag", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Container uses a floating image tag",
				fmt.Sprintf("%s %s/%s uses non-pinned image(s): %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].image", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func isFloatingTag(image string) bool {
	ref := image
	if strings.LastIndex(ref, "@") >= 0 {
		return false // digest-pinned
	}
	slash := strings.LastIndex(ref, "/")
	lastPart := ref[slash+1:]
	colon := strings.LastIndex(lastPart, ":")
	if colon < 0 {
		return true // no tag => :latest
	}
	return lastPart[colon+1:] == "latest"
}

func checkHPAMaxed(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, h := range snap.HPAs {
		if h.Status.CurrentReplicas == h.Spec.MaxReplicas && h.Status.DesiredReplicas >= h.Spec.MaxReplicas {
			out = append(out, finding(
				"reliability/hpa-maxed", analyzer.SeverityWarning,
				analyzer.ObjectRef{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Namespace: h.Namespace, Name: h.Name},
				"HPA is pinned at maxReplicas",
				fmt.Sprintf("HPA %s/%s is at max (%d/%d, desired %d); it cannot scale out further", h.Namespace, h.Name, h.Status.CurrentReplicas, h.Spec.MaxReplicas, h.Status.DesiredReplicas),
				ev("hpa.status", map[string]any{"current": h.Status.CurrentReplicas, "desired": h.Status.DesiredReplicas, "max": h.Spec.MaxReplicas}, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNodePressure(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, n := range snap.Nodes {
		var tripped []string
		notReady := false
		for _, c := range n.Status.Conditions {
			switch c.Type {
			case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
				if c.Status == corev1.ConditionTrue {
					tripped = append(tripped, string(c.Type))
				}
			case corev1.NodeReady:
				if c.Status != corev1.ConditionTrue {
					notReady = true
					tripped = append(tripped, "NotReady")
				}
			}
		}
		if len(tripped) == 0 {
			continue
		}
		sev := analyzer.SeverityWarning
		if notReady {
			sev = analyzer.SeverityCritical
		}
		out = append(out, finding(
			"reliability/node-pressure", sev,
			analyzer.ObjectRef{Kind: "Node", Name: n.Name},
			"Node under pressure or not Ready",
			fmt.Sprintf("Node %s: %s", n.Name, strings.Join(tripped, ", ")),
			ev("node.status.conditions", tripped, snap.Meta.CollectedAt),
		))
	}
	return out
}

func checkPVCUnbound(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.PVCs {
		if p.Status.Phase != corev1.ClaimBound {
			out = append(out, finding(
				"reliability/pvc-unbound", analyzer.SeverityWarning,
				analyzer.ObjectRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: p.Namespace, Name: p.Name},
				"PersistentVolumeClaim is not Bound",
				fmt.Sprintf("PVC %s/%s is %s", p.Namespace, p.Name, p.Status.Phase),
				ev("pvc.status.phase", string(p.Status.Phase), snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkWarningEventClusters(snap *snapshot.Snapshot, existing []analyzer.Finding) []analyzer.Finding {
	cutoff := snap.Meta.CollectedAt.Add(-snap.Meta.Lookback)
	type agg struct {
		ref     analyzer.ObjectRef
		count   int32
		reasons map[string]bool
	}
	groups := map[string]*agg{}
	for _, e := range snap.Events {
		if e.Type != corev1.EventTypeWarning || lastSeen(e).Before(cutoff) {
			continue
		}
		ref := analyzer.ObjectRef{
			Kind:      e.InvolvedObject.Kind,
			Namespace: e.InvolvedObject.Namespace,
			Name:      e.InvolvedObject.Name,
		}
		key := ref.String()
		g := groups[key]
		if g == nil {
			g = &agg{ref: ref, reasons: map[string]bool{}}
			groups[key] = g
		}
		c := e.Count
		if c == 0 {
			c = 1
		}
		g.count += c
		g.reasons[e.Reason] = true
	}

	covered := map[string]bool{}
	for _, f := range existing {
		covered[f.Object.String()] = true
	}

	var out []analyzer.Finding
	for key, g := range groups {
		if g.count < 3 || covered[key] {
			continue
		}
		reasons := make([]string, 0, len(g.reasons))
		for r := range g.reasons {
			reasons = append(reasons, r)
		}
		out = append(out, finding(
			"reliability/warning-events", analyzer.SeverityInfo, g.ref,
			"Object has repeated Warning events",
			fmt.Sprintf("%s has %d Warning events in the lookback window (%s)", key, g.count, strings.Join(reasons, ", ")),
			ev("events[type=Warning] grouped by involvedObject", map[string]any{"eventCount": g.count, "reasons": reasons}, snap.Meta.CollectedAt),
		))
	}
	return out
}
