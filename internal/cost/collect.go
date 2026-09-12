package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	ocpkg "github.com/init-kaushal/poirot/internal/connector/opencost"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// daysPerWeek scales a 7-day OpenCost window up to a 30-day month.
const monthOverWeek = 30.0 / 7.0

// Collect returns the best available CostSet, or nil (=> analyzer emits
// cost/skipped).
//
//  1. opencost available            -> measured (OpenCost allocation)
//  2. else *spec.EstimateFallback && sheet loads -> estimated (Estimate)
//  3. else                          -> nil
//
// A partially-built measured set is never returned: if either OpenCost query
// errors, fails to unmarshal, or the step-count guard trips, everything built
// so far is discarded, the error text is carried into noteFromMeasured, and the
// fallback decision runs as if OpenCost were unavailable.
func Collect(ctx context.Context, snap *snapshot.Snapshot, reg *connector.Registry, spec config.ConnectorSpec) *snapshot.CostSet {
	var noteFromMeasured string

	if reg != nil && reg.Satisfied([]string{"opencost"}) {
		if oc, ok := reg.Get("opencost"); ok {
			if cs, err := collectMeasured(ctx, snap, oc); err == nil {
				return sanitize(cs)
			} else {
				noteFromMeasured = err.Error()
			}
		}
	}

	// Fallback decision.
	if spec.EstimateFallback != nil && !*spec.EstimateFallback {
		return nil
	}
	sheet, err := Load()
	if err != nil {
		return nil
	}
	sheet.SetOverride(rateFromConfig(spec.Rates))
	cs := Estimate(ctx, snap, reg, sheet)
	if cs != nil && noteFromMeasured != "" {
		cs.Note = joinNotes(noteFromMeasured, cs.Note)
	}
	return sanitize(cs)
}

// sanitize replaces any non-finite float in cs with 0 before it can reach
// Evidence.Value or report.Meta.Cost — a single malformed resource quantity
// (e.g. a container requesting "1e400") must never make the whole report
// unmarshalable. -1 sentinels (usage/prior-cost "unknown") are preserved.
func sanitize(cs *snapshot.CostSet) *snapshot.CostSet {
	if cs == nil {
		return cs
	}
	fin := func(f float64) float64 {
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return 0
		}
		return f
	}
	for i := range cs.Workloads {
		w := &cs.Workloads[i]
		w.CPURequestCores = fin(w.CPURequestCores)
		w.MemRequestBytes = fin(w.MemRequestBytes)
		w.MonthlyCost = fin(w.MonthlyCost)
		w.MonthlyCPUCost = fin(w.MonthlyCPUCost)
		w.MonthlyMemCost = fin(w.MonthlyMemCost)
		if w.CPUUsageCores != -1 {
			w.CPUUsageCores = fin(w.CPUUsageCores)
		}
		if w.MemUsageBytes != -1 {
			w.MemUsageBytes = fin(w.MemUsageBytes)
		}
	}
	for i := range cs.Namespaces {
		n := &cs.Namespaces[i]
		n.MonthlyCost = fin(n.MonthlyCost)
		if n.PriorMonthlyCost != -1 {
			n.PriorMonthlyCost = fin(n.PriorMonthlyCost)
		}
	}
	return cs
}

// collectMeasured builds a fully-measured CostSet from the opencost connector.
// A non-nil error means the caller must discard the result and fall through to
// the estimate decision (carrying err.Error() into the note).
func collectMeasured(ctx context.Context, snap *snapshot.Snapshot, oc connector.Connector) (*snapshot.CostSet, error) {
	// 7d: namespace,controller aggregate, accumulated to a single step.
	steps, err := queryAllocation(ctx, oc, map[string]any{
		"window":     "7d",
		"aggregate":  "namespace,controller",
		"accumulate": true,
	})
	if err != nil {
		return nil, err
	}
	if len(steps) < 1 {
		return nil, fmt.Errorf("opencost allocation (7d): expected at least 1 step, got %d", len(steps))
	}

	// 14d: namespace aggregate, two 7d steps -> prior vs recent.
	steps2, err := queryAllocation(ctx, oc, map[string]any{
		"window":     "14d",
		"aggregate":  "namespace",
		"accumulate": false,
		"step":       "7d",
	})
	if err != nil {
		return nil, err
	}
	var prior, recent []ocpkg.Allocation
	var trendNote string
	if len(steps2) >= 2 {
		prior, recent = steps2[0], steps2[1]
	} else {
		trendNote = "namespace spend trend unavailable — OpenCost returned insufficient allocation history"
	}

	workloads := measuredWorkloads(snap, steps[0])
	namespaces := measuredNamespaces(prior, recent)

	sortWorkloads(workloads)
	sort.Slice(namespaces, func(i, j int) bool {
		return namespaces[i].Namespace < namespaces[j].Namespace
	})

	return &snapshot.CostSet{
		Basis:       snapshot.CostMeasured,
		Source:      "opencost",
		Currency:    "USD",
		Window:      "7d",
		CollectedAt: snap.Meta.CollectedAt,
		Workloads:   workloads,
		Namespaces:  namespaces,
		Note:        trendNote,
	}, nil
}

