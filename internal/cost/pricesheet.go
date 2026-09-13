package cost

import (
	_ "embed"
	"fmt"
	sigsyaml "sigs.k8s.io/yaml"
)

//go:embed prices.yaml
var pricesYAML []byte

type Rate struct {
	CPUHour    float64 `json:"cpuHour"`
	MemGiBHour float64 `json:"memGiBHour"`
}

type Sheet struct {
	Default        Rate            `json:"default"`
	ByInstanceType map[string]Rate `json:"byInstanceType"`
	override       *Rate
}

func Load() (*Sheet, error) {
	var s Sheet
	if err := sigsyaml.Unmarshal(pricesYAML, &s); err != nil {
		return nil, fmt.Errorf("parse embedded price sheet: %w", err)
	}
	if s.Default.CPUHour <= 0 || s.Default.MemGiBHour <= 0 {
		return nil, fmt.Errorf("embedded price sheet has non-positive default rate")
	}
	return &s, nil
}

func (s *Sheet) SetOverride(r *Rate) {
	if r != nil {
		s.override = r
	}
}

func (s *Sheet) Rate(instanceType string) Rate {
	if s.override != nil {
		return *s.override
	}
	if r, ok := s.ByInstanceType[instanceType]; ok {
		return r
	}
	return s.Default
}
