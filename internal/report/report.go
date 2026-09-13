package report

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

type Counts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type CostMeta struct {
	Basis                 string  `json:"basis"`
	Currency              string  `json:"currency"`
	Window                string  `json:"window"`
	MonthlyTotal          float64 `json:"monthlyTotal"`
	EstimatedMonthlyWaste float64 `json:"estimatedMonthlyWaste"`
	Note                  string  `json:"note,omitempty"`
}

type Meta struct {
	Tool        string    `json:"tool"`
	Version     string    `json:"version"`
	GeneratedAt time.Time `json:"generatedAt"`
	Context     string    `json:"context"`
	Lookback    string    `json:"lookback"`
	Namespaces  []string  `json:"namespaces"`
	Counts      Counts    `json:"counts"`
	LLM         *LLMMeta  `json:"llm,omitempty"`
	Cost        *CostMeta `json:"cost,omitempty"`
}

type LLMMeta struct {
	Status        string   `json:"status"`
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	PromptVersion string   `json:"promptVersion"`
	InputTokens   int      `json:"inputTokens"`
	OutputTokens  int      `json:"outputTokens"`
	ToolCalls     int      `json:"toolCalls"`
	WallClockMs   int64    `json:"wallClockMs"`
	Warnings      []string `json:"warnings,omitempty"`
}

type Summary struct {
	Headline string   `json:"headline"`
	Actions  []string `json:"actions"`
}

type Report struct {
	Meta       Meta               `json:"meta"`
	Connectors []connector.Status `json:"connectors"`
	Findings   []analyzer.Finding `json:"findings"`
	Summary    *Summary           `json:"summary,omitempty"`
}

// Build assembles the canonical report: it sorts findings deterministically
// (severity rank desc, domain asc, object asc, ruleId asc), fills meta.Counts
// from the findings, and defaults Tool to "poirot". The caller's findings
// slice is never mutated.
func Build(meta Meta, statuses []connector.Status, findings []analyzer.Finding) Report {
	if meta.Tool == "" {
		meta.Tool = "poirot"
	}
	if meta.Namespaces == nil {
		meta.Namespaces = []string{}
	}

	conns := make([]connector.Status, len(statuses))
	copy(conns, statuses)

	sorted := make([]analyzer.Finding, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Object.String() != b.Object.String() {
			return a.Object.String() < b.Object.String()
		}
		return a.RuleID < b.RuleID
	})

	for _, f := range sorted {
		switch f.Severity {
		case analyzer.SeverityCritical:
			meta.Counts.Critical++
		case analyzer.SeverityWarning:
			meta.Counts.Warning++
		case analyzer.SeverityInfo:
			meta.Counts.Info++
		}
	}

	return Report{Meta: meta, Connectors: conns, Findings: sorted}
}

// JSON renders the report as indented JSON with a trailing newline.
func (r Report) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// worst returns 2 if any critical finding, 1 if any warning, else 0.
func (r Report) worst() int {
	switch {
	case r.Meta.Counts.Critical > 0:
		return 2
	case r.Meta.Counts.Warning > 0:
		return 1
	default:
		return 0
	}
}

// ExitCode maps the report to a process exit code given the failOn threshold.
func (r Report) ExitCode(failOn string) int {
	switch failOn {
	case "none":
		return 0
	case "warning":
		return r.worst()
	default: // "critical"
		if r.worst() == 2 {
			return 2
		}
		return 0
	}
}
