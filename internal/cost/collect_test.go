package cost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	ocpkg "github.com/init-kaushal/poirot/internal/connector/opencost"
	"github.com/init-kaushal/poirot/internal/snapshot"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

// fakeOC: a connector.Connector that answers opencost.allocation from testdata,
// switching on the "window" arg. Probe -> Available.
type fakeOC struct{}

func (fakeOC) Name() string { return "opencost" }
func (fakeOC) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAvailable}
}
func (fakeOC) Capabilities() []connector.Capability { return nil }
func (fakeOC) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Window string `json:"window"`
	}
	_ = json.Unmarshal(args, &a)
	f := "testdata/alloc_ns_controller.json"
	if a.Window == "14d" {
		f = "testdata/alloc_ns_14d.json"
	}
	// re-parse via the opencost package the same way the real connector does,
	// then re-marshal, so the shape matches production exactly:
	b, _ := os.ReadFile(filepath.Join("..", "connector", "opencost", f))
	steps, err := ocpkg.ParseAllocation(b)
	if err != nil {
		return nil, err
	}
	return json.Marshal(steps)
}

type errOC struct{ fakeOC }

func (errOC) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("boom")
}

func TestCollectMeasuredJoinsCaseInsensitiveKind(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakeOC{})
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Deployments: []appsv1.Deployment{deploy("team", "web-deploy", 2, "1", "1Gi")},
	}
	cs := Collect(context.Background(), snap, reg, config.ConnectorSpec{EstimateFallback: ptr(true)})
	require.NotNil(t, cs)
	require.Equal(t, snapshot.CostMeasured, cs.Basis)
	require.Len(t, cs.Workloads, 1)
	require.Equal(t, "Deployment", cs.Workloads[0].Kind)                   // opencost "deployment" -> snapshot "Deployment"
	require.InDelta(t, 50.0*(30.0/7.0), cs.Workloads[0].MonthlyCost, 1e-6) // totalCost 50 in the fixture
}

func TestCollectFallsBackToEstimateOnQueryError(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(errOC{})
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "p", "n", ctrlRef("Deployment", "t", "d"))},
		Deployments: []appsv1.Deployment{deploy("t", "d", 1, "500m", "512Mi")},
	}
	cs := Collect(context.Background(), snap, reg, config.ConnectorSpec{EstimateFallback: ptr(true)})
	require.Equal(t, snapshot.CostEstimated, cs.Basis)
	require.Contains(t, cs.Note, "boom")
}

func TestCollectNilWhenNoOpenCostAndFallbackOff(t *testing.T) {
	cs := Collect(context.Background(), &snapshot.Snapshot{Meta: snapshot.Meta{}}, connector.NewRegistry(),
		config.ConnectorSpec{EstimateFallback: ptr(false)})
	require.Nil(t, cs)
}

func TestCollectEstimateWhenNoOpenCostRegistered(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "p", "n", ctrlRef("Deployment", "t", "d"))},
		Deployments: []appsv1.Deployment{deploy("t", "d", 1, "500m", "512Mi")},
	}
	cs := Collect(context.Background(), snap, connector.NewRegistry(), config.ConnectorSpec{EstimateFallback: ptr(true)})
	require.NotNil(t, cs)
	require.Equal(t, snapshot.CostEstimated, cs.Basis)
}
