package slo

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

var _ analyzer.Analyzer = (*Analyzer)(nil)

func (*Analyzer) ID() string         { return "slo" }
func (*Analyzer) Requires() []string { return []string{"promql"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	if snap.Metrics == nil {
		return nil, nil
	}
	m := snap.Metrics
	var out []analyzer.Finding
	for _, rule := range []func(*snapshot.MetricSet) []analyzer.Finding{
		checkCPUSaturation,
		checkMemSaturation,
		checkRestartRate,
		checkPodNotReady,
		checkTargetsDown,
		checkQueryErrors,
	} {
		out = append(out, rule(m)...)
	}
	return out, nil
}
