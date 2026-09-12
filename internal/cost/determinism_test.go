package cost

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestCostCollectDeterministicMeasured locks the parse -> case-fold join ->
// sort chain on the measured path: 50 Collect calls against a frozen fixture
// must produce byte-identical CostSets.
func TestCostCollectDeterministicMeasured(t *testing.T) {
	snap := &snapshot.Snapshot{
		Deployments: []appsv1.Deployment{deploy("team", "web-deploy", 2, "1", "1Gi")},
	}
	reg := connector.NewRegistry()
	reg.Register(fakeOC{})
	reg.Probe(context.Background())
	spec := config.ConnectorSpec{EstimateFallback: ptr(true)}

	first := mustJSON(t, Collect(context.Background(), snap, reg, spec))
	for i := 0; i < 50; i++ {
		require.Equal(t, first, mustJSON(t, Collect(context.Background(), snap, reg, spec)))
	}
}

// TestCostCollectDeterministicEstimate locks the estimate path end-to-end
// through the public Collect entry point, including the R9 instance-type
// tie-break: 3 workloads, pods split across two instance types.
func TestCostCollectDeterministicEstimate(t *testing.T) {
	snap := &snapshot.Snapshot{
		Nodes: []corev1.Node{node("a", "c6i.large"), node("b", "m6i.large")},
		Pods: []corev1.Pod{
			runningPod("t", "d1-1", "a", ctrlRef("Deployment", "t", "d1")),
			runningPod("t", "d1-2", "b", ctrlRef("Deployment", "t", "d1")),
			runningPod("t", "d2-1", "a", ctrlRef("Deployment", "t", "d2")),
			runningPod("t", "d3-1", "b", ctrlRef("Deployment", "t", "d3")),
		},
		Deployments: []appsv1.Deployment{
			deploy("t", "d1", 2, "500m", "512Mi"),
			deploy("t", "d2", 1, "250m", "256Mi"),
			deploy("t", "d3", 1, "1", "1Gi"),
		},
	}
	reg := connector.NewRegistry() // no opencost registered -> estimate path
	spec := config.ConnectorSpec{EstimateFallback: ptr(true)}

	first := mustJSON(t, Collect(context.Background(), snap, reg, spec))
	for i := 0; i < 50; i++ {
		require.Equal(t, first, mustJSON(t, Collect(context.Background(), snap, reg, spec)))
	}
}
