package orchestrator

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/init-kaushal/poirot/internal/agent"
	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/analyzer/change"
	analyzercost "github.com/init-kaushal/poirot/internal/analyzer/cost"
	"github.com/init-kaushal/poirot/internal/analyzer/reliability"
	"github.com/init-kaushal/poirot/internal/analyzer/slo"
	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
	"github.com/init-kaushal/poirot/internal/connector/opencost"
	"github.com/init-kaushal/poirot/internal/connector/promql"
	"github.com/init-kaushal/poirot/internal/cost"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/llm/anthropic"
	"github.com/init-kaushal/poirot/internal/llm/openaicompat"
	"github.com/init-kaushal/poirot/internal/llm/prompts"
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
	Config   *config.Config
	Version  string
	K8s      K8sSource           // nil => built from Config.Cluster + Config.Scope
	Promql   connector.Connector // nil => built from Config.Connectors.PromQL
	OpenCost connector.Connector // nil => built from Config.Connectors.OpenCost
	// LLM overrides the provider built from Config.LLM (test seam). When set, it
	// is used even if Config.LLM.Provider is "none"; Meta.LLM then reflects the
	// injected client (its Model()), not the config.
	LLM llm.LLM
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

	// 1. Discover. The promql connector is always registered — a "disabled" URL
	// makes it probe as absent ("disabled in config"), so the connector table
	// lists it as disabled rather than omitting it, and reg.Satisfied(["promql"])
	// is still false so the slo/skipped info-finding fires.
	reg := connector.NewRegistry()
	reg.Register(src)
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
	oc := opts.OpenCost
	if oc == nil {
		url := cfg.Connectors.OpenCost.URL
		if url == "" {
			url = "auto"
		}
		oc = opencost.New(opencost.Options{
			URL:        url,
			Clientset:  src.Clientset(),
			Namespaces: cfg.Scope.Namespaces,
		})
	}
	reg.Register(oc)
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
	snap.Cost = cost.Collect(ctx, snap, reg, cfg.Connectors.OpenCost)

	// 3. Analyze
	analyzers := []analyzer.Analyzer{reliability.New(), change.New(), slo.New(), analyzercost.New()}
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

	// 4-6. Investigate → Correlate → Synthesize. Optional and never fatal:
	// every failure mode degrades into llmMeta.Status / llmMeta.Warnings.
	var summary *report.Summary
	var llmMeta *report.LLMMeta

	l := opts.LLM
	if l == nil {
		var reason string
		l, reason = buildLLM(cfg)
		if l == nil {
			// Skipped: record ONLY the reason (Ruling 5 — a no-key run and a
			// provider:"none" run then differ solely in Meta.LLM.Status).
			llmMeta = &report.LLMMeta{Status: reason}
		}
	}
	if l != nil {
		findings, summary, llmMeta = runLLMPhase(ctx, l, cfg, reg, snap, statuses, findings)
	}

	// 7. Assemble
	meta := report.Meta{
		Version:     opts.Version,
		GeneratedAt: time.Now(),
		Context:     snap.Meta.Context,
		Lookback:    time.Duration(cfg.Scope.Lookback).String(),
		Namespaces:  snap.Meta.Namespaces,
	}
	rep := report.Build(meta, statuses, findings)
	rep.Summary = summary
	rep.Meta.LLM = llmMeta
	if snap.Cost != nil {
		rep.Meta.Cost = cost.From(snap.Cost, rep.Findings)
	}
	return &Result{Report: rep, ExitCode: rep.ExitCode(cfg.Output.FailOn)}, nil
}

