package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/report"
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

func TestWriteOutputs(t *testing.T) {
	findings := []analyzer.Finding{
		{
			RuleID:   "reliability/crashloop",
			Domain:   "reliability",
			Severity: analyzer.SeverityCritical,
			Title:    "Container in CrashLoopBackOff",
			Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "api-0"},
			Summary:  "container in Pod/n/api-0 is in CrashLoopBackOff",
		},
		{
			RuleID:   "reliability/no-limits",
			Domain:   "reliability",
			Severity: analyzer.SeverityInfo,
			Title:    "Containers have no resource limits",
			Object:   analyzer.ObjectRef{Kind: "Deployment", Namespace: "n", Name: "api"},
			Summary:  "Deployment/n/api: containers without CPU/memory limits: api",
		},
	}
	r := report.Build(report.Meta{Version: "t", Context: "ctx", Lookback: "24h"}, nil, findings)

	dir := t.TempDir()
	require.NoError(t, writeOutputs(dir, r))

	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")
	require.FileExists(t, jsonPath)
	require.FileExists(t, mdPath)

	jb, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	var back report.Report
	require.NoError(t, json.Unmarshal(jb, &back))
	require.Equal(t, "t", back.Meta.Version)
	require.Len(t, back.Findings, len(findings))

	mb, err := os.ReadFile(mdPath)
	require.NoError(t, err)
	md := string(mb)
	require.True(t, strings.Contains(md, "# poirot report"))
	require.True(t, strings.Contains(md, "ctx"))

	// writeOutputs must create a nested, non-existent output directory.
	nested := filepath.Join(t.TempDir(), "a", "b")
	require.NoError(t, writeOutputs(nested, r))
	require.FileExists(t, filepath.Join(nested, "report.json"))
	require.FileExists(t, filepath.Join(nested, "report.md"))
}
