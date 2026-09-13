package cost

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func TestFromRollsUpWasteFromFindings(t *testing.T) {
	cs := &snapshot.CostSet{
		Basis: snapshot.CostEstimated, Currency: "USD", Window: "7d",
		Workloads: []snapshot.WorkloadCost{{MonthlyCost: 100}, {MonthlyCost: 50}},
		Note:      "estimated from requests",
	}
	findings := []analyzer.Finding{
		{RuleID: "cost/idle", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 40.0}}},
		{RuleID: "cost/rightsizing", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 12.5}}},
		{RuleID: "slo/x", Domain: "slo", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 999.0}}}, // ignored: not cost/*
	}
	m := From(cs, findings)
	require.Equal(t, "estimated", m.Basis)
	require.InDelta(t, 150.0, m.MonthlyTotal, 1e-9)
	require.InDelta(t, 52.5, m.EstimatedMonthlyWaste, 1e-9)
}

func TestFromNilSafe(t *testing.T) { require.Nil(t, From(nil, nil)) }

func TestFromFallsBackToEstimatedMonthlyCostForOrphanedFindings(t *testing.T) { // I1
	cs := &snapshot.CostSet{
		Basis: snapshot.CostEstimated, Currency: "USD", Window: "7d",
		Workloads: []snapshot.WorkloadCost{{MonthlyCost: 0}},
	}
	findings := []analyzer.Finding{
		{RuleID: "cost/idle", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 40.0}}},
		// cost/orphaned-lb-style: only estimatedMonthlyCost, no estimatedMonthlySaving.
		{RuleID: "cost/orphaned-lb", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlyCost", Value: 18.0}}},
	}
	m := From(cs, findings)
	require.InDelta(t, 58.0, m.EstimatedMonthlyWaste, 1e-9)
}