// queryAllocation issues one opencost.allocation query and unmarshals the
// connector's json.Marshal([][]opencost.Allocation) output back into that shape.
func queryAllocation(ctx context.Context, oc connector.Connector, args map[string]any) ([][]ocpkg.Allocation, error) {
	b, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	raw, err := oc.Query(ctx, "opencost.allocation", b)
	if err != nil {
		return nil, err
	}
	var steps [][]ocpkg.Allocation
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

// measuredWorkloads joins each controller-scoped allocation row to a snapshot
// workload (Deployment/StatefulSet/DaemonSet) by (namespace, name, kind), the
// kind match being case-insensitive (OpenCost emits lowercase). Rows with an
// empty ControllerKind or no matching workload are dropped.
func measuredWorkloads(snap *snapshot.Snapshot, rows []ocpkg.Allocation) []snapshot.WorkloadCost {
	specs := collectWorkloadSpecs(snap)
	out := make([]snapshot.WorkloadCost, 0, len(rows))
	for _, row := range rows {
		if row.ControllerKind == "" {
			continue
		}
		var match *wlSpec
		for i := range specs {
			sp := &specs[i]
			if row.Namespace == sp.ns && row.Controller == sp.name &&
				strings.EqualFold(row.ControllerKind, sp.kind) {
				match = sp
				break
			}
		}
		if match == nil {
			continue
		}

		var replicas int32
		switch {
		case match.kind == "DaemonSet":
			replicas = int32(len(runningPodsOf(snap, "DaemonSet", match.uid)))
		case match.replicas != nil:
			replicas = *match.replicas
		default:
			replicas = 1
		}

		out = append(out, snapshot.WorkloadCost{
			Namespace:       row.Namespace,
			Kind:            match.kind, // canonical case from the snapshot
			Name:            match.name,
			Replicas:        replicas,
			CPURequestCores: row.CPUCoreRequest,
			CPUUsageCores:   row.CPUCoreUsage,
			MemRequestBytes: row.RAMByteRequest,
			MemUsageBytes:   row.RAMByteUsage,
			MonthlyCPUCost:  row.CPUCost * monthOverWeek,
			MonthlyMemCost:  row.RAMCost * monthOverWeek,
			MonthlyCost:     (row.CPUCost + row.RAMCost + row.PVCost + row.LBCost) * monthOverWeek,
		})
	}
	return out
}

// measuredNamespaces produces one NamespaceCost per namespace present in the
// recent 7d step, with PriorMonthlyCost pulled from the earlier step or -1 when
// that namespace has no prior row.
func measuredNamespaces(prior, recent []ocpkg.Allocation) []snapshot.NamespaceCost {
	priorByNS := make(map[string]float64, len(prior))
	for _, r := range prior {
		priorByNS[r.Namespace] = r.TotalCost
	}
	out := make([]snapshot.NamespaceCost, 0, len(recent))
	for _, r := range recent {
		nc := snapshot.NamespaceCost{
			Namespace:        r.Namespace,
			MonthlyCost:      r.TotalCost * monthOverWeek,
			PriorMonthlyCost: -1,
		}
		if pc, ok := priorByNS[r.Namespace]; ok {
			nc.PriorMonthlyCost = pc * monthOverWeek
		}
		out = append(out, nc)
	}
	return out
}

func sortWorkloads(w []snapshot.WorkloadCost) {
	sort.Slice(w, func(i, j int) bool {
		a, b := w[i], w[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
}

// rateFromConfig converts a config rate override into a price-sheet override
// (nil -> nil, i.e. keep the sheet's own rates).
func rateFromConfig(r *config.Rate) *Rate {
	if r == nil {
		return nil
	}
	return &Rate{CPUHour: r.CPUHour, MemGiBHour: r.MemGiBHour}
}

// joinNotes concatenates non-empty note fragments with "; " in a fixed order
// (measured-path note first, estimate-path note second).
func joinNotes(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "; ")
}
