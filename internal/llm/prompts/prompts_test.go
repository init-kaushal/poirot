package prompts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadReturnsBodyAndVersion(t *testing.T) {
	body, ver, err := Load("investigate_system")
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.False(t, strings.HasPrefix(body, "# v"), "header line must be stripped from the body")
	require.Regexp(t, `^v\d+\+[0-9a-f]{8}$`, ver)
}

func TestLoadUnknownName(t *testing.T) {
	_, _, err := Load("nope")
	require.Error(t, err)
}

func TestLoadAllThreePromptsPresent(t *testing.T) {
	for _, n := range []string{"investigate_system", "correlate", "synthesize"} {
		_, _, err := Load(n)
		require.NoError(t, err, n)
	}
}
