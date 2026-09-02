package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeConnector struct {
	name  string
	avail Availability
}

func (f fakeConnector) Name() string                       { return f.name }
func (f fakeConnector) Probe(context.Context) Availability { return f.avail }
func (f fakeConnector) Capabilities() []Capability         { return nil }
func (f fakeConnector) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestRegistryProbeAndAvailable(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeConnector{name: "k8s", avail: Availability{State: StateAvailable}})
	r.Register(fakeConnector{name: "promql", avail: Availability{State: StateAbsent, Reason: "not found"}})

	statuses := r.Probe(context.Background())
	require.Len(t, statuses, 2)
	require.Equal(t, "k8s", statuses[0].Name)
	require.Equal(t, StateAbsent, statuses[1].Availability.State)

	avail := r.Available()
	require.Len(t, avail, 1)
	require.Equal(t, "k8s", avail[0].Name())

	require.True(t, r.Satisfied([]string{"k8s"}))
	require.False(t, r.Satisfied([]string{"k8s", "promql"}))

	c, ok := r.Get("promql")
	require.True(t, ok)
	require.Equal(t, "promql", c.Name())
}