// runLLMPhase runs phases 4-6 (Investigate → Correlate → Synthesize) against a
// resolved client under a single global-budget context. It is a pure refactor
// of the block that used to live inline in Run: it never fails, folding every
// error mode into meta.Status / meta.Warnings.
func runLLMPhase(
	ctx context.Context,
	l llm.LLM,
	cfg *config.Config,
	reg *connector.Registry,
	snap *snapshot.Snapshot,
	statuses []connector.Status,
	findings []analyzer.Finding,
) ([]analyzer.Finding, *report.Summary, *report.LLMMeta) {
	meta := &report.LLMMeta{Status: "ok", Provider: cfg.LLM.Provider, Model: l.Model()}
	if _, pv, err := prompts.Load("investigate_system"); err == nil {
		meta.PromptVersion = pv
	}

	// A non-positive GlobalBudget means "no phase timeout", not an
	// already-expired context. config.Validate rejects <= 0, but only inside
	// Load — a library caller building config.Config directly (which Options
	// supports) would otherwise get silent total LLM failure.
	var bctx context.Context
	var cancel context.CancelFunc
	if d := time.Duration(cfg.LLM.GlobalBudget); d > 0 {
		bctx, cancel = context.WithTimeout(ctx, d)
	} else {
		bctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	start := time.Now()
	var summary *report.Summary

	tp := agent.NewInProcessToolProvider(reg, snap, findings)
	investigated, im := agent.Investigate(bctx, l, tp, findings, agent.InvestigateConfig{
		MaxGroups:        cfg.LLM.MaxFindingsInvestigated,
		Budget:           agent.Budget{MaxToolCalls: cfg.LLM.MaxToolCallsPerGroup, MaxTokens: cfg.LLM.MaxTokensPerGroup},
		MaxTokensPerCall: 4096,
	})
	findings = investigated

	// Once the global-budget context is done, Investigate has already stubbed
	// every remaining group; Correlate and Synthesize would only issue doomed
	// calls and append error-warnings, so skip them.
	var cw, sw []string
	var hl string
	var acts []string
	if bctx.Err() == nil {
		var corr []analyzer.Finding
		corr, cw = agent.Correlate(bctx, l, findings, 4096)
		findings = corr

		var sc agent.SynthCounts
		for _, f := range findings {
			switch f.Severity {
			case analyzer.SeverityCritical:
				sc.Critical++
			case analyzer.SeverityWarning:
				sc.Warning++
			case analyzer.SeverityInfo:
				sc.Info++
			}
		}
		hl, acts, sw = agent.Synthesize(bctx, l, findings, sc, connectorNames(statuses), 4096)
	}

	meta.InputTokens = im.InputTokens
	meta.OutputTokens = im.OutputTokens
	meta.ToolCalls = im.ToolCalls
	if w := append(append(append([]string{}, im.Warnings...), cw...), sw...); len(w) > 0 {
		meta.Warnings = w
	}
	meta.WallClockMs = time.Since(start).Milliseconds()
	if hl != "" {
		summary = &report.Summary{Headline: hl, Actions: acts}
	}
	if bctx.Err() == context.DeadlineExceeded {
		meta.Status = "partial: global budget exceeded"
	} else if len(meta.Warnings) > 0 && meta.Status == "ok" {
		meta.Status = "partial: see warnings"
	}
	return findings, summary, meta
}

// connectorNames returns the sorted connector status names.
func connectorNames(ss []connector.Status) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// buildLLM resolves cfg.LLM into a client. It returns (nil, reason) on every
// skip path and (client, "") on success — it never returns an error, so a
// misconfigured LLM block degrades the phase instead of failing Run.
func buildLLM(cfg *config.Config) (llm.LLM, string) {
	if cfg.LLM.Provider == "none" {
		return nil, "disabled"
	}
	key := os.Getenv(cfg.LLM.APIKeyEnv)
	if cfg.LLM.Provider != "openai-compatible" && key == "" {
		return nil, "skipped: no api key"
	}
	if cfg.LLM.Provider == "openai-compatible" && cfg.LLM.BaseURL == "" {
		return nil, "skipped: no baseURL"
	}
	switch cfg.LLM.Provider {
	case "anthropic":
		c, err := anthropic.New(anthropic.Options{APIKey: key, Model: cfg.LLM.Model, BaseURL: cfg.LLM.BaseURL})
		if err != nil {
			return nil, "skipped: " + err.Error()
		}
		return c, ""
	case "openai-compatible":
		c, err := openaicompat.New(openaicompat.Options{APIKey: key, Model: cfg.LLM.Model, BaseURL: cfg.LLM.BaseURL})
		if err != nil {
			return nil, "skipped: " + err.Error()
		}
		return c, ""
	}
	return nil, "disabled"
}
