package cost

import (
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
		Summary: "requests are more than 2x observed usage — lower them toward usage to reclaim spend",
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

	sev := analyzer.SeverityInfo
	if wl.MonthlyCost >= 20 {
		sev = analyzer.SeverityWarning
	}
	hasHPA := hpa[key(wl)]

	return analyzer.Finding{
		RuleID:   "cost/idle",
		Domain:   "cost",
		Severity: sev,
		Title:    "Workload is idle",
		Object:   analyzer.ObjectRef{Kind: wl.Kind, Namespace: wl.Namespace, Name: wl.Name},
		Evidence: []analyzer.Evidence{
			{Source: "cost", Query: "cpuUsageCores", Value: wl.CPUUsageCores},
			{Source: "cost", Query: "replicas", Value: wl.Replicas},
			{Source: "cost", Query: "monthlyCost", Value: wl.MonthlyCost},
			{Source: "cost", Query: "estimatedMonthlySaving", Value: wl.MonthlyCost},
			{Source: "cost", Query: "hasHPA", Value: hasHPA},
			{Source: "cost", Query: "basis", Value: string(basis)},
		},
		Summary: "per-pod CPU usage is near zero — scale to zero or remove the workload to free its full cost",
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
		Summary: "replicas sit well below 20% per-pod CPU utilization with no HPA — cut the replica count",
	}, true
}
