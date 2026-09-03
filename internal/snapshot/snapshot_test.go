package snapshot

import (
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
