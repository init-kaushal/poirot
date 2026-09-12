package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
	ocpkg "github.com/init-kaushal/poirot/internal/connector/opencost"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/metrics"
	"github.com/init-kaushal/poirot/internal/report"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func testConfig() *config.Config {
	c := config.Default()
	c.LLM.Provider = "none"
	// Keep M1-era tests hermetic: promql is always registered now, but URL
	// "disabled" makes Probe short-circuit to absent without any network call.
	// Tests that exercise the metrics/slo path opt back in with their own URL
	// and an injected fake (see promqlConfig).
	c.Connectors.PromQL.URL = "disabled"
	return c
}

// promqlConfig is testConfig with a non-"disabled" URL, so an injected fake
// promql connector (opts.Promql) is probed as if real.
func promqlConfig() *config.Config {
	c := testConfig()
	c.Connectors.PromQL.URL = "auto"
	return c
}

func TestRunProducesReliabilityReport(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
				Name: "api", RestartCount: 9,
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}}},
		},
	)
	src := k8s.NewWithClient(cs, "test-ctx", k8s.Scope{Lookback: 24 * time.Hour})

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: src})
	require.NoError(t, err)

	require.Equal(t, "test-ctx", res.Report.Meta.Context)
	require.GreaterOrEqual(t, res.Report.Meta.Counts.Critical, 1)
	require.Equal(t, 2, res.ExitCode) // default failOn=critical, a critical finding present

	var found bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "reliability/crashloop" {
			found = true
		}
	}
	require.True(t, found)
}

func TestRunFailsWhenK8sUnavailable(t *testing.T) {
	_, err := Run(context.Background(), Options{
		Config:  testConfig(),
		Version: "test",
		K8s:     unreachableK8s{},
	})
	require.ErrorContains(t, err, "not reachable")
}

// unreachableK8s stubs a k8s source whose API server cannot be reached.
type unreachableK8s struct{}

func (unreachableK8s) Name() string { return "k8s" }

func (unreachableK8s) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAbsent, Detail: "dial tcp: refused"}
}

func (unreachableK8s) Capabilities() []connector.Capability { return nil }

func (unreachableK8s) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func (unreachableK8s) Collect(context.Context) (*snapshot.Snapshot, error) {
	return nil, errors.New("unreachable")
}

func (unreachableK8s) ContextName() string { return "" }

func (unreachableK8s) Clientset() kubernetes.Interface { return fake.NewSimpleClientset() }

// fakePromql is a hermetic promql connector stub for orchestrator tests.
type fakePromql struct {
	state   connector.State
	samples map[string][]snapshot.MetricSample // keyed by pack entry Expr
}

func (fakePromql) Name() string { return "promql" }

func (f fakePromql) Probe(context.Context) connector.Availability {
	return connector.Availability{State: f.state}
}

func (fakePromql) Capabilities() []connector.Capability { return nil }

func (f fakePromql) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Expr string `json:"expr"`
	}
	_ = json.Unmarshal(args, &a)
	return json.Marshal(f.samples[a.Expr])
}

func (fakePromql) Backend() string { return "fake" }

func TestRunCollectsMetricsAndRunsSLO(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p"}},
	)
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})

	pack := metrics.DefaultPack()
	fp := fakePromql{state: connector.StateAvailable, samples: map[string][]snapshot.MetricSample{
		pack[1].Expr: {{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.99}},
	}}

	res, err := Run(context.Background(), Options{Config: promqlConfig(), Version: "t", K8s: src, Promql: fp})
	require.NoError(t, err)

	var found bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "slo/mem-saturation" {
			found = true
		}
	}
	require.True(t, found)
	require.GreaterOrEqual(t, res.Report.Meta.Counts.Critical, 1) // mem 0.99 => critical
}

// fakeLLM replays a scripted list of responses; each Complete pops the next and
// its parallel error. Past the end it returns a bare end_turn response.
type fakeLLM struct {
	responses []llm.Response
	errs      []error
	calls     int
}

func (f *fakeLLM) Model() string { return "fake" }

func (f *fakeLLM) Complete(context.Context, llm.Request) (llm.Response, error) {
	i := f.calls
	f.calls++
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	if i < len(f.responses) {
		return f.responses[i], err
	}
	return llm.Response{StopReason: "end_turn"}, err
}

