package cost

import (
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// checkOrphanedPVC flags a bound PVC, older than a week, that no pod mounts
// (spec cost/orphaned-pvc). snap.PVCs is iterated in a (namespace, name)-sorted
// copy — collection order must not leak into the output.
func checkOrphanedPVC(snap *snapshot.Snapshot, basis snapshot.CostBasis) []analyzer.Finding {
	referenced := map[string]bool{}
	for i := range snap.Pods {
		for _, v := range snap.Pods[i].Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				referenced[v.PersistentVolumeClaim.ClaimName] = true
			}
		}
	}

	pvcs := make([]corev1.PersistentVolumeClaim, len(snap.PVCs))
	copy(pvcs, snap.PVCs)
	sort.Slice(pvcs, func(i, j int) bool {
		if pvcs[i].Namespace != pvcs[j].Namespace {
			return pvcs[i].Namespace < pvcs[j].Namespace
		}
		return pvcs[i].Name < pvcs[j].Name
	})

	var out []analyzer.Finding
	for i := range pvcs {
		pvc := pvcs[i]
		if pvc.Status.Phase != corev1.ClaimBound || referenced[pvc.Name] {
			continue
		}
		age := time.Since(pvc.CreationTimestamp.Time)
		if age <= 7*24*time.Hour {
			continue
		}

		q := pvc.Status.Capacity[corev1.ResourceStorage]
		capacityBytes := q.AsApproximateFloat64()
		storageClass := ""
		if pvc.Spec.StorageClassName != nil {
			storageClass = *pvc.Spec.StorageClassName
		}
		ageDays := age.Hours() / 24
		estimatedMonthlyCost := capacityBytes / (1 << 30) * 0.10

		out = append(out, analyzer.Finding{
			RuleID:   "cost/orphaned-pvc",
			Domain:   "cost",
			Severity: analyzer.SeverityWarning,
			Title:    "PersistentVolumeClaim is orphaned",
			Object: analyzer.ObjectRef{
				APIVersion: "v1", Kind: "PersistentVolumeClaim",
				Namespace: pvc.Namespace, Name: pvc.Name,
			},
			Evidence: []analyzer.Evidence{
				{Source: "cost", Query: "capacityBytes", Value: capacityBytes},
				{Source: "cost", Query: "storageClass", Value: storageClass},
				{Source: "cost", Query: "ageDays", Value: ageDays},
				{Source: "cost", Query: "estimatedMonthlyCost", Value: estimatedMonthlyCost},
				{Source: "cost", Query: "basis", Value: string(basis)},
			},
			Summary: "a bound PVC older than 7 days is mounted by no pod — delete it to stop paying for the volume",
		})
	}
	return out
}

// checkOrphanedLB flags a LoadBalancer Service that has a selector but no ready
// pod behind it (spec cost/orphaned-lb). A Service with an empty selector is
// externally managed and never flagged.
func checkOrphanedLB(snap *snapshot.Snapshot, basis snapshot.CostBasis) []analyzer.Finding {
	svcs := make([]corev1.Service, len(snap.Services))
	copy(svcs, snap.Services)
	sort.Slice(svcs, func(i, j int) bool {
		if svcs[i].Namespace != svcs[j].Namespace {
			return svcs[i].Namespace < svcs[j].Namespace
		}
		return svcs[i].Name < svcs[j].Name
	})

	var out []analyzer.Finding
	for i := range svcs {
		svc := svcs[i]
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || len(svc.Spec.Selector) == 0 {
			continue
		}
		if lbHasReadyPod(snap, svc) {
			continue
		}

		ageDays := time.Since(svc.CreationTimestamp.Time).Hours() / 24
		out = append(out, analyzer.Finding{
			RuleID:   "cost/orphaned-lb",
			Domain:   "cost",
			Severity: analyzer.SeverityWarning,
			Title:    "LoadBalancer Service has no ready backends",
			Object: analyzer.ObjectRef{
				APIVersion: "v1", Kind: "Service",
				Namespace: svc.Namespace, Name: svc.Name,
			},
			Evidence: []analyzer.Evidence{
				{Source: "cost", Query: "ageDays", Value: ageDays},
				{Source: "cost", Query: "estimatedMonthlyCost", Value: 18.00},
				{Source: "cost", Query: "basis", Value: string(basis)},
			},
			Summary: "a LoadBalancer Service with a selector has no ready pods — the cloud load balancer is billed with nothing behind it",
		})
	}
	return out
}

