package opencost

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAllocationHappy(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Len(t, steps[0], 1) // __idle__ dropped
	a := steps[0][0]
	require.Equal(t, "team", a.Namespace)
	require.Equal(t, "web-deploy", a.Controller)
	require.Equal(t, "deployment", a.ControllerKind)
	require.InDelta(t, 2.0, a.CPUCoreRequest, 1e-9)
	require.InDelta(t, 50.0, a.TotalCost, 1e-9)
}

func TestParseAllocationTwoSteps(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_ns_14d.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 2)
}

func TestParseAllocationError(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_error.json")
	_, err := ParseAllocation(b)
	require.Error(t, err)
}

func TestParseAllocationEmpty(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_empty.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Empty(t, steps[0])
}
