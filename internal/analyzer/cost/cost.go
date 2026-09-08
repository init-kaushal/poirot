package cost

import (
	"context"
	"math"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

var _ analyzer.Analyzer = (*Analyzer)(nil)

func (*Analyzer) ID() string         { return "cost" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	if snap.Cost == nil {
		return []analyzer.Finding{skippedFinding()}, nil
	}
	cs := snap.Cost
	hpa := hpaTargets(snap) // hoisted once
	var out []analyzer.Finding
	overReplicated := map[string]bool{}

	// Pass 1
	for _, wl := range cs.Workloads {
		if f, ok := checkOverReplicated(wl, cs.Basis, hpa); ok {
			out = append(out, f)
			overReplicated[key(wl)] = true
		}
	}
	for _, wl := range cs.Workloads {
		if f, ok := checkIdle(wl, cs.Basis, hpa); ok {
			out = append(out, f)
		}
	}
	// Pass 2
	anyUsage := false
	for _, wl := range cs.Workloads {
		if wl.CPUUsageCores >= 0 {
			anyUsage = true
			break
		}
	}
	if !anyUsage {
		out = append(out, rightsizingLimitedFinding())
	} else {
		for _, wl := range cs.Workloads {
			if overReplicated[key(wl)] {
				continue
			}
			if f, ok := checkRightsizing(wl, cs.Basis); ok {
				out = append(out, f)
			}
		}
	}
	// T12 appends the object rules here.
	return out, nil
}

func key(wl snapshot.WorkloadCost) string { return wl.Namespace + "|" + wl.Kind + "|" + wl.Name }

// safeRatio returns num/den, ok=false when den==0 or the result is non-finite (spec R1).
func safeRatio(num, den float64) (float64, bool) {
	if den == 0 {
		return 0, false
	}
	v := num / den
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func hpaTargets(snap *snapshot.Snapshot) map[string]bool {
	m := map[string]bool{}
	for i := range snap.HPAs {
		h := &snap.HPAs[i]
		m[h.Namespace+"|"+h.Spec.ScaleTargetRef.Kind+"|"+h.Spec.ScaleTargetRef.Name] = true
	}
	return m
}

func skippedFinding() analyzer.Finding {
	return analyzer.Finding{
		RuleID: "cost/skipped", Domain: "cost", Severity: analyzer.SeverityInfo,
		Title:   "Cost analysis skipped",
		Object:  analyzer.ObjectRef{Kind: "Cluster"},
		Summary: "cost analysis skipped — no cost data available (no OpenCost and estimate fallback disabled, or the price sheet failed to load)",
	}
}

func rightsizingLimitedFinding() analyzer.Finding {
	return analyzer.Finding{
		RuleID: "cost/rightsizing-limited", Domain: "cost", Severity: analyzer.SeverityInfo,
		Title:   "Rightsizing needs usage data",
		Object:  analyzer.ObjectRef{Kind: "Cluster"}, // spec R15 — explicit, never the zero value
		Summary: "rightsizing needs per-workload usage — deploy OpenCost or Prometheus",
	}
}
