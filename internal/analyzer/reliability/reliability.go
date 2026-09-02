package reliability

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) ID() string         { return "reliability" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	var out []analyzer.Finding
	podRules := []func(*snapshot.Snapshot) []analyzer.Finding{
		checkCrashLoop,
		checkImagePull,
		checkOOMKilled,
		checkHighRestarts,
		checkPending,
		checkEvicted,
		checkProbeFailing,
		checkNotReady,
	}
	for _, r := range podRules {
		out = append(out, r(snap)...)
	}
	simpleWorkloadRules := []func(*snapshot.Snapshot) []analyzer.Finding{
		checkSingleReplica,
		checkNoResourceLimits,
		checkNoProbes,
		checkNoPDB,
		checkLatestTag,
		checkHPAMaxed,
		checkNodePressure,
		checkPVCUnbound,
	}
	for _, r := range simpleWorkloadRules {
		out = append(out, r(snap)...)
	}
	out = append(out, checkWarningEventClusters(snap, out)...)
	return out, nil
}

func finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, ev ...analyzer.Evidence) analyzer.Finding {
	return analyzer.Finding{
		RuleID:   ruleID,
		Domain:   "reliability",
		Severity: sev,
		Title:    title,
		Object:   obj,
		Summary:  summary,
		Evidence: ev,
	}
}
