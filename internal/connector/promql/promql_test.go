package promql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func fakeCS() *fake.Clientset {
	return fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}})
}

func TestConnectorDisabled(t *testing.T) {
	c := New(Options{URL: "disabled"})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
	require.Contains(t, av.Reason, "disabled")
}

func TestConnectorExplicitURLProbeAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(read(t, "vector_ok.json"))
	}))
	defer srv.Close()

	c := New(Options{URL: srv.URL})
	require.Equal(t, "promql", c.Name())
	require.Equal(t, connector.StateAvailable, c.Probe(context.Background()).State)

	caps := c.Capabilities()
	require.Len(t, caps, 2)
	require.Equal(t, "promql.instant", caps[0].ID)
	require.NotEmpty(t, caps[0].ArgsSchema)

	args, _ := json.Marshal(map[string]string{"expr": "up"})
	raw, err := c.Query(context.Background(), "promql.instant", args)
	require.NoError(t, err)
	var got []snapshot.MetricSample
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 2)
}

func TestConnectorExplicitURLDegradedWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	srv.Close() // immediately closed → connection refused

	c := New(Options{URL: srv.URL})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateDegraded, av.State)
}

func TestConnectorAutoNoServiceIsAbsent(t *testing.T) {
	c := New(Options{URL: "auto", Clientset: fakeCS(), Namespaces: []string{"default"}})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
	require.Contains(t, av.Reason, "no metrics Service")
}

func TestQueryBeforeProbeErrors(t *testing.T) {
	c := New(Options{URL: "disabled"})
	_, err := c.Query(context.Background(), "promql.instant", []byte(`{"expr":"up"}`))
	require.Error(t, err)
}
