package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type fakeConn struct {
	byExpr map[string][]snapshot.MetricSample
	fail   map[string]bool
}

func (fakeConn) Name() string { return "promql" }

func (fakeConn) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAvailable}
}

func (fakeConn) Capabilities() []connector.Capability { return nil }

func (f fakeConn) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Expr string `json:"expr"`
	}
	_ = json.Unmarshal(args, &a)
	if f.fail[a.Expr] {
		return nil, errors.New("boom")
	}
	return json.Marshal(f.byExpr[a.Expr])
}

func TestCollectRunsPackAndToleratesPerQueryFailure(t *testing.T) {
	pack := DefaultPack()
	f := fakeConn{
		byExpr: map[string][]snapshot.MetricSample{
			pack[0].Expr: {{Labels: map[string]string{"pod": "b"}, Value: 0.9}, {Labels: map[string]string{"pod": "a"}, Value: 0.5}},
		},
		fail: map[string]bool{pack[4].Expr: true},
	}
	at := time.Unix(1735732800, 0)
	set := Collect(context.Background(), f, pack, at, "test")

	require.Equal(t, "test", set.Backend)
	require.Equal(t, at, set.CollectedAt)
	require.Len(t, set.Results, 5)

	cpu := set.Result("cpu_saturation")
	require.NoError(t, errFromResult(cpu))
	require.Equal(t, "a", cpu.Samples[0].Labels["pod"]) // sorted by label string

	td := set.Result("targets_down")
	require.NotEmpty(t, td.Error) // per-query failure captured, not fatal
	require.Empty(t, td.Samples)

	nr := set.Result("pod_not_ready")
	require.Empty(t, nr.Error)
	require.Empty(t, nr.Samples) // fakeConn returned nil for this expr → empty, no error
}

func errFromResult(r *snapshot.MetricResult) error {
	if r != nil && r.Error != "" {
		return errors.New(r.Error)
	}
	return nil
}
