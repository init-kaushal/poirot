package analyzer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeverityRankOrders(t *testing.T) {
	require.Greater(t, SeverityCritical.Rank(), SeverityWarning.Rank())
	require.Greater(t, SeverityWarning.Rank(), SeverityInfo.Rank())
	require.Equal(t, 0, Severity("bogus").Rank())
}

func TestObjectRefString(t *testing.T) {
	require.Equal(t, "Node/worker-1", ObjectRef{Kind: "Node", Name: "worker-1"}.String())
	require.Equal(t, "Pod/payments/api-abc", ObjectRef{Kind: "Pod", Namespace: "payments", Name: "api-abc"}.String())
}
