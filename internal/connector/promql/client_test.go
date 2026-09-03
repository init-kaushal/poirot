package promql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientInstantViaFakeDoer(t *testing.T) {
	var gotPath string
	var gotParams url.Values
	c := newClient(func(_ context.Context, path string, p url.Values) ([]byte, error) {
		gotPath, gotParams = path, p
		return read(t, "vector_ok.json"), nil
	})

	s, err := c.Instant(context.Background(), "up", time.Unix(1735732800, 0))
	require.NoError(t, err)
	require.Len(t, s, 2)
	require.Equal(t, "api/v1/query", gotPath)
	require.Equal(t, "up", gotParams.Get("query"))
	require.Equal(t, "1735732800", gotParams.Get("time"))
}

func TestHTTPDoerHitsRealPathAndParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/query", r.URL.Path)
		require.Equal(t, "vector(1)", r.URL.Query().Get("query"))
		w.Write(read(t, "vector_ok.json"))
	}))
	defer srv.Close()

	d, err := httpDoer(srv.URL+"/", nil)
	require.NoError(t, err)
	c := newClient(d)
	s, err := c.Instant(context.Background(), "vector(1)", time.Unix(1735732800, 0))
	require.NoError(t, err)
	require.Len(t, s, 2)
}

func TestHTTPDoerRejectsBadBase(t *testing.T) {
	_, err := httpDoer("not-a-url", nil)
	require.Error(t, err)
}

func TestHTTPDoerNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	d, _ := httpDoer(srv.URL, nil)
	_, err := newClient(d).Instant(context.Background(), "up", time.Now())
	require.ErrorContains(t, err, "502")
}
