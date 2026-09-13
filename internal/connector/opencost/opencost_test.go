package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/connector"
)

func TestProbeAutoAbsentWhenNoService(t *testing.T) {
	c := New(Options{URL: "auto", Clientset: fake.NewSimpleClientset()})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
}

func TestProbeDisabled(t *testing.T) {
	c := New(Options{URL: "disabled"})
	require.Equal(t, connector.StateAbsent, c.Probe(context.Background()).State)
	require.Equal(t, "disabled in config", c.Probe(context.Background()).Reason)
}

func TestProbeExplicitURLAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
		w.Write(b)
	}))
	defer srv.Close()
	c := New(Options{URL: srv.URL})
	require.Equal(t, connector.StateAvailable, c.Probe(context.Background()).State)
	out, err := c.Query(context.Background(), "opencost.allocation",
		json.RawMessage(`{"window":"7d","aggregate":"namespace,controller","accumulate":true}`))
	require.NoError(t, err)
	require.Contains(t, string(out), "web-deploy")
}
