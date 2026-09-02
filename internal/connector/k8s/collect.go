package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

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
	}

	snap.Meta = snapshot.Meta{
		CollectedAt: time.Now(),
		Lookback:    c.scope.Lookback,
		Context:     c.ctxName,
		Namespaces:  namespaces,
	}
	return snap, nil
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
