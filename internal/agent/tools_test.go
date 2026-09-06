package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// fakeConn implements connector.Connector with one capability "<name>.echo".
type fakeConn struct {
	name  string
	avail connector.State
}

func (f fakeConn) Name() string { return f.name }
func (f fakeConn) Probe(context.Context) connector.Availability {
	return connector.Availability{State: f.avail}
}
func (f fakeConn) Capabilities() []connector.Capability {
	return []connector.Capability{{ID: f.name + ".echo", Description: "echo",
		ArgsSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (f fakeConn) Query(_ context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

// erroringConn is available, exposes one k8s.get capability, and always errors.
type erroringConn struct{}

func (erroringConn) Name() string { return "k8s" }
func (erroringConn) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAvailable}
}
func (erroringConn) Capabilities() []connector.Capability {
	return []connector.Capability{{ID: "k8s.get", Description: "get",
		ArgsSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (erroringConn) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("boom")
}

// regWithK8s returns a probed registry exposing one available fake "k8s"
// connector, so connector-routed tool calls (k8s.*) resolve during tests.
func regWithK8s(t *testing.T) *connector.Registry {
	t.Helper()
	reg := connector.NewRegistry()
	reg.Register(fakeConn{name: "k8s", avail: connector.StateAvailable})
	reg.Probe(context.Background())
	return reg
}

func TestToolsListsOnlyAvailableConnectors(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakeConn{name: "k8s", avail: connector.StateAvailable})
	reg.Register(fakeConn{name: "promql", avail: connector.StateAbsent})
	reg.Probe(context.Background())

	tp := NewInProcessToolProvider(reg, &snapshot.Snapshot{}, nil)
	names := map[string]bool{}
	for _, tspec := range tp.Tools() {
		names[tspec.Name] = true
	}
	require.True(t, names["snapshot.findings"])
	require.True(t, names["snapshot.object"])
	require.True(t, names["snapshot.events"])
	require.True(t, names["snapshot.logs"])
	require.True(t, names["k8s.echo"])
	require.False(t, names["promql.echo"], "absent connector's caps must not be listed")
}

func TestToolsIncludesSnapshotLogs(t *testing.T) {
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, nil)
	tools := tp.Tools()
	require.GreaterOrEqual(t, len(tools), 4)
	require.Equal(t, "snapshot.findings", tools[0].Name)
	require.Equal(t, "snapshot.object", tools[1].Name)
	require.Equal(t, "snapshot.events", tools[2].Name)
	require.Equal(t, "snapshot.logs", tools[3].Name)
}

func TestInvokeSnapshotObject(t *testing.T) {
	snap := &snapshot.Snapshot{Pods: []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "p"},
	}}}
	tp := NewInProcessToolProvider(connector.NewRegistry(), snap, nil)
	res, isErr, err := tp.Invoke(context.Background(), "snapshot.object",
		json.RawMessage(`{"kind":"Pod","namespace":"p","name":"api-1"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	require.Contains(t, string(res), "api-1")
}

func TestInvokeSnapshotObjectNotFound(t *testing.T) {
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, nil)
	_, isErr, err := tp.Invoke(context.Background(), "snapshot.object",
		json.RawMessage(`{"kind":"Pod","namespace":"p","name":"nope"}`))
	require.NoError(t, err)
	require.True(t, isErr)
}

func TestInvokeSnapshotFindings(t *testing.T) {
	findings := []analyzer.Finding{
		{RuleID: "r1", Domain: "reliability", Severity: analyzer.SeverityCritical,
			Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "api-1"}, Summary: "crashloop"},
		{RuleID: "r2", Domain: "slo", Severity: analyzer.SeverityInfo,
			Object: analyzer.ObjectRef{Kind: "Deployment", Namespace: "p", Name: "api"}, Summary: "fine"},
	}
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, findings)
	res, isErr, err := tp.Invoke(context.Background(), "snapshot.findings",
		json.RawMessage(`{"severityGte":"warning"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	require.Contains(t, string(res), "r1")
	require.NotContains(t, string(res), "r2")
}

func TestInvokeSnapshotLogs(t *testing.T) {
	snap := &snapshot.Snapshot{PodLogs: map[string][]snapshot.LogChunk{
		"Pod/p/api-1": {{Container: "api", Previous: false, Lines: "boom"}},
	}}
	tp := NewInProcessToolProvider(connector.NewRegistry(), snap, nil)

	res, isErr, err := tp.Invoke(context.Background(), "snapshot.logs",
		json.RawMessage(`{"namespace":"p","pod":"api-1"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	require.Contains(t, string(res), "boom")

	_, isErr, err = tp.Invoke(context.Background(), "snapshot.logs",
		json.RawMessage(`{"namespace":"p","pod":"missing"}`))
	require.NoError(t, err)
	require.True(t, isErr)
}

func TestInvokeConnectorQueryErrorBecomesIsErr(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(erroringConn{}) // Name "k8s", Query returns an error
	reg.Probe(context.Background())
	tp := NewInProcessToolProvider(reg, &snapshot.Snapshot{}, nil)
	res, isErr, err := tp.Invoke(context.Background(), "k8s.get", json.RawMessage(`{}`))
	require.NoError(t, err) // infra ok
	require.True(t, isErr)  // tool reported an error
	require.Contains(t, string(res), "tool error")
}

func TestInvokeUnknownTool(t *testing.T) {
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, nil)
	_, isErr, err := tp.Invoke(context.Background(), "nope", nil)
	require.NoError(t, err)
	require.True(t, isErr)
}

func TestInvokeContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, nil)
	_, isErr, err := tp.Invoke(ctx, "snapshot.findings", nil)
	require.Error(t, err)
	require.False(t, isErr)
}
