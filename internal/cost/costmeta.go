package cost

import (
	"encoding/json"
	"strings"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/report"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// From rolls a CostSet + the cost analyzer's findings into a report.CostMeta.
// nil cs -> nil.
func From(cs *snapshot.CostSet, findings []analyzer.Finding) *report.CostMeta {
	if cs == nil {
		return nil
	}
	m := &report.CostMeta{
		Basis:    string(cs.Basis),
		Currency: cs.Currency,
		Window:   cs.Window,
		Note:     cs.Note,
	}
	for _, w := range cs.Workloads {
		m.MonthlyTotal += w.MonthlyCost
	}
	for _, f := range findings {
		if !strings.HasPrefix(f.RuleID, "cost/") {
			continue
		}
		// Mirror markdown.go's costSavings fallback: prefer
		// estimatedMonthlySaving, else estimatedMonthlyCost (cost/orphaned-pvc
		// and cost/orphaned-lb only ever carry the latter) — otherwise the
		// headline waste total undercounts what the rendered table shows (I1).
		var saving float64
		var found bool
		for _, e := range f.Evidence {
			if e.Query == "estimatedMonthlySaving" {
				if v, ok := toFloat(e.Value); ok {
					saving, found = v, true
					break // prefer Saving; stop scanning this finding
				}
			}
		}
		if !found {
			for _, e := range f.Evidence {
				if e.Query == "estimatedMonthlyCost" {
					if v, ok := toFloat(e.Value); ok {
						saving, found = v, true
						break
					}
				}
			}
		}
		if found {
			m.EstimatedMonthlyWaste += saving
		}
	}
	return m
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
