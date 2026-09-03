package metrics

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// Collect runs every entry in pack against c and returns a MetricSet with one
// result per entry, in pack order. A single query or decode failure is captured
// in that result's Error and never aborts the collect. Never returns nil.
func Collect(ctx context.Context, c connector.Connector, pack []Entry, at time.Time, backend string) *snapshot.MetricSet {
	set := &snapshot.MetricSet{Backend: backend, CollectedAt: at, Results: make([]snapshot.MetricResult, 0, len(pack))}
	for _, e := range pack {
		res := snapshot.MetricResult{Name: e.Name, Expr: e.Expr}
		capID, args := "promql.instant", mustJSON(map[string]any{"expr": e.Expr, "time": at.Format(time.RFC3339)})
		if e.Kind == KindRange {
			capID, args = "promql.range", mustJSON(map[string]any{"expr": e.Expr, "stepSeconds": 60})
		}
		raw, err := c.Query(ctx, capID, args)
		if err != nil {
			res.Error = err.Error()
			set.Results = append(set.Results, res)
			continue
		}
		var samples []snapshot.MetricSample
		if err := json.Unmarshal(raw, &samples); err != nil {
			res.Error = "decode result: " + err.Error()
			set.Results = append(set.Results, res)
			continue
		}
		sortSamples(samples)
		res.Samples = samples
		set.Results = append(set.Results, res)
	}
	return set
}

// sortSamples orders samples deterministically: by the sorted-key-joined label
// string, then by value. Prometheus result order is not guaranteed, so this
// guards report.json byte-stability.
func sortSamples(s []snapshot.MetricSample) {
	sort.SliceStable(s, func(i, j int) bool {
		ki, kj := labelKey(s[i].Labels), labelKey(s[j].Labels)
		if ki != kj {
			return ki < kj
		}
		return s[i].Value < s[j].Value
	})
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(',')
	}
	return b.String()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
