package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return Load(p)
}

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	require.NoError(t, err)

	require.Equal(t, "prod", cfg.Cluster.Context)
	require.Equal(t, 24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, []string{"kube-system", "kube-node-lease"}, cfg.Scope.Exclude)
	require.Equal(t, "auto", cfg.Connectors.PromQL.URL)
	require.NotNil(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.True(t, *cfg.Connectors.OpenCost.EstimateFallback)
	require.Equal(t, "auto", cfg.Connectors.GitOps.Mode)
	require.Equal(t, "anthropic", cfg.LLM.Provider)
	require.Equal(t, "claude-sonnet-5", cfg.LLM.Model)
	require.Equal(t, "POIROT_LLM_API_KEY", cfg.LLM.APIKeyEnv)
	require.Equal(t, "", cfg.LLM.BaseURL)
	require.Equal(t, 8, cfg.LLM.MaxToolCallsPerGroup)
	require.Equal(t, 40000, cfg.LLM.MaxTokensPerGroup)
	require.Equal(t, 15, cfg.LLM.MaxFindingsInvestigated)
	require.Equal(t, 5*time.Minute, time.Duration(cfg.LLM.GlobalBudget))
	require.Equal(t, "./poirot-out", cfg.Output.Dir)
	require.Equal(t, "critical", cfg.Output.FailOn)
}

func TestLoadFullOverridesDefaults(t *testing.T) {
	cfg, err := Load("testdata/full.yaml")
	require.NoError(t, err)

	require.Equal(t, []string{"payments", "checkout"}, cfg.Scope.Namespaces)
	require.Equal(t, 7*24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, "http://prom.mon:9090", cfg.Connectors.PromQL.URL)
	require.Equal(t, "warning", cfg.Output.FailOn)
	require.Equal(t, []string{"cost"}, cfg.Focus)
}

func TestLoadFullLLMBlock(t *testing.T) {
	cfg, err := Load("testdata/full.yaml")
	require.NoError(t, err)

	require.Equal(t, "openai-compatible", cfg.LLM.Provider)
	require.Equal(t, "llama3.1", cfg.LLM.Model)
	require.Equal(t, "MY_KEY", cfg.LLM.APIKeyEnv)
	require.Equal(t, "http://localhost:11434/v1", cfg.LLM.BaseURL)
	require.Equal(t, 4, cfg.LLM.MaxToolCallsPerGroup)
	require.Equal(t, 20000, cfg.LLM.MaxTokensPerGroup)
	require.Equal(t, 5, cfg.LLM.MaxFindingsInvestigated)
	require.Equal(t, 2*time.Minute, time.Duration(cfg.LLM.GlobalBudget))
}

func TestDefaultLLMShape(t *testing.T) {
	llm := Default().LLM
	require.Equal(t, "anthropic", llm.Provider)
	require.Equal(t, "claude-sonnet-5", llm.Model)
	require.Equal(t, "POIROT_LLM_API_KEY", llm.APIKeyEnv)
	require.Equal(t, "", llm.BaseURL)
	require.Equal(t, 8, llm.MaxToolCallsPerGroup)
	require.Equal(t, 40000, llm.MaxTokensPerGroup)
	require.Equal(t, 15, llm.MaxFindingsInvestigated)
	require.Equal(t, 5*time.Minute, time.Duration(llm.GlobalBudget))
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	_, err := Load("testdata/bad-provider.yaml")
	require.ErrorContains(t, err, "llm.provider")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	require.Error(t, err)
}

func TestValidateRejectsEmptyModelWithProvider(t *testing.T) {
	cfg := Default()
	cfg.LLM.Provider = "anthropic"
	cfg.LLM.Model = ""
	err := cfg.Validate()
	require.ErrorContains(t, err, "llm.model")
}

func TestValidateRejectsZeroGlobalBudget(t *testing.T) {
	cfg := Default()
	cfg.LLM.GlobalBudget = 0
	err := cfg.Validate()
	require.ErrorContains(t, err, "globalBudget")
}

func TestValidateRejectsLowMaxTokens(t *testing.T) {
	cfg := Default()
	cfg.LLM.MaxTokensPerGroup = 500
	err := cfg.Validate()
	require.ErrorContains(t, err, "maxTokensPerGroup")
}

func TestValidateRejectsZeroMaxToolCallsPerGroup(t *testing.T) {
	cfg := Default()
	cfg.LLM.MaxToolCallsPerGroup = 0
	err := cfg.Validate()
	require.ErrorContains(t, err, "maxToolCallsPerGroup")
}

func TestValidateRejectsZeroMaxFindingsInvestigated(t *testing.T) {
	cfg := Default()
	cfg.LLM.MaxFindingsInvestigated = 0
	err := cfg.Validate()
	require.ErrorContains(t, err, "maxFindingsInvestigated")
}

func TestValidateRejectsBadPromqlURL(t *testing.T) {
	cfg := Default()
	cfg.Connectors.PromQL.URL = "Auto" // case-sensitive: only "auto" is accepted
	err := cfg.Validate()
	require.ErrorContains(t, err, "connectors.promql.url")

	for _, ok := range []string{"auto", "disabled", "http://prom:9090", "https://prom.example/api"} {
		cfg.Connectors.PromQL.URL = ok
		require.NoError(t, cfg.Validate(), "URL %q must be accepted", ok)
	}
}

func TestOpenCostEstimateFallbackDefaultsTrue(t *testing.T) {
	cfg, err := loadYAML(t, "connectors:\n  opencost:\n    url: auto\n")
	require.NoError(t, err)
	require.NotNil(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.True(t, *cfg.Connectors.OpenCost.EstimateFallback)
}

func TestOpenCostEstimateFallbackFalseHonoured(t *testing.T) {
	cfg, err := loadYAML(t, "connectors:\n  opencost:\n    url: auto\n    estimateFallback: false\n")
	require.NoError(t, err)
	require.NotNil(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.False(t, *cfg.Connectors.OpenCost.EstimateFallback)
}

func TestValidateRejectsBadOpenCostURL(t *testing.T) {
	_, err := loadYAML(t, "connectors:\n  opencost:\n    url: Auto\n")
	require.ErrorContains(t, err, "connectors.opencost.url")
}

func TestValidateRejectsNonPositiveRates(t *testing.T) {
	_, err := loadYAML(t, "connectors:\n  opencost:\n    url: auto\n    rates:\n      cpuHour: 0\n      memGiBHour: 0.004\n")
	require.ErrorContains(t, err, "connectors.opencost.rates")
}
