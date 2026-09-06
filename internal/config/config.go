package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"
)

// Duration parses Go duration strings ("24h", "90m") from YAML/JSON.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Cluster struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
}

type Scope struct {
	Namespaces []string `json:"namespaces"`
	Exclude    []string `json:"exclude"`
	Lookback   Duration `json:"lookback"`
}

type ConnectorSpec struct {
	URL              string `json:"url"`
	Mode             string `json:"mode"`
	EstimateFallback bool   `json:"estimateFallback"`
}

type Connectors struct {
	PromQL       ConnectorSpec `json:"promql"`
	OpenCost     ConnectorSpec `json:"opencost"`
	Alertmanager ConnectorSpec `json:"alertmanager"`
	GitOps       ConnectorSpec `json:"gitops"`
}

type LLM struct {
	Provider                string   `json:"provider"`
	Model                   string   `json:"model"`
	APIKeyEnv               string   `json:"apiKeyEnv"`
	BaseURL                 string   `json:"baseURL"`
	MaxToolCallsPerGroup    int      `json:"maxToolCallsPerGroup"`
	MaxTokensPerGroup       int      `json:"maxTokensPerGroup"`
	MaxFindingsInvestigated int      `json:"maxFindingsInvestigated"`
	GlobalBudget            Duration `json:"globalBudget"`
}

type Output struct {
	Dir    string `json:"dir"`
	FailOn string `json:"failOn"`
}

type Config struct {
	Cluster    Cluster    `json:"cluster"`
	Scope      Scope      `json:"scope"`
	Connectors Connectors `json:"connectors"`
	Focus      []string   `json:"focus"`
	LLM        LLM        `json:"llm"`
	Output     Output     `json:"output"`
}

func Default() *Config {
	return &Config{
		Scope: Scope{
			Exclude:  []string{"kube-system", "kube-node-lease"},
			Lookback: Duration(24 * time.Hour),
		},
		Connectors: Connectors{
			PromQL:       ConnectorSpec{URL: "auto"},
			OpenCost:     ConnectorSpec{URL: "auto", EstimateFallback: true},
			Alertmanager: ConnectorSpec{URL: "auto"},
			GitOps:       ConnectorSpec{Mode: "auto"},
		},
		LLM: LLM{
			Provider:                "anthropic",
			Model:                   "claude-sonnet-5",
			APIKeyEnv:               "POIROT_LLM_API_KEY",
			BaseURL:                 "",
			MaxToolCallsPerGroup:    8,
			MaxTokensPerGroup:       40000,
			MaxFindingsInvestigated: 15,
			GlobalBudget:            Duration(5 * time.Minute),
		},
		Output: Output{Dir: "./poirot-out", FailOn: "critical"},
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults fills fields still at their zero value after unmarshal.
func (c *Config) applyDefaults() {
	d := Default()
	if c.Scope.Lookback == 0 {
		c.Scope.Lookback = d.Scope.Lookback
	}
	if c.Scope.Exclude == nil {
		c.Scope.Exclude = d.Scope.Exclude
	}
	if c.Connectors.PromQL.URL == "" {
		c.Connectors.PromQL.URL = d.Connectors.PromQL.URL
	}
	if c.Connectors.OpenCost.URL == "" {
		c.Connectors.OpenCost.URL = d.Connectors.OpenCost.URL
	}
	if c.Connectors.Alertmanager.URL == "" {
		c.Connectors.Alertmanager.URL = d.Connectors.Alertmanager.URL
	}
	if c.Connectors.GitOps.Mode == "" {
		c.Connectors.GitOps.Mode = d.Connectors.GitOps.Mode
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = d.LLM.Provider
	}
	if c.LLM.Model == "" {
		c.LLM.Model = d.LLM.Model
	}
	if c.LLM.APIKeyEnv == "" {
		c.LLM.APIKeyEnv = d.LLM.APIKeyEnv
	}
	if c.LLM.MaxToolCallsPerGroup == 0 {
		c.LLM.MaxToolCallsPerGroup = d.LLM.MaxToolCallsPerGroup
	}
	if c.LLM.MaxTokensPerGroup == 0 {
		c.LLM.MaxTokensPerGroup = d.LLM.MaxTokensPerGroup
	}
	if c.LLM.MaxFindingsInvestigated == 0 {
		c.LLM.MaxFindingsInvestigated = d.LLM.MaxFindingsInvestigated
	}
	if c.LLM.GlobalBudget == 0 {
		c.LLM.GlobalBudget = d.LLM.GlobalBudget
	}
	if c.Output.Dir == "" {
		c.Output.Dir = d.Output.Dir
	}
	if c.Output.FailOn == "" {
		c.Output.FailOn = d.Output.FailOn
	}
	// OpenCost.EstimateFallback defaults to true only when the whole opencost
	// block was omitted; if the user wrote an opencost block they opt in explicitly.
	// For M1 we always want the default-on behavior, so force it when unset via URL check above.
	if d.Connectors.OpenCost.EstimateFallback && c.Connectors.OpenCost.URL == "auto" {
		c.Connectors.OpenCost.EstimateFallback = true
	}
}

func (c *Config) Validate() error {
	switch c.LLM.Provider {
	case "anthropic", "openai-compatible", "none":
	default:
		return fmt.Errorf("llm.provider must be anthropic|openai-compatible|none, got %q", c.LLM.Provider)
	}
	switch c.Output.FailOn {
	case "none", "warning", "critical":
	default:
		return fmt.Errorf("output.failOn must be none|warning|critical, got %q", c.Output.FailOn)
	}
	if time.Duration(c.Scope.Lookback) <= 0 {
		return fmt.Errorf("scope.lookback must be positive, got %s", time.Duration(c.Scope.Lookback))
	}
	if c.LLM.Provider != "none" && c.LLM.Model == "" {
		return fmt.Errorf("llm.model is required unless llm.provider is none")
	}
	if time.Duration(c.LLM.GlobalBudget) <= 0 {
		return fmt.Errorf("llm.globalBudget must be positive, got %s", time.Duration(c.LLM.GlobalBudget))
	}
	if c.LLM.MaxTokensPerGroup < 1000 {
		return fmt.Errorf("llm.maxTokensPerGroup must be >= 1000, got %d", c.LLM.MaxTokensPerGroup)
	}
	if c.LLM.MaxToolCallsPerGroup < 1 {
		return fmt.Errorf("llm.maxToolCallsPerGroup must be >= 1, got %d", c.LLM.MaxToolCallsPerGroup)
	}
	if c.LLM.MaxFindingsInvestigated < 1 {
		return fmt.Errorf("llm.maxFindingsInvestigated must be >= 1, got %d", c.LLM.MaxFindingsInvestigated)
	}
	return nil
}
