package analyzer

import (
	"context"
	"fmt"
	"time"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Rank returns a sortable severity rank; higher is more severe.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

type ObjectRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

func (o ObjectRef) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s/%s", o.Kind, o.Name)
	}
	return fmt.Sprintf("%s/%s/%s", o.Kind, o.Namespace, o.Name)
}

type Evidence struct {
	Source string    `json:"source"`
	Query  string    `json:"query"`
	Value  any       `json:"value"`
	At     time.Time `json:"at"`
}

type Analysis struct {
	ProbableCause      string   `json:"probableCause"`
	CorrelatedFindings []string `json:"correlatedFindings,omitempty"`
	Confidence         string   `json:"confidence"`
	Remediation        string   `json:"remediation"`
}

type Finding struct {
	RuleID   string     `json:"ruleId"`
	Domain   string     `json:"domain"`
	Severity Severity   `json:"severity"`
	Title    string     `json:"title"`
	Object   ObjectRef  `json:"object"`
	Evidence []Evidence `json:"evidence"`
	Summary  string     `json:"summary"`
	Analysis *Analysis  `json:"analysis,omitempty"`
}

// Analyzer is a deterministic, pure check over a Snapshot.
type Analyzer interface {
	ID() string
	Requires() []string // connector names that must be available
	Analyze(ctx context.Context, snap *snapshot.Snapshot) ([]Finding, error)
}
