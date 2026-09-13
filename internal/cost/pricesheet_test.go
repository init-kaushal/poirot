package cost

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestSheetLoad(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	require.Positive(t, s.Default.CPUHour)
	require.Contains(t, s.ByInstanceType, "m6i.large")
}

func TestSheetRateFallsBackToDefault(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	require.Equal(t, s.Default, s.Rate("no-such-type"))
	require.Equal(t, s.ByInstanceType["m6i.large"], s.Rate("m6i.large"))
}

func TestSheetOverrideWinsForEveryType(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	o := &Rate{CPUHour: 1, MemGiBHour: 2}
	s.SetOverride(o)
	require.Equal(t, *o, s.Rate("m6i.large")) // even a known type
	require.Equal(t, *o, s.Rate("whatever"))
	s.SetOverride(nil) // no-op, override stays
	require.Equal(t, *o, s.Rate("m6i.large"))
}
