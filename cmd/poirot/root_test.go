package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionCommandPrintsVersion(t *testing.T) {
	cmd := newRootCmd("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	require.NoError(t, cmd.Execute())
	require.Equal(t, "1.2.3\n", out.String())
}

func TestExitErrorMessage(t *testing.T) {
	err := &ExitError{Code: 2}
	require.EqualError(t, err, "exit code 2")
}