// crashloopClientset stages one pod whose two containers each raise exactly one
// warning-or-worse reliability finding on the SAME object: "api" is in
// CrashLoopBackOff (reliability/crashloop, critical) and "sidecar" has 7
// restarts (reliability/restarts, warning). Same object => one investigate
// group => one Investigate LLM call; two analysed findings => Correlate fires.
func crashloopClientset() *fake.Clientset {
	return fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{
					Name: "api", RestartCount: 9,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
				},
				{
					Name: "sidecar", RestartCount: 7,
					State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
				},
			}},
		},
	)
}

func newCrashloopSrc() K8sSource {
	return k8s.NewWithClient(crashloopClientset(), "test-ctx", k8s.Scope{Lookback: 24 * time.Hour})
}

func TestRunFullLLMPath(t *testing.T) {
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"reliability/crashloop","probableCause":"bad env","confidence":"high","remediation":"fix it"}]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"correlations":[]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"headline":"Cluster is degraded","actions":["Restart api-1"]}`}}},
	}}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc(), LLM: l})
	require.NoError(t, err)

	var crash *analyzer.Finding
	for i := range res.Report.Findings {
		if res.Report.Findings[i].RuleID == "reliability/crashloop" {
			crash = &res.Report.Findings[i]
		}
	}
	require.NotNil(t, crash, "crashloop finding must be present")
	require.NotNil(t, crash.Analysis, "crashloop finding must be enriched")
	require.Equal(t, "bad env", crash.Analysis.ProbableCause)

	require.NotNil(t, res.Report.Summary)
	require.NotNil(t, res.Report.Meta.LLM)
	require.Equal(t, "ok", res.Report.Meta.LLM.Status)
	require.Regexp(t, `^v\d+\+`, res.Report.Meta.LLM.PromptVersion)
}

// I7: a non-positive GlobalBudget means "no phase timeout", not an
// already-expired context. A library caller building config.Config directly
// (bypassing config.Validate) must still get a working LLM phase.
func TestRunZeroGlobalBudgetStillRunsLLMPhase(t *testing.T) {
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"reliability/crashloop","probableCause":"bad env","confidence":"high","remediation":"fix it"}]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"correlations":[]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"headline":"Cluster is degraded","actions":["Restart api-1"]}`}}},
	}}
	cfg := testConfig()
	cfg.LLM.GlobalBudget = 0

	res, err := Run(context.Background(), Options{Config: cfg, Version: "test", K8s: newCrashloopSrc(), LLM: l})
	require.NoError(t, err)
	require.NotNil(t, res.Report.Meta.LLM)
	require.Equal(t, "ok", res.Report.Meta.LLM.Status, "LLM phase runs rather than every group stubbed")

	var crash *analyzer.Finding
	for i := range res.Report.Findings {
		if res.Report.Findings[i].RuleID == "reliability/crashloop" {
			crash = &res.Report.Findings[i]
		}
	}
	require.NotNil(t, crash)
	require.NotNil(t, crash.Analysis)
	require.Equal(t, "bad env", crash.Analysis.ProbableCause)
}

func TestRunNoAPIKey(t *testing.T) {
	cfg := testConfig()
	cfg.LLM.Provider = "anthropic"
	t.Setenv(cfg.LLM.APIKeyEnv, "") // APIKeyEnv defaults to POIROT_LLM_API_KEY

	res, err := Run(context.Background(), Options{Config: cfg, Version: "test", K8s: newCrashloopSrc()})
	require.NoError(t, err)

	for _, f := range res.Report.Findings {
		require.Nil(t, f.Analysis, "no finding may be enriched when the LLM phase is skipped")
	}
	require.Nil(t, res.Report.Summary)
	require.NotNil(t, res.Report.Meta.LLM)
	require.Equal(t, "skipped: no api key", res.Report.Meta.LLM.Status)

	// A provider:"none" run of identical inputs is the deterministic baseline.
	det, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc()})
	require.NoError(t, err)
	require.Equal(t, "disabled", det.Report.Meta.LLM.Status)
	require.Equal(t, det.ExitCode, res.ExitCode)

	// Per Ruling 5: the two reports differ ONLY in Meta.LLM.Status. Once the
	// demarcated non-deterministic surface is normalised on both — Meta.LLM,
	// plus the wall-clock timestamps a live collection stamps (Meta.GeneratedAt
	// and each Evidence.At, which flow from snapshot CollectedAt = time.Now())
	// — the JSON must be byte-identical.
	aj, err := normalizedJSON(res.Report)
	require.NoError(t, err)
	bj, err := normalizedJSON(det.Report)
	require.NoError(t, err)
	require.Equal(t, string(bj), string(aj))
}

