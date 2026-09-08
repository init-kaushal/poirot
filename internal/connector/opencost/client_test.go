package opencost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAllocationSendsAccumulateAndStep(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
		w.Write(b)
	}))
	defer srv.Close()
	d, err := httpDoer(srv.URL, srv.Client())
	require.NoError(t, err)
	c := newClient(d)

	_, err = c.Allocation(context.Background(), "7d", "namespace,controller", true, "")
	require.NoError(t, err)
	require.Equal(t, "true", gotQuery.Get("accumulate"))
	require.Equal(t, "7d", gotQuery.Get("window"))
	require.Empty(t, gotQuery.Get("step"))

	_, err = c.Allocation(context.Background(), "14d", "namespace", false, "7d")
	require.NoError(t, err)
	require.Equal(t, "false", gotQuery.Get("accumulate"))
	require.Equal(t, "7d", gotQuery.Get("step"))
}

func TestAllocationSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("upstream down"))
	}))
	defer srv.Close()
	d, _ := httpDoer(srv.URL, srv.Client())
	_, err := newClient(d).Allocation(context.Background(), "7d", "namespace", true, "")
	require.ErrorContains(t, err, "503")
}
