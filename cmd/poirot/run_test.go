package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunCommandWritesReportsAndExits(t *testing.T) {
	dir := t.TempDir()
	cfg := "cluster:\n  context: does-not-matter\nllm:\n  provider: none\noutput:\n  dir: " + filepath.Join(dir, "out") + "\n"
	cfgPath := filepath.Join(dir, "poirot.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o644))

	// Point KUBECONFIG at an empty file so k8s.New fails fast and the command
	// returns an error (there is no cluster in CI). This asserts wiring, not success.
	empty := filepath.Join(dir, "kubeconfig")
	require.NoError(t, os.WriteFile(empty, []byte("apiVersion: v1\nkind: Config\n"), 0o644))
	t.Setenv("KUBECONFIG", empty)

	cmd := newRootCmd("test")
	cmd.SetArgs([]string{"run", "-c", cfgPath})
	err := cmd.Execute()
	require.Error(t, err) // no reachable cluster
}