// normalizedJSON strips the demarcated non-deterministic surface (Meta.LLM and
// every collection-stamped timestamp) so two independent live Runs of identical
// inputs can be compared byte-for-byte. It operates on a deep-enough copy so the
// caller's report (its Findings and Evidence slices) is never mutated.
func normalizedJSON(r report.Report) ([]byte, error) {
	r.Meta.LLM = nil
	r.Meta.GeneratedAt = time.Time{}

	findings := make([]analyzer.Finding, len(r.Findings))
	copy(findings, r.Findings)
	for i := range findings {
		ev := make([]analyzer.Evidence, len(findings[i].Evidence))
		copy(ev, findings[i].Evidence)
		for j := range ev {
			ev[j].At = time.Time{}
		}
		findings[i].Evidence = ev
	}
	r.Findings = findings
	return r.JSON()
}

func TestRunLLMErrorMidInvestigate(t *testing.T) {
	// First Complete succeeds (bare end_turn => unparseable answer), the parse
	// retry (2nd Complete) errors => the group is stubbed, not enriched.
	l := &fakeLLM{errs: []error{nil, errors.New("boom")}}

	det, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc()})
	require.NoError(t, err)

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc(), LLM: l})
	require.NoError(t, err, "an LLM error must never fail Run")

	require.NotNil(t, res.Report.Meta.LLM)
	require.Regexp(t, `^partial`, res.Report.Meta.LLM.Status)
	require.Equal(t, det.ExitCode, res.ExitCode, "enrichment must not change the exit code")

	var enriched int
	for _, f := range res.Report.Findings {
		if f.Analysis != nil {
			enriched++
		}
	}
	require.GreaterOrEqual(t, enriched, 1, "at least one finding must carry a stub Analysis")
}

func TestRunPopulatesCorrelatedFindings(t *testing.T) {
	// crashloopClientset yields two analysed findings on one object:
	// reliability/crashloop (critical) and reliability/restarts (warning).
	// Object.String() == "Pod/payments/api-1", so refKey ==
	// "<ruleId>@Pod/payments/api-1" (see agent.refKey / correlate.go).
	const crashKey = "reliability/crashloop@Pod/payments/api-1"
	const restartKey = "reliability/restarts@Pod/payments/api-1"

	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[` +
			`{"ruleId":"reliability/crashloop","probableCause":"bad env","confidence":"high","remediation":"fix env"},` +
			`{"ruleId":"reliability/restarts","probableCause":"same bad env","confidence":"medium","remediation":"fix env"}]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"correlations":[` +
			`{"key":"` + crashKey + `","related":["` + restartKey + `"]}]}`}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"headline":"degraded","actions":["restart"]}`}}},
	}}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc(), LLM: l})
	require.NoError(t, err)

	var crash *analyzer.Finding
	for i := range res.Report.Findings {
		if res.Report.Findings[i].RuleID == "reliability/crashloop" {
			crash = &res.Report.Findings[i]
		}
	}
	require.NotNil(t, crash)
	require.NotNil(t, crash.Analysis)
	require.NotEmpty(t, crash.Analysis.CorrelatedFindings, "correlate pass must populate CorrelatedFindings")
	require.Contains(t, crash.Analysis.CorrelatedFindings, restartKey)
}

func TestRunNoAPIKeyRendersBanner(t *testing.T) {
	cfg := testConfig()
	cfg.LLM.Provider = "anthropic"
	t.Setenv(cfg.LLM.APIKeyEnv, "")

	res, err := Run(context.Background(), Options{Config: cfg, Version: "test", K8s: newCrashloopSrc()})
	require.NoError(t, err)
	require.Equal(t, "skipped: no api key", res.Report.Meta.LLM.Status)
	md, err := res.Report.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(md), "> ⚠️ AI analysis skipped: no api key")

	// provider:"none" is not a degradation — no banner.
	det, err := Run(context.Background(), Options{Config: testConfig(), Version: "test", K8s: newCrashloopSrc()})
	require.NoError(t, err)
	dmd, err := det.Report.Markdown()
	require.NoError(t, err)
	require.NotContains(t, string(dmd), "⚠️ AI analysis")
}

// fakeOpencost is a hermetic opencost connector stub returning canned
// allocation JSON, switching on the "window" arg like fakePromql does on expr.
type fakeOpencost struct{}

