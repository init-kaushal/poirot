package change

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

var _ analyzer.Analyzer = (*Analyzer)(nil)

func (*Analyzer) ID() string         { return "change" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	var out []analyzer.Finding
	for _, rule := range []func(*snapshot.Snapshot) []analyzer.Finding{
		checkRecentRollout,
		checkRolloutStuck,
		checkReplicaSetChurn,
	} {
		out = append(out, rule(snap)...)
	}
	return out, nil
}
