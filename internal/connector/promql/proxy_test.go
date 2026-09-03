package promql

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFlattenParams(t *testing.T) {
	p := url.Values{}
	p.Set("query", "up == 0")
	p.Set("time", "123")
	got := flattenParams(p)
	require.Equal(t, map[string]string{"query": "up == 0", "time": "123"}, got)
}

func TestProxyDoerConstructs(t *testing.T) {
	d := proxyDoer(fake.NewSimpleClientset(), Target{Namespace: "m", Name: "prom", Port: "http", Scheme: "http"})
	require.NotNil(t, d)
}