func (fakeOpencost) Name() string { return "opencost" }

func (fakeOpencost) Probe(context.Context) connector.Availability {
	return connector.Availability{State: connector.StateAvailable}
}

func (fakeOpencost) Capabilities() []connector.Capability { return nil }

func (fakeOpencost) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct {
		Window string `json:"window"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Window == "14d" {
		steps := [][]ocpkg.Allocation{
			{{Namespace: "team", ControllerKind: "", TotalCost: 40}},
			{{Namespace: "team", ControllerKind: "", TotalCost: 80}},
		}
		return json.Marshal(steps)
	}
	steps := [][]ocpkg.Allocation{{{
		Namespace: "team", Controller: "web", ControllerKind: "deployment",
		CPUCoreRequest: 1, CPUCoreUsage: 0.05, RAMByteRequest: 1 << 30, RAMByteUsage: 1 << 27,
		CPUCost: 20, RAMCost: 5, TotalCost: 25,
	}}}
	return json.Marshal(steps)
}

func costWorkloadSrc() K8sSource {
	replicas := int32(2)
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		},
	)
	return k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})
}

func TestRunPopulatesCostMetaMeasured(t *testing.T) {
	cfg := testConfig()
	cfg.Connectors.OpenCost.URL = "auto" // explicit for clarity — config.Default() already sets "auto"

	res, err := Run(context.Background(), Options{Config: cfg, Version: "t", K8s: costWorkloadSrc(), OpenCost: fakeOpencost{}})
	require.NoError(t, err)
	require.NotNil(t, res.Report.Meta.Cost)
	require.Equal(t, "measured", res.Report.Meta.Cost.Basis)

	// Pin the join: MonthlyTotal is derived by summing snap.Cost.Workloads, so
	// this can only be (20+5)*30/7 if the fixture's team/web Deployment was
	// correctly joined (by Namespace+Controller+ControllerKind) to the
	// allocation row {CPUCost:20, RAMCost:5}. With no measured workloads
	// (a silently broken join), MonthlyTotal would sum an empty slice => 0.
	require.InDelta(t, (20.0+5.0)*30.0/7.0, res.Report.Meta.Cost.MonthlyTotal, 0.01,
		"MonthlyTotal must reflect the joined team/web workload's allocation row, not an empty measured set")

	var hasCost bool
	var hasWebFinding bool
	for _, f := range res.Report.Findings {
		if f.Domain == "cost" {
			hasCost = true
		}
		if f.Object.Namespace == "team" && f.Object.Name == "web" {
			hasWebFinding = true
		}
	}
	require.True(t, hasCost, "at least one cost/* finding (even cost/skipped-adjacent rules) must be present")
	require.True(t, hasWebFinding, "a finding referencing the joined team/web workload must be present")
}

func TestRunCostEstimateWhenNoOpenCost(t *testing.T) {
	cfg := testConfig()
	cfg.Connectors.OpenCost.URL = "auto" // no fake injected -> real opencost.New discovers nothing on this clientset -> absent -> estimate

	res, err := Run(context.Background(), Options{Config: cfg, Version: "t", K8s: costWorkloadSrc()})
	require.NoError(t, err)
	require.NotNil(t, res.Report.Meta.Cost)
	require.Equal(t, "estimated", res.Report.Meta.Cost.Basis)
}

func TestRunNoLLMStillByteStableWithCost(t *testing.T) {
	a, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: newCrashloopSrc()})
	require.NoError(t, err)
	b, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: newCrashloopSrc()})
	require.NoError(t, err)
	require.Equal(t, "disabled", a.Report.Meta.LLM.Status)
	aj, err := normalizedJSON(a.Report)
	require.NoError(t, err)
	bj, err := normalizedJSON(b.Report)
	require.NoError(t, err)
	require.Equal(t, string(aj), string(bj))
}

func TestRunSkipsSLOWhenPromqlAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})
	fp := fakePromql{state: connector.StateAbsent}

	// promql is registered (URL != "disabled") but probes absent, so slo must
	// report as skipped via reg.Satisfied(["promql"]) == false.
	res, err := Run(context.Background(), Options{Config: promqlConfig(), Version: "t", K8s: src, Promql: fp})
	require.NoError(t, err)

	var skipped bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "slo/skipped" {
			skipped = true
		}
	}
	require.True(t, skipped)
}
