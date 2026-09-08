// Package opencost parses OpenCost /allocation/compute responses into a
// connector-friendly shape. This file is the tolerant JSON parser; the HTTP
// client, discovery, and connector.Connector wrapper live in sibling files.
package opencost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Allocation is one workload (or namespace aggregate) row from an OpenCost
// allocation step.
type Allocation struct {
	Namespace      string
	Controller     string
	ControllerKind string  // lowercase from OpenCost: "deployment" | "statefulset" | "daemonset" | ""
	CPUCoreRequest float64 // cpuCoreRequestAverage
	CPUCoreUsage   float64 // cpuCoreUsageAverage
	RAMByteRequest float64 // ramByteRequestAverage
	RAMByteUsage   float64 // ramByteUsageAverage
	CPUCost        float64
	RAMCost        float64
	PVCost         float64
	LBCost         float64
	TotalCost      float64
}

type envelope struct {
	Code    *int              `json:"code"`
	Message string            `json:"message"`
	Data    []json.RawMessage `json:"data"`
}

type rawAlloc struct {
	Name       string `json:"name"`
	Properties struct {
		Namespace      string `json:"namespace"`
		Controller     string `json:"controller"`
		ControllerKind string `json:"controllerKind"`
	} `json:"properties"`
	CPUCoreRequestAverage float64 `json:"cpuCoreRequestAverage"`
	CPUCoreUsageAverage   float64 `json:"cpuCoreUsageAverage"`
	RAMByteRequestAverage float64 `json:"ramByteRequestAverage"`
	RAMByteUsageAverage   float64 `json:"ramByteUsageAverage"`
	CPUCost               float64 `json:"cpuCost"`
	RAMCost               float64 `json:"ramCost"`
	PVCost                float64 `json:"pvCost"`
	LoadBalancerCost      float64 `json:"loadBalancerCost"`
	TotalCost             float64 `json:"totalCost"`
}

// ParseAllocation decodes one OpenCost /allocation/compute response body.
// It returns one []Allocation per step in "data" (len 1 for accumulate=true,
// len 2 for the 14d step=7d call). Rows keyed __idle__/__unallocated__ or with
// an empty name are dropped. A row that fails to decode is skipped, not fatal.
func ParseAllocation(body []byte) (steps [][]Allocation, err error) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("opencost: decode allocation response: %w", err)
	}
	if env.Code != nil && *env.Code >= 400 {
		if env.Message != "" {
			return nil, fmt.Errorf("opencost: allocation request failed: code %d: %s", *env.Code, env.Message)
		}
		return nil, fmt.Errorf("opencost: allocation request failed: code %d", *env.Code)
	}
	if env.Data == nil {
		return nil, fmt.Errorf("opencost: allocation response has no data array")
	}

	steps = make([][]Allocation, 0, len(env.Data))
	for _, rawStep := range env.Data {
		var rows map[string]rawAlloc
		if err := json.Unmarshal(rawStep, &rows); err != nil {
			// A step that fails to decode is skipped, not fatal.
			steps = append(steps, []Allocation{})
			continue
		}
		out := make([]Allocation, 0, len(rows))
		for key, r := range rows {
			if skipKey(key) || skipKey(r.Name) {
				continue
			}
			if r.Properties.Namespace == "" {
				continue
			}
			out = append(out, Allocation{
				Namespace:      r.Properties.Namespace,
				Controller:     r.Properties.Controller,
				ControllerKind: r.Properties.ControllerKind,
				CPUCoreRequest: r.CPUCoreRequestAverage,
				CPUCoreUsage:   r.CPUCoreUsageAverage,
				RAMByteRequest: r.RAMByteRequestAverage,
				RAMByteUsage:   r.RAMByteUsageAverage,
				CPUCost:        r.CPUCost,
				RAMCost:        r.RAMCost,
				PVCost:         r.PVCost,
				LBCost:         r.LoadBalancerCost,
				TotalCost:      r.TotalCost,
			})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Namespace != out[j].Namespace {
				return out[i].Namespace < out[j].Namespace
			}
			if out[i].ControllerKind != out[j].ControllerKind {
				return out[i].ControllerKind < out[j].ControllerKind
			}
			return out[i].Controller < out[j].Controller
		})
		steps = append(steps, out)
	}
	return steps, nil
}

// skipKey reports whether an allocation identifier is an OpenCost pseudo-row
// (__idle__ / __unallocated__) or empty, and so must be dropped.
func skipKey(s string) bool {
	if s == "" {
		return true
	}
	return strings.Contains(s, "__idle__") || strings.Contains(s, "__unallocated__")
}
