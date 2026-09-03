package promql

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

func TestParseInstantVector(t *testing.T) {
	s, err := parseInstant(read(t, "vector_ok.json"))
	require.NoError(t, err)
	require.Len(t, s, 2)
	require.Equal(t, "payments", s[0].Labels["namespace"])
	require.InDelta(t, 0.94, s[0].Value, 1e-9)
}

func TestParseInstantEmpty(t *testing.T) {
	s, err := parseInstant(read(t, "empty_vector.json"))
	require.NoError(t, err)
	require.Empty(t, s)
}

func TestParseInstantError(t *testing.T) {
	_, err := parseInstant(read(t, "error.json"))
	var ae *apiError
	require.True(t, errors.As(err, &ae))
	require.Equal(t, "bad_data", ae.Type)
}

func TestParseInstantSkipsNonFinite(t *testing.T) {
	s, err := parseInstant(read(t, "nonfinite.json"))
	require.NoError(t, err)
	require.Len(t, s, 1)
	require.Equal(t, "c", s[0].Labels["namespace"])
	require.InDelta(t, 0.42, s[0].Value, 1e-9)
}

func TestParseRangeUsesLastPoint(t *testing.T) {
	s, err := parseRange(read(t, "matrix_ok.json"))
	require.NoError(t, err)
	require.Len(t, s, 1)
	require.Equal(t, "kubelet", s[0].Labels["job"])
	require.InDelta(t, 0.0, s[0].Value, 1e-9) // last of [["...","1"],["...","0"]]
}
