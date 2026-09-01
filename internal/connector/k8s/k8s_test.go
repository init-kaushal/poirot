package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/connector"
)

func TestProbeAvailableWithFakeClient(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewWithClient(cs, "test-ctx", Scope{})

	require.Equal(t, "k8s", c.Name())
	require.Equal(t, "test-ctx", c.ContextName())

	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAvailable, av.State)
}

func TestQueryUnsupportedInM1(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "test-ctx", Scope{})
	_, err := c.Query(context.Background(), "anything", nil)
	require.ErrorContains(t, err, "no queryable capabilities")
}
