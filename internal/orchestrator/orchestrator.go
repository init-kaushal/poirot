package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/analyzer/change"
	"github.com/init-kaushal/poirot/internal/analyzer/reliability"
	"github.com/init-kaushal/poirot/internal/analyzer/slo"
	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
	"github.com/init-kaushal/poirot/internal/connector/promql"
	"github.com/init-kaushal/poirot/internal/metrics"
	"github.com/init-kaushal/poirot/internal/report"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// K8sSource is the seam the orchestrator needs from the Kubernetes connector:
// the base Connector contract plus collection and the resolved context name.
type K8sSource interface {
	connector.Connector
	Collect(ctx context.Context) (*snapshot.Snapshot, error)
	ContextName() string
	Clientset() kubernetes.Interface
}

// Options configures a single Run.
type Options struct {
	Config  *config.Config
	Version string
	K8s     K8sSource           // nil => built from Config.Cluster + Config.Scope
	Promql  connector.Connector // nil => built from Config.Connectors.PromQL
}

// Result is the product of a Run: the canonical report and the process exit code.
type Result struct {
	Report   report.Report
	ExitCode int
}

// Run executes the M1 assessment lifecycle: discover → collect → analyze →
// synthesize. There is no investigate phase in M1.
func Run(ctx context.Context, opts Options) (*Result, error) {
	cfg := opts.Config

	src := opts.K8s
	if src == nil {
		built, err := k8s.New(
			cfg.Cluster.Kubeconfig,
			cfg.Cluster.Context,
			k8s.Scope{
				Namespaces: cfg.Scope.Namespaces,
				Exclude:    cfg.Scope.Exclude,
				Lookback:   time.Duration(cfg.Scope.Lookback),
			},
		)
		if err != nil {
			return nil, err
		}
		src = built
	}

	// 1. Discover
	reg := connector.NewRegistry()
	reg.Register(src)
	if cfg.Connectors.PromQL.URL != "disabled" {
		pc := opts.Promql
		if pc == nil {
			url := cfg.Connectors.PromQL.URL
			if url == "" {
				url = "auto"
			}
			pc = promql.New(promql.Options{
				URL:        url,
				Clientset:  src.Clientset(),
				Namespaces: cfg.Scope.Namespaces,
			})
		}
		reg.Register(pc)
	}
	statuses := reg.Probe(ctx)
	for _, s := range statuses {
		if s.Name == "k8s" && s.Availability.State != connector.StateAvailable {
			return nil, fmt.Errorf("kubernetes API not reachable: %s", s.Availability.Detail)
		}
	}

	// 2. Collect
	snap, err := src.Collect(ctx)
	if err != nil {
		return nil, fmt.Errorf("collect: %w", err)
	}
	if pc, ok := reg.Get("promql"); ok && reg.Satisfied([]string{"promql"}) {
		backend := "prometheus"
		if b, ok := pc.(interface{ Backend() string }); ok && b.Backend() != "" {
			backend = b.Backend()
		}
		snap.Metrics = metrics.Collect(ctx, pc, metrics.DefaultPack(), snap.Meta.CollectedAt, backend)
	}

	// 3. Analyze
	analyzers := []analyzer.Analyzer{reliability.New(), change.New(), slo.New()}
	var findings []analyzer.Finding
	for _, a := range analyzers {
		if !reg.Satisfied(a.Requires()) {
			findings = append(findings, analyzer.Finding{
				RuleID:   a.ID() + "/skipped",
				Domain:   a.ID(),
				Severity: analyzer.SeverityInfo,
				Title:    "Analyzer skipped",
				Summary: fmt.Sprintf(
					"%s analysis skipped — missing connector(s): %s",
					a.ID(), strings.Join(a.Requires(), ", "),
				),
			})
			continue
		}
		fs, err := a.Analyze(ctx, snap)
		if err != nil {
			return nil, fmt.Errorf("%s analyzer: %w", a.ID(), err)
		}
		findings = append(findings, fs...)
	}

	// 4. Synthesize
	meta := report.Meta{
		Version:     opts.Version,
		GeneratedAt: time.Now(),
		Context:     snap.Meta.Context,
		Lookback:    time.Duration(cfg.Scope.Lookback).String(),
		Namespaces:  snap.Meta.Namespaces,
	}
	rep := report.Build(meta, statuses, findings)
	return &Result{Report: rep, ExitCode: rep.ExitCode(cfg.Output.FailOn)}, nil
}
