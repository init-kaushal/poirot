package snapshot

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricSetResult(t *testing.T) {
	m := &MetricSet{Results: []MetricResult{
		{Name: "cpu_saturation", Expr: "rate(...)"},
		{Name: "targets_down", Expr: "up == 0"},
	}}
	require.Equal(t, "up == 0", m.Result("targets_down").Expr)
	require.Nil(t, m.Result("nope"))

	var nilSet *MetricSet
	require.Nil(t, nilSet.Result("cpu_saturation"))
}

func TestSnapshotHasMetricsField(t *testing.T) {
	var s Snapshot
	require.Nil(t, s.Metrics) // zero value is nil pointer
	s.Metrics = &MetricSet{Backend: "prometheus"}
	require.Equal(t, "prometheus", s.Metrics.Backend)
}

func TestSnapshotHasPodLogsField(t *testing.T) {
	var s Snapshot
	require.Nil(t, s.PodLogs) // zero value is nil map

	s.PodLogs = map[string][]LogChunk{
		"Pod/p/api-1": {{Container: "api", Previous: true, Lines: "boom"}},
	}
	chunk := s.PodLogs["Pod/p/api-1"][0]
	require.Equal(t, "api", chunk.Container)
	require.True(t, chunk.Previous)
	require.Equal(t, "boom", chunk.Lines)
}

func TestCostSetJSONKeys(t *testing.T) {
	s := Snapshot{
		Cost: &CostSet{
			Basis:      CostEstimated,
			Workloads:  []WorkloadCost{{Namespace: "n"}},
			Namespaces: []NamespaceCost{{Namespace: "n"}},
		},
	}
	data, err := json.Marshal(s)
	require.NoError(t, err)

	// Populated Snapshot should contain cost, basis, workloads, namespaces
	jsonStr := string(data)
	require.Contains(t, jsonStr, `"cost"`)
	require.Contains(t, jsonStr, `"basis":"estimated"`)
	require.Contains(t, jsonStr, `"workloads"`)
	require.Contains(t, jsonStr, `"namespaces"`)

	// Empty Snapshot should omit cost and jobs (omitempty)
	empty := Snapshot{}
	emptyData, err := json.Marshal(empty)
	require.NoError(t, err)
	emptyStr := string(emptyData)
	require.NotContains(t, emptyStr, `"cost"`)
	require.NotContains(t, emptyStr, `"jobs"`)
}
