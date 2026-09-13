package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

// Collect lists the read-only cluster state within the connector's scope and
// returns it as a Snapshot. Any list error is wrapped and returned, failing the
// whole collection; the orchestrator decides how to proceed.
func (c *Connector) Collect(ctx context.Context) (*snapshot.Snapshot, error) {
	snap := &snapshot.Snapshot{}

	nodes, err := c.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	snap.Nodes = nodes.Items

	namespaces, err := c.targetNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces {
		deploys, err := c.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list deployments in %s: %w", ns, err)
		}
		snap.Deployments = append(snap.Deployments, deploys.Items...)

		sts, err := c.cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list statefulsets in %s: %w", ns, err)
		}
		snap.StatefulSets = append(snap.StatefulSets, sts.Items...)

		ds, err := c.cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list daemonsets in %s: %w", ns, err)
		}
		snap.DaemonSets = append(snap.DaemonSets, ds.Items...)

		rs, err := c.cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list replicasets in %s: %w", ns, err)
		}
		snap.ReplicaSets = append(snap.ReplicaSets, rs.Items...)

		pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pods in %s: %w", ns, err)
		}
		snap.Pods = append(snap.Pods, pods.Items...)

		events, err := c.cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list events in %s: %w", ns, err)
		}
		snap.Events = append(snap.Events, events.Items...)

		pdbs, err := c.cs.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pdbs in %s: %w", ns, err)
		}
		snap.PDBs = append(snap.PDBs, pdbs.Items...)

		hpas, err := c.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list hpas in %s: %w", ns, err)
		}
		snap.HPAs = append(snap.HPAs, hpas.Items...)

		pvcs, err := c.cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pvcs in %s: %w", ns, err)
		}
		snap.PVCs = append(snap.PVCs, pvcs.Items...)

		svcs, err := c.cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list services in %s: %w", ns, err)
		}
		snap.Services = append(snap.Services, svcs.Items...)

		jobs, jerr := c.cs.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
		if jerr == nil {
			snap.Jobs = append(snap.Jobs, jobs.Items...)
		}
	}

	c.collectFlaggedPodLogs(ctx, snap)

	snap.Meta = snapshot.Meta{
		CollectedAt: time.Now(),
		Lookback:    c.scope.Lookback,
		Context:     c.ctxName,
		Namespaces:  namespaces,
	}
	return snap, nil
}

// flaggedPodLogCap bounds how many flagged pods the collect phase pulls logs
// for, so a badly broken cluster cannot make collection unbounded.
const flaggedPodLogCap = 20

// flaggedWaitingReasons are container "Waiting" reasons that mark a pod as
// unhealthy enough to capture recent logs for during collection.
var flaggedWaitingReasons = map[string]bool{
	"CrashLoopBackOff": true,
	"ImagePullBackOff": true,
	"ErrImagePull":     true,
}

// collectFlaggedPodLogs pulls recent current and previous logs for up to
// flaggedPodLogCap pods that the reliability rules would flag, in deterministic
// order (namespace, then name), appending every non-empty result to
// snap.PodLogs. It is best-effort: a podLogs error for any pod/container is
// skipped silently — collection stays quiet and never fails here. The map key
// is "Pod/<namespace>/<name>", which equals
// analyzer.ObjectRef{Kind:"Pod",Namespace,Name}.String() (snapshot is a leaf
// package, so T6 relies on this string form rather than importing analyzer).
// snap.PodLogs stays nil unless at least one chunk is produced.
func (c *Connector) collectFlaggedPodLogs(ctx context.Context, snap *snapshot.Snapshot) {
	var flagged []*corev1.Pod
	for i := range snap.Pods {
		if podIsFlagged(&snap.Pods[i]) {
			flagged = append(flagged, &snap.Pods[i])
		}
	}
	sort.Slice(flagged, func(a, b int) bool {
		if flagged[a].Namespace != flagged[b].Namespace {
			return flagged[a].Namespace < flagged[b].Namespace
		}
		return flagged[a].Name < flagged[b].Name
	})
	if len(flagged) > flaggedPodLogCap {
		flagged = flagged[:flaggedPodLogCap]
	}

	for _, pod := range flagged {
		key := "Pod/" + pod.Namespace + "/" + pod.Name
		for _, container := range flaggedPodContainers(pod) {
			for _, previous := range []bool{false, true} {
				lines, err := c.podLogs(ctx, pod.Namespace, pod.Name, container, previous, 100)
				if err != nil || lines == "" {
					continue
				}
				if snap.PodLogs == nil {
					snap.PodLogs = make(map[string][]snapshot.LogChunk)
				}
				snap.PodLogs[key] = append(snap.PodLogs[key], snapshot.LogChunk{
					Container: container,
					Previous:  previous,
					Lines:     lines,
				})
			}
		}
	}
}

// podIsFlagged reports whether the reliability rules would flag this pod, so
// the collect phase should pull its recent logs. A pod is flagged when any
// container status is Waiting with a reason in flaggedWaitingReasons, or was
// last terminated with reason "OOMKilled", or the pod is Running with a Ready
// condition whose status is not "True".
func podIsFlagged(pod *corev1.Pod) bool {
	for _, cs := range pod.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && flaggedWaitingReasons[w.Reason] {
			return true
		}
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			return true
		}
	}
	if pod.Status.Phase == corev1.PodRunning {
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status != corev1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

// flaggedPodContainers returns the container names to pull logs for: those
// currently Waiting or carrying a LastTerminationState.Terminated, else the
// first container in the pod spec.
func flaggedPodContainers(pod *corev1.Pod) []string {
	var names []string
	seen := map[string]bool{}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting == nil && cs.LastTerminationState.Terminated == nil {
			continue
		}
		if !seen[cs.Name] {
			seen[cs.Name] = true
			names = append(names, cs.Name)
		}
	}
	if len(names) == 0 && len(pod.Spec.Containers) > 0 {
		names = append(names, pod.Spec.Containers[0].Name)
	}
	return names
}

// targetNamespaces resolves the namespaces to inspect: the explicit scope list
// when set (sorted), otherwise every namespace in the cluster minus the
// configured exclusions (sorted).
func (c *Connector) targetNamespaces(ctx context.Context) ([]string, error) {
	if len(c.scope.Namespaces) > 0 {
		out := append([]string(nil), c.scope.Namespaces...)
		sort.Strings(out)
		return out, nil
	}
	nsList, err := c.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	excl := make(map[string]bool, len(c.scope.Exclude))
	for _, e := range c.scope.Exclude {
		excl[e] = true
	}
	out := []string{}
	for _, ns := range nsList.Items {
		if !excl[ns.Name] {
			out = append(out, ns.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}
