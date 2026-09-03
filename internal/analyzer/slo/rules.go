package slo

import (
	"fmt"
	"time"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

const (
	cpuWarn   = 0.90
	memWarn   = 0.90
	memCrit   = 0.98
	restartHr = 1.0
)

func ev(query string, value any, at time.Time) analyzer.Evidence {
	return analyzer.Evidence{Source: "promql", Query: query, Value: value, At: at}
}

func finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, e ...analyzer.Evidence) analyzer.Finding {
	return analyzer.Finding{RuleID: ruleID, Domain: "slo", Severity: sev, Title: title, Object: obj, Summary: summary, Evidence: e}
}

func podRef(labels map[string]string) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: labels["namespace"], Name: labels["pod"]}
}

func checkCPUSaturation(m *snapshot.MetricSet) []analyzer.Finding {
	r := m.Result("cpu_saturation")
	if r == nil {
		return nil
	}
	var out []analyzer.Finding
	for _, s := range r.Samples {
		if s.Value <= cpuWarn {
			continue
		}
		obj := podRef(s.Labels)
		out = append(out, finding("slo/cpu-saturation", analyzer.SeverityWarning, obj,
			"Container near its CPU limit",
			fmt.Sprintf("container %q in %s is at %.0f%% of its CPU limit", s.Labels["container"], obj, s.Value*100),
			ev(r.Expr, map[string]any{"labels": s.Labels, "ratio": s.Value}, m.CollectedAt),
		))
	}
	return out
}

func checkMemSaturation(m *snapshot.MetricSet) []analyzer.Finding {
	r := m.Result("mem_saturation")
	if r == nil {
		return nil
	}
	var out []analyzer.Finding
	for _, s := range r.Samples {
		if s.Value <= memWarn {
			continue
		}
		sev := analyzer.SeverityWarning
		if s.Value >= memCrit {
			sev = analyzer.SeverityCritical
		}
		obj := podRef(s.Labels)
		out = append(out, finding("slo/mem-saturation", sev, obj,
			"Container near its memory limit",
			fmt.Sprintf("container %q in %s is at %.0f%% of its memory limit", s.Labels["container"], obj, s.Value*100),
			ev(r.Expr, map[string]any{"labels": s.Labels, "ratio": s.Value}, m.CollectedAt),
		))
	}
	return out
}

func checkRestartRate(m *snapshot.MetricSet) []analyzer.Finding {
	r := m.Result("restart_rate")
	if r == nil {
		return nil
	}
	var out []analyzer.Finding
	for _, s := range r.Samples {
		if s.Value <= restartHr {
			continue
		}
		obj := podRef(s.Labels)
		out = append(out, finding("slo/restart-rate", analyzer.SeverityWarning, obj,
			"Pod restarting frequently",
			fmt.Sprintf("%s is restarting ~%.1f times/hour", obj, s.Value),
			ev(r.Expr, map[string]any{"labels": s.Labels, "perHour": s.Value}, m.CollectedAt),
		))
	}
	return out
}

func checkPodNotReady(m *snapshot.MetricSet) []analyzer.Finding {
	r := m.Result("pod_not_ready")
	if r == nil {
		return nil
	}
	var out []analyzer.Finding
	for _, s := range r.Samples {
		if s.Value < 1 {
			continue
		}
		obj := podRef(s.Labels)
		out = append(out, finding("slo/not-ready", analyzer.SeverityWarning, obj,
			"Pod not Ready (sustained, from metrics)",
			fmt.Sprintf("%s has not been Ready", obj),
			ev(r.Expr, map[string]any{"labels": s.Labels}, m.CollectedAt),
		))
	}
	return out
}

func checkTargetsDown(m *snapshot.MetricSet) []analyzer.Finding {
	r := m.Result("targets_down")
	if r == nil {
		return nil
	}
	var out []analyzer.Finding
	for _, s := range r.Samples {
		job, inst := s.Labels["job"], s.Labels["instance"]
		obj := analyzer.ObjectRef{Kind: "ScrapeTarget", Name: job + "/" + inst}
		out = append(out, finding("slo/target-down", analyzer.SeverityWarning, obj,
			"Scrape target down",
			fmt.Sprintf("scrape target %s (job %s) is down", inst, job),
			ev(r.Expr, map[string]any{"labels": s.Labels}, m.CollectedAt),
		))
	}
	return out
}

func checkQueryErrors(m *snapshot.MetricSet) []analyzer.Finding {
	var out []analyzer.Finding
	for _, r := range m.Results {
		if r.Error == "" {
			continue
		}
		out = append(out, finding("slo/query-error", analyzer.SeverityInfo,
			analyzer.ObjectRef{Kind: "MetricsBackend", Name: m.Backend},
			"Metrics query failed",
			fmt.Sprintf("query %q failed: %s", r.Name, r.Error),
			ev(r.Expr, r.Error, m.CollectedAt),
		))
	}
	return out
}
