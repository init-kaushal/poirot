package cost

import (
	"fmt"
	"math"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// checkRightsizing flags a workload whose CPU and/or memory requests dwarf its
// observed usage (per-dimension, spec R8).
func checkRightsizing(wl snapshot.WorkloadCost, basis snapshot.CostBasis) (analyzer.Finding, bool) {
	cpuElig := wl.CPURequestCores > 0 && wl.CPUUsageCores >= 0
	memElig := wl.MemRequestBytes > 0 && wl.MemUsageBytes >= 0
	cpuUtil, _ := safeRatio(wl.CPUUsageCores, wl.CPURequestCores)
	memUtil, _ := safeRatio(wl.MemUsageBytes, wl.MemRequestBytes)
	cpuOver := cpuElig && cpuUtil < 0.5
	memOver := memElig && memUtil < 0.5
	if (!cpuOver && !memOver) || wl.MonthlyCost < 5 {
		return analyzer.Finding{}, false
	}

	sugCPU := wl.CPURequestCores
	if cpuOver {
		sugCPU = math.Max(wl.CPUUsageCores*1.3, 0.010)
	}
	sugMem := wl.MemRequestBytes
	if memOver {
		sugMem = math.Max(wl.MemUsageBytes*1.3, 16<<20)
	}

	saving := 0.0
	if r, ok := safeRatio(sugCPU, wl.CPURequestCores); ok {
		saving += wl.MonthlyCPUCost * clamp01(1-r)
	}
	if r, ok := safeRatio(sugMem, wl.MemRequestBytes); ok {
		saving += wl.MonthlyMemCost * clamp01(1-r)
	}

	sev := analyzer.SeverityInfo
	if saving >= 20 {
		sev = analyzer.SeverityWarning
	}

	return analyzer.Finding{
		RuleID:   "cost/rightsizing",
		Domain:   "cost",
		Severity: sev,
		Title:    "Workload requests exceed usage",
		Object:   analyzer.ObjectRef{Kind: wl.Kind, Namespace: wl.Namespace, Name: wl.Name},
		Evidence: []analyzer.Evidence{
			{Source: "cost", Query: "cpuRequestCores", Value: wl.CPURequestCores},
			{Source: "cost", Query: "cpuUsageCores", Value: wl.CPUUsageCores},
			{Source: "cost", Query: "memRequestBytes", Value: wl.MemRequestBytes},
			{Source: "cost", Query: "memUsageBytes", Value: wl.MemUsageBytes},
			{Source: "cost", Query: "suggestedCpuRequest", Value: sugCPU},
			{Source: "cost", Query: "suggestedMemRequest", Value: sugMem},
			{Source: "cost", Query: "estimatedMonthlySaving", Value: saving},
			{Source: "cost", Query: "basis", Value: string(basis)},
		},
		Summary: fmt.Sprintf("Set requests to cpu=%dm mem=%dMi (usage ×1.3); saves ~$%.2f/mo",
			int(sugCPU*1000), int(sugMem/(1<<20)), saving),
	}, true
}

// checkIdle flags a workload that is running replicas but doing almost no work.
func checkIdle(wl snapshot.WorkloadCost, basis snapshot.CostBasis, hpa map[string]bool) (analyzer.Finding, bool) {
	if wl.Kind == "DaemonSet" || wl.Replicas < 1 || wl.CPUUsageCores < 0 || wl.MonthlyCost < 5 {
		return analyzer.Finding{}, false
	}
	perPod, _ := safeRatio(wl.CPUUsageCores, float64(wl.Replicas))
	if perPod >= 0.005 {
		return analyzer.Finding{}, false
	}

	// I3: there is no traffic signal in M4 (no request-rate join), so this rule
	// cannot actually distinguish "idle" from "I/O-bound and quiet on CPU" —
	// cap severity at Info and hedge the language rather than claim certainty
	// the data doesn't support.
	hasHPA := hpa[key(wl)]

	return analyzer.Finding{
		RuleID:   "cost/idle",
		Domain:   "cost",
		Severity: analyzer.SeverityInfo,
		Title:    "Workload has very low CPU usage",
		Object:   analyzer.ObjectRef{Kind: wl.Kind, Namespace: wl.Namespace, Name: wl.Name},
		Evidence: []analyzer.Evidence{
			{Source: "cost", Query: "cpuUsageCores", Value: wl.CPUUsageCores},
			{Source: "cost", Query: "replicas", Value: wl.Replicas},
			{Source: "cost", Query: "monthlyCost", Value: wl.MonthlyCost},
			{Source: "cost", Query: "estimatedMonthlySaving", Value: wl.MonthlyCost},
			{Source: "cost", Query: "hasHPA", Value: hasHPA},
			{Source: "cost", Query: "basis", Value: string(basis)},
		},
		Summary: fmt.Sprintf("Per-pod CPU usage is near zero (~%dm) — scale to zero or delete if truly unused; verify first since I/O-bound services can look idle by CPU alone. Frees ~$%.2f/mo.",
			int(wl.CPUUsageCores/float64(wl.Replicas)*1000), wl.MonthlyCost),
	}, true
}

// checkOverReplicated flags a wide Deployment/StatefulSet whose replicas are
// mostly idle and that has no HPA driving the count.
func checkOverReplicated(wl snapshot.WorkloadCost, basis snapshot.CostBasis, hpa map[string]bool) (analyzer.Finding, bool) {
	if wl.Kind != "Deployment" && wl.Kind != "StatefulSet" {
		return analyzer.Finding{}, false
	}
	if wl.Replicas < 4 || hpa[key(wl)] || wl.CPUUsageCores < 0 || wl.MonthlyCost < 10 {
		return analyzer.Finding{}, false
	}
	perPodReq, ok := safeRatio(wl.CPURequestCores, float64(wl.Replicas))
	if !ok {
		return analyzer.Finding{}, false
	}
	perPodUse, _ := safeRatio(wl.CPUUsageCores, float64(wl.Replicas))
	if perPodUse >= 0.2*perPodReq {
		return analyzer.Finding{}, false
	}

	sug := int32(math.Ceil(wl.CPUUsageCores / (perPodReq * 0.6)))
	if sug < 2 {
		sug = 2
	}
	ratio, _ := safeRatio(float64(sug), float64(wl.Replicas))
	saving := wl.MonthlyCost * clamp01(1-ratio)
	perPodUtil, _ := safeRatio(perPodUse, perPodReq)

	sev := analyzer.SeverityInfo
	if saving >= 20 {
		sev = analyzer.SeverityWarning
	}

	return analyzer.Finding{
		RuleID:   "cost/over-replicated",
		Domain:   "cost",
		Severity: sev,
		Title:    "Workload is over-replicated",
		Object:   analyzer.ObjectRef{Kind: wl.Kind, Namespace: wl.Namespace, Name: wl.Name},
		Evidence: []analyzer.Evidence{
			{Source: "cost", Query: "replicas", Value: wl.Replicas},
			{Source: "cost", Query: "suggestedReplicas", Value: sug},
			{Source: "cost", Query: "perPodCpuUtil", Value: perPodUtil},
			{Source: "cost", Query: "estimatedMonthlySaving", Value: saving},
			{Source: "cost", Query: "basis", Value: string(basis)},
		},
		Summary: fmt.Sprintf("Reduce replicas from %d to %d (or add an HPA); saves ~$%.2f/mo", wl.Replicas, sug, saving),
	}, true
}
