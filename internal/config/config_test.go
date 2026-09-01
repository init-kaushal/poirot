package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	require.NoError(t, err)

	require.Equal(t, "prod", cfg.Cluster.Context)
	require.Equal(t, 24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, []string{"kube-system", "kube-node-lease"}, cfg.Scope.Exclude)
	require.Equal(t, "auto", cfg.Connectors.PromQL.URL)
	require.True(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.Equal(t, "auto", cfg.Connectors.GitOps.Mode)
	require.Equal(t, "anthropic", cfg.LLM.Provider)
	require.Equal(t, "claude-sonnet-5", cfg.LLM.Model)
	require.Equal(t, 6, cfg.LLM.MaxToolCallsPerFinding)
	require.Equal(t, 15, cfg.LLM.MaxFindingsInvestigated)
	require.Equal(t, "./poirot-out", cfg.Output.Dir)
	require.Equal(t, "critical", cfg.Output.FailOn)
}

func TestLoadFullOverridesDefaults(t *testing.T) {
	cfg, err := Load("testdata/full.yaml")
	require.NoError(t, err)

	require.Equal(t, []string{"payments", "checkout"}, cfg.Scope.Namespaces)
	require.Equal(t, 7*24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, "http://prom.mon:9090", cfg.Connectors.PromQL.URL)
	require.Equal(t, "none", cfg.LLM.Provider)
	require.Equal(t, "warning", cfg.Output.FailOn)
	require.Equal(t, []string{"cost"}, cfg.Focus)
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	_, err := Load("testdata/bad-provider.yaml")
	require.ErrorContains(t, err, "llm.provider")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	require.Error(t, err)
}