func lbHasReadyPod(snap *snapshot.Snapshot, svc corev1.Service) bool {
	for i := range snap.Pods {
		pod := &snap.Pods[i]
		if pod.Namespace != svc.Namespace || !labelsSuperset(pod.Labels, svc.Spec.Selector) {
			continue
		}
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
				return true
			}
		}
	}
	return false
}

// labelsSuperset reports whether labels contains every key/value pair in sel.
func labelsSuperset(labels, sel map[string]string) bool {
	for k, v := range sel {
		if labels[k] != v {
			return false
		}
	}
	return true
}

// checkRetainedJobs flags, per namespace, the presence of succeeded Jobs older
// than a week that carry no TTL (spec cost/retained-jobs). Returns nil when the
// Jobs collection is absent (R3 — absent, not an error).
func checkRetainedJobs(snap *snapshot.Snapshot) []analyzer.Finding {
	if snap.Jobs == nil {
		return nil
	}

	type agg struct {
		count      int
		oldestDone time.Time
	}
	byNS := map[string]*agg{}
	for i := range snap.Jobs {
		job := &snap.Jobs[i]
		if job.Status.Succeeded <= 0 || job.Status.CompletionTime == nil {
			continue
		}
		if job.Spec.TTLSecondsAfterFinished != nil {
			continue
		}
		done := job.Status.CompletionTime.Time
		if time.Since(done) <= 7*24*time.Hour {
			continue
		}
		a := byNS[job.Namespace]
		if a == nil {
			a = &agg{}
			byNS[job.Namespace] = a
		}
		a.count++
		if a.oldestDone.IsZero() || done.Before(a.oldestDone) {
			a.oldestDone = done
		}
	}

	nss := make([]string, 0, len(byNS))
	for ns := range byNS {
		nss = append(nss, ns)
	}
	sort.Strings(nss)

	var out []analyzer.Finding
	for _, ns := range nss {
		a := byNS[ns]
		if a.count < 1 {
			continue
		}
		out = append(out, analyzer.Finding{
			RuleID:   "cost/retained-jobs",
			Domain:   "cost",
			Severity: analyzer.SeverityInfo,
			Title:    "Completed Jobs are being retained",
			Object:   analyzer.ObjectRef{Kind: "Namespace", Name: ns},
			Evidence: []analyzer.Evidence{
				{Source: "cost", Query: "count", Value: a.count},
				{Source: "cost", Query: "oldestCompletionDays", Value: time.Since(a.oldestDone).Hours() / 24},
			},
			Summary: "succeeded Jobs older than 7 days carry no ttlSecondsAfterFinished — set it so the controller reaps them",
		})
	}
	return out
}

// checkNamespaceSpendTrend flags a namespace whose measured month-over-month
// cost jumped sharply (spec R1). Runs on the measured path only; the estimate
// path (PriorMonthlyCost < 0) is skipped, and deltaPct is emitted only when it
// is finite.
func checkNamespaceSpendTrend(cs *snapshot.CostSet) []analyzer.Finding {
	var out []analyzer.Finding
	for _, nc := range cs.Namespaces {
		if nc.PriorMonthlyCost < 0 {
			continue
		}
		delta := nc.MonthlyCost - nc.PriorMonthlyCost
		if delta < 50 {
			continue
		}
		if !(nc.PriorMonthlyCost == 0 || nc.MonthlyCost > nc.PriorMonthlyCost*1.25) {
			continue
		}

		sev := analyzer.SeverityInfo
		if delta >= 200 {
			sev = analyzer.SeverityWarning
		}

		ev := []analyzer.Evidence{
			{Source: "cost", Query: "monthlyCost", Value: nc.MonthlyCost},
			{Source: "cost", Query: "priorMonthlyCost", Value: nc.PriorMonthlyCost},
			{Source: "cost", Query: "deltaAbs", Value: delta},
		}
		if pct, ok := safeRatio(delta, nc.PriorMonthlyCost); ok {
			ev = append(ev, analyzer.Evidence{Source: "cost", Query: "deltaPct", Value: pct})
		}
		ev = append(ev, analyzer.Evidence{Source: "cost", Query: "basis", Value: "measured"})

		out = append(out, analyzer.Finding{
			RuleID:   "cost/namespace-spend-trend",
			Domain:   "cost",
			Severity: sev,
			Title:    "Namespace spend is trending up",
			Object:   analyzer.ObjectRef{Kind: "Namespace", Name: nc.Namespace},
			Evidence: ev,
			Summary:  "month-over-month namespace cost jumped sharply — check for a recent scale-up or a runaway workload",
		})
	}
	return out
}
