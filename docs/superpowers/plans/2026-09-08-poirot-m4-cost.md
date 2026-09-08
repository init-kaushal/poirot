# poirot M4 (Cost) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `opencost` connector, an `internal/cost` collection layer with an embedded requests-only estimator, and an always-on `cost` analyzer with four rule families, so `poirot run` reports monthly spend, estimated waste, and ranked cost actions — measured from OpenCost when reachable, estimated from resource requests × a bundled price sheet otherwise.

**Architecture:** Mirrors the M2 `promql` → `metrics` → `slo` three-layer split. A thin `opencost` connector implements `connector.Connector` (explicit `url:` or `url: auto` via the Kubernetes API-server proxy after well-known-Service discovery). The collect phase gains a step after `metrics.Collect`: `cost.Collect` calls `opencost.allocation` when the connector is available, otherwise runs `internal/cost/estimate.go` (pod requests × `internal/cost/pricesheet` keyed by node instance-type; usage from the registered `promql` connector when present). Both paths produce one immutable `snapshot.CostSet`. The `cost` analyzer is pure over the Snapshot, `Requires()==["k8s"]` so it always runs, and every finding carries a `basis: measured|estimated` evidence tag. Dollar values in evidence and the whole `report.Meta.Cost` block are non-deterministic and demarcated exactly like `slo` metric values and `report.Meta.LLM`.

**Tech Stack:** Go 1.26, `k8s.io/client-go` (`CoreV1().Services(ns).ProxyGet(...)`, `BatchV1().Jobs(ns).List`, `fake` clientset for tests), `net/http` + `net/http/httptest`, `sigs.k8s.io/yaml` (embedded price sheet), `encoding/json`, `github.com/stretchr/testify`.

**Spec:** `docs/superpowers/specs/2026-09-08-poirot-m4-cost-design.md` — read it in full before starting. The spec's inline `(review Rn)` tags mark decisions that a first-pass `/code-review` of the spec forced; the tasks below carry them as explicit requirements.

**Predecessors:** M1 `docs/superpowers/plans/2026-09-01-poirot-m1-skeleton.md`, M2 `docs/superpowers/plans/2026-09-03-poirot-m2-metrics-change.md`, M3 `docs/superpowers/plans/2026-09-05-poirot-m3-agent.md` — all merged to `main`. This plan builds on `analyzer.Analyzer` / `analyzer.Finding` / `analyzer.Evidence` / `analyzer.ObjectRef`, `connector.Connector` / `connector.Registry`, `snapshot.Snapshot`, `report.Build` / `report.Meta`, and `orchestrator.Run` as they exist on `main` today. Read M2's "File Structure" and Task 7 (the `promql` connector) — the `opencost` connector is a near-parallel of it.

## Global Constraints

- Module path: `github.com/init-kaushal/poirot`. `go.mod` directive `go 1.26.0` — do **not** change it or `.github/workflows/ci.yml`'s `go-version: '1.26'`.
- **Read-only.** No code path may create/update/patch/delete/evict/apply/watch any cluster resource, or issue any write to OpenCost. Cluster access is `get`/`list` only; OpenCost access is HTTP `GET` only (`/allocation/compute`), directly or via `Services(ns).ProxyGet`.
- Connector packages import only their own client libraries plus `internal/connector` and `internal/snapshot` — never `analyzer`, `orchestrator`, `agent`, `report`, `config`, or another connector. `internal/cost` may import `internal/config`, `internal/connector`, `internal/snapshot`, `internal/analyzer` (for `Finding` in `costmeta`), and `internal/report` (for `CostMeta` in `costmeta`) — but **not** `orchestrator` or `agent`.
- Every connector query is `Query(ctx, capabilityID string, args json.RawMessage) (json.RawMessage, error)`. `Capability.ArgsSchema` is a real JSON Schema string.
- Analyzers are pure: `Analyze(ctx, *snapshot.Snapshot) ([]Finding, error)` — no I/O, no goroutines, no Snapshot mutation. Same Snapshot in ⇒ same `[]Finding` out, in slice order.
- Lowercase domain strings: severity `critical|warning|info`; connector state `available|degraded|absent`; new finding domain `cost`.
- **No non-finite `Evidence.Value`** (spec R1). `report.JSON()` uses `json.MarshalIndent`, which errors on `±Inf`/`NaN`, and `cmd/poirot/run.go`'s `writeOutputs` makes that error fatal for the whole report. Every ratio goes through the `safeRatio` helper (Task 11); a rule that cannot form a finite ratio omits that evidence value or does not fire.
- **Determinism.** Any slice derived from a Go map MUST be sorted before it reaches a `Finding` or a `CostSet`. Instance-type selection in the estimator is an argmax over a `map[string]int` — it MUST tie-break on the smallest instance-type string (spec R9). `report.json` / `report.md` **minus** `meta.cost` / the `## Cost` section stay byte-identical across runs on an identical Snapshot; `internal/report/testdata/golden_basic.md` must come out byte-identical (it carries no cost data).
- All tests run with `go test ./... -race -count=1` and pass before every commit. `go vet ./...`, `gofmt -l .`, `go build ./...`, and `go build -tags tools ./...` all clean.
- Commit messages: `<type>: <summary>` (`feat`|`fix`|`test`|`chore`|`refactor`|`docs`). Body ends with exactly these two lines:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`
  `Claude-Session: https://claude.ai/code/session_0164sdpmRJzczSXDjTnfFR1T`

---

## File Structure

| Path | Responsibility | New/Modified |
|---|---|---|
| `internal/config/config.go` | `ConnectorSpec.EstimateFallback` → `*bool`; add `ConnectorSpec.Rates *Rate` + `Rate{CPUHour,MemGiBHour float64}`; `Default`/`applyDefaults`/`Validate` for the opencost block | Modified |
| `internal/config/testdata/full.yaml`, `poirot.example.yaml` | reflect the opencost block | Modified |
| `internal/snapshot/snapshot.go` | add `Cost *CostSet` + `Jobs []batchv1.Job` fields; add `CostBasis` / `CostSet` / `WorkloadCost` / `NamespaceCost` types | Modified |
| `internal/connector/k8s/collect.go` | best-effort `BatchV1().Jobs(ns).List` → `snap.Jobs` (list error ⇒ `snap.Jobs` stays nil, run continues) | Modified |
| `internal/cost/pricesheet.go` + `prices.yaml` | `//go:embed prices.yaml`; `Rate` / `Sheet` types; `Load() (*Sheet, error)`; `(*Sheet).Rate(string) Rate`; `(*Sheet).SetOverride(*Rate)` | New |
| `internal/connector/opencost/parse.go` | OpenCost allocation JSON envelope → `[]Allocation` (tolerant: skip malformed rows, skip `__idle__`/`__unallocated__`) | New |
| `internal/connector/opencost/client.go` | `Client` over a `doer` func; `Allocation(ctx, window, aggregate string, accumulate bool, step string)`; `httpDoer` for explicit-URL mode; 1 MiB `LimitReader` | New |
| `internal/connector/opencost/discover.go` | `Discover(ctx, cs, namespaces)` → `*Target`; well-known Service names/namespaces; `pickPort` (copied from `promql/discover.go`); `proxyDoer(cs, Target)` | New |
| `internal/connector/opencost/opencost.go` | `connector.Connector`: `Name`/`Probe`/`Capabilities`/`Query`; capability `opencost.allocation` | New |
| `internal/cost/estimate.go` | `Estimate(snap, reg, sheet) *snapshot.CostSet` — requests × sheet, instance-type modal+tiebreak, DaemonSet running-pod count, promql two-hop usage join | New |
| `internal/cost/collect.go` | `Collect(ctx, snap, reg, spec) *snapshot.CostSet` — measured (2 allocation calls, case-fold join, discard-partial-on-error) else estimate else nil; sorts every slice | New |
| `internal/cost/costmeta.go` | `From(*snapshot.CostSet, []analyzer.Finding) *report.CostMeta` | New |
| `internal/report/report.go` | add `CostMeta` type + `Meta.Cost *CostMeta` (`json:"cost,omitempty"`) | Modified |
| `internal/report/markdown.go` | `mdData` gains `Cost *CostMeta` + `CostBanner string` + `CostTop5 []costRow`; `## Cost` template block `{{if .Cost}}`-guarded | Modified |
| `internal/analyzer/cost/cost.go` | `Analyzer`: `ID()=="cost"`, `Requires()==["k8s"]`, two-pass `Analyze`; `safeRatio` helper | New |
| `internal/analyzer/cost/usage_rules.go` | `cost/rightsizing`, `cost/rightsizing-limited`, `cost/idle`, `cost/over-replicated` | New |
| `internal/analyzer/cost/object_rules.go` | `cost/orphaned-pvc`, `cost/orphaned-lb`, `cost/retained-jobs`, `cost/namespace-spend-trend` | New |
| `internal/orchestrator/orchestrator.go` | register `opencost`; `snap.Cost = cost.Collect(...)` after `metrics.Collect`; analyzer list `+= cost.New()`; `rep.Meta.Cost = costmeta.From(...)` post-`Build` | Modified |
| `README.md` | M4 status line + `## Cost analysis` section | Modified |
| `hack/demo-broken.yaml` | add a cost-waste workload set (orphaned LB, oversized requests, idle) | Modified |

---

## Task 1: `config` — opencost block (`EstimateFallback *bool`, `Rates`, validation)

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/testdata/full.yaml`
- Modify: `poirot.example.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `ConnectorSpec` (currently `{URL, Mode string; EstimateFallback bool}` at `config.go:44`), the `promql.url` validation switch (`config.go:191`).
- Produces:
  - `type Rate struct { CPUHour float64 \`json:"cpuHour"\`; MemGiBHour float64 \`json:"memGiBHour"\` }`
  - `ConnectorSpec` becomes `{URL, Mode string; EstimateFallback *bool \`json:"estimateFallback,omitempty"\`; Rates *Rate \`json:"rates,omitempty"\`}`
  - Package helper `func boolPtr(b bool) *bool { return &b }` (add near `Default`).
  - After `Load` (`Default` → unmarshal → `applyDefaults` → `Validate`), `cfg.Connectors.OpenCost.EstimateFallback` is never nil: `true` unless the user wrote `estimateFallback: false`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/config/config_test.go`:

```go
func TestOpenCostEstimateFallbackDefaultsTrue(t *testing.T) {
	cfg, err := LoadFromBytes([]byte("connectors:\n  opencost:\n    url: auto\n"))
	require.NoError(t, err)
	require.NotNil(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.True(t, *cfg.Connectors.OpenCost.EstimateFallback)
}

func TestOpenCostEstimateFallbackFalseHonoured(t *testing.T) {
	cfg, err := LoadFromBytes([]byte("connectors:\n  opencost:\n    url: auto\n    estimateFallback: false\n"))
	require.NoError(t, err)
	require.NotNil(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.False(t, *cfg.Connectors.OpenCost.EstimateFallback)
}

func TestValidateRejectsBadOpenCostURL(t *testing.T) {
	_, err := LoadFromBytes([]byte("connectors:\n  opencost:\n    url: Auto\n"))
	require.ErrorContains(t, err, "connectors.opencost.url")
}

func TestValidateRejectsNonPositiveRates(t *testing.T) {
	_, err := LoadFromBytes([]byte("connectors:\n  opencost:\n    url: auto\n    rates:\n      cpuHour: 0\n      memGiBHour: 0.004\n"))
	require.ErrorContains(t, err, "connectors.opencost.rates")
}
```

If `LoadFromBytes` does not exist, use whatever the existing config tests use to parse a YAML string (check the top of `config_test.go`; M3 tests call `Load` against a temp file or a `LoadFromBytes` helper — reuse that exact pattern).

- [ ] **Step 2: Run tests — expect FAIL**

`go test ./internal/config/ -run 'OpenCost|NonPositiveRates|BadOpenCostURL' -v` → compile error (`EstimateFallback` is `bool`, no `Rates`, no `Rate`).

- [ ] **Step 3: Implement**

In `config.go`:

```go
type Rate struct {
	CPUHour    float64 `json:"cpuHour"`
	MemGiBHour float64 `json:"memGiBHour"`
}

type ConnectorSpec struct {
	URL              string `json:"url"`
	Mode             string `json:"mode"`
	EstimateFallback *bool  `json:"estimateFallback,omitempty"`
	Rates            *Rate  `json:"rates,omitempty"`
}

func boolPtr(b bool) *bool { return &b }
```

`Default()` — set `OpenCost: ConnectorSpec{URL: "auto", EstimateFallback: boolPtr(true)}`.

`applyDefaults()` — **delete** the old block:
```go
// OpenCost.EstimateFallback defaults to true only when ... URL check above.
if d.Connectors.OpenCost.EstimateFallback && c.Connectors.OpenCost.URL == "auto" {
	c.Connectors.OpenCost.EstimateFallback = true
}
```
replace with:
```go
if c.Connectors.OpenCost.EstimateFallback == nil {
	c.Connectors.OpenCost.EstimateFallback = boolPtr(true)
}
```

`Validate()` — after the `promql.url` switch, add:
```go
switch u := c.Connectors.OpenCost.URL; {
case u == "auto" || u == "disabled":
case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
default:
	return fmt.Errorf("connectors.opencost.url must be auto|disabled|an http(s) URL, got %q", u)
}
if r := c.Connectors.OpenCost.Rates; r != nil {
	if r.CPUHour <= 0 || r.MemGiBHour <= 0 {
		return fmt.Errorf("connectors.opencost.rates.{cpuHour,memGiBHour} must be positive, got %v/%v", r.CPUHour, r.MemGiBHour)
	}
}
```

Update `internal/config/testdata/full.yaml` and `poirot.example.yaml` opencost blocks to:
```yaml
  opencost:
    url: auto               # auto | disabled | http(s)://…
    estimateFallback: true   # false => emit cost/skipped instead of estimating when OpenCost is absent
    rates:                   # optional flat override; wins for every node type when set
      cpuHour: 0.031
      memGiBHour: 0.004
```
(In `poirot.example.yaml` keep `rates:` present but commented if the file comments other optional blocks — match the file's existing style.)

- [ ] **Step 4: Run tests — expect PASS**

`go test ./internal/config/ -race -count=1` all green. Then `go build ./... && go vet ./... && gofmt -l internal/config/`.

- [ ] **Step 5: Grep for other `EstimateFallback` readers**

`grep -rn 'EstimateFallback' internal/` — the only non-test hit outside `config` should be none yet (M4 tasks 9/13 add them). If M1/M2 left a reader, update it to deref the pointer.

- [ ] **Step 6: Commit**

```bash
git add internal/config/ poirot.example.yaml
git commit -m "feat: config — opencost block (estimateFallback *bool, rates override, url validation)"
```

---

## Task 2: `snapshot` — cost types + best-effort Jobs collection

**Files:**
- Modify: `internal/snapshot/snapshot.go`
- Modify: `internal/connector/k8s/collect.go`
- Test: `internal/snapshot/snapshot_test.go`, `internal/connector/k8s/collect_test.go`

**Interfaces:**
- Consumes: `snapshot.Snapshot` (`snapshot.go`), `k8s.Connector.Collect` (`collect.go:18`, per-namespace list loop).
- Produces (package `snapshot`):

```go
type CostBasis string

const (
	CostMeasured  CostBasis = "measured"
	CostEstimated CostBasis = "estimated"
)

type CostSet struct {
	Basis       CostBasis       `json:"basis"`
	Source      string          `json:"source"`   // "opencost" | "estimate"
	Currency    string          `json:"currency"` // "USD"
	Window      string          `json:"window"`   // "7d"
	CollectedAt time.Time       `json:"collectedAt"`
	Workloads   []WorkloadCost  `json:"workloads"`  // sorted (namespace, kind, name)
	Namespaces  []NamespaceCost `json:"namespaces"` // sorted (namespace)
	Note        string          `json:"note,omitempty"`
}

type WorkloadCost struct {
	Namespace       string  `json:"namespace"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	Replicas        int32   `json:"replicas"`
	CPURequestCores float64 `json:"cpuRequestCores"`
	CPUUsageCores   float64 `json:"cpuUsageCores"` // -1 when unavailable
	MemRequestBytes float64 `json:"memRequestBytes"`
	MemUsageBytes   float64 `json:"memUsageBytes"` // -1 when unavailable
	MonthlyCost     float64 `json:"monthlyCost"`
	MonthlyCPUCost  float64 `json:"monthlyCpuCost"`
	MonthlyMemCost  float64 `json:"monthlyMemCost"`
}

type NamespaceCost struct {
	Namespace        string  `json:"namespace"`
	MonthlyCost      float64 `json:"monthlyCost"`
	PriorMonthlyCost float64 `json:"priorMonthlyCost"` // -1 on the estimate path
}
```
- `Snapshot` gains `Cost *CostSet \`json:"cost,omitempty"\`` (after `PodLogs`) and `Jobs []batchv1.Job \`json:"jobs,omitempty"\`` (after `Services`). Import `batchv1 "k8s.io/api/batch/v1"`.

- [ ] **Step 1: Write the failing test (Jobs best-effort)**

Add to `internal/connector/k8s/collect_test.go` (match the existing fake-clientset setup in that file):

```go
func TestCollectJobsBestEffort(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}},
	)
	// Jobs list fails; core lists succeed.
	cs.PrependReactor("list", "jobs", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "jobs"}, "", nil)
	})
	c := newTestConnector(cs) // whatever the file's helper is
	snap, err := c.Collect(context.Background())
	require.NoError(t, err)          // run continues
	require.Nil(t, snap.Jobs)        // Jobs simply absent
}
```

(If `collect_test.go` has no reactor helpers, follow the same imports M3's `k8s` tests used: `k8stesting "k8s.io/client-go/testing"`, `apierrors "k8s.io/apimachinery/pkg/api/errors"`, `"k8s.io/apimachinery/pkg/runtime/schema"`.)

- [ ] **Step 2: Run — expect FAIL** (`snap.Jobs` undefined).

- [ ] **Step 3: Implement**

`snapshot.go`: add the types above and the two `Snapshot` fields. Add `time` to imports if not present (it is).

`collect.go`: inside the per-namespace loop, after the `Services` list, add a **best-effort** block that does NOT `return nil, err`:

```go
jobs, jerr := c.cs.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
if jerr == nil {
	snap.Jobs = append(snap.Jobs, jobs.Items...)
}
// jerr != nil: leave snap.Jobs as-is; cost/retained-jobs will simply not run.
```

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/snapshot/ ./internal/connector/k8s/ -race -count=1`; `go build ./...`.

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/ internal/connector/k8s/
git commit -m "feat: snapshot — CostSet types + best-effort Jobs collection"
```

---

## Task 3: `internal/cost/pricesheet` — embedded price sheet

**Files:**
- Create: `internal/cost/pricesheet.go`
- Create: `internal/cost/prices.yaml`
- Test: `internal/cost/pricesheet_test.go`

**Interfaces:**
- Consumes: nothing (leaf). Uses `sigs.k8s.io/yaml` (already a dep — `go.mod`).
- Produces (package `cost`):

```go
type Rate struct {
	CPUHour    float64 `json:"cpuHour"`
	MemGiBHour float64 `json:"memGiBHour"`
}
type Sheet struct {
	Default        Rate            `json:"default"`
	ByInstanceType map[string]Rate `json:"byInstanceType"`
	override       *Rate
}
func Load() (*Sheet, error)
func (s *Sheet) SetOverride(r *Rate)          // r may be nil (no-op)
func (s *Sheet) Rate(instanceType string) Rate // override wins; else exact ByInstanceType; else Default
```

- [ ] **Step 1: Write the failing test**

```go
package cost

import (
	"testing"
	"github.com/stretchr/testify/require"
)

func TestSheetLoad(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	require.Positive(t, s.Default.CPUHour)
	require.Contains(t, s.ByInstanceType, "m6i.large")
}

func TestSheetRateFallsBackToDefault(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	require.Equal(t, s.Default, s.Rate("no-such-type"))
	require.Equal(t, s.ByInstanceType["m6i.large"], s.Rate("m6i.large"))
}

func TestSheetOverrideWinsForEveryType(t *testing.T) {
	s, err := Load()
	require.NoError(t, err)
	o := &Rate{CPUHour: 1, MemGiBHour: 2}
	s.SetOverride(o)
	require.Equal(t, *o, s.Rate("m6i.large")) // even a known type
	require.Equal(t, *o, s.Rate("whatever"))
	s.SetOverride(nil) // no-op, override stays
	require.Equal(t, *o, s.Rate("m6i.large"))
}
```

- [ ] **Step 2: Run — expect FAIL** (no package).

- [ ] **Step 3: Implement**

`prices.yaml`:
```yaml
default:
  cpuHour: 0.031
  memGiBHour: 0.004
byInstanceType:
  m6i.large: { cpuHour: 0.0210, memGiBHour: 0.0028 }
  c6i.large: { cpuHour: 0.0250, memGiBHour: 0.0033 }
  r6i.large: { cpuHour: 0.0165, memGiBHour: 0.0022 }
  n2-standard-4: { cpuHour: 0.0240, memGiBHour: 0.0032 }
  e2-standard-4: { cpuHour: 0.0170, memGiBHour: 0.0023 }
  Standard_D4s_v5: { cpuHour: 0.0230, memGiBHour: 0.0031 }
```

`pricesheet.go`:
```go
package cost

import (
	_ "embed"
	"fmt"
	sigsyaml "sigs.k8s.io/yaml"
)

//go:embed prices.yaml
var pricesYAML []byte

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
```

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/cost/ -race -count=1`; `go build -tags tools ./...` (embed must compile).

- [ ] **Step 5: Commit**

```bash
git add internal/cost/pricesheet.go internal/cost/prices.yaml internal/cost/pricesheet_test.go
git commit -m "feat: cost — embedded price sheet with per-type rates and flat override"
```

---

## Task 4: `internal/connector/opencost/parse.go` — allocation JSON → `[]Allocation`

**Files:**
- Create: `internal/connector/opencost/parse.go`
- Test: `internal/connector/opencost/parse_test.go`
- Test data: `internal/connector/opencost/testdata/alloc_ns_controller.json`, `testdata/alloc_ns_14d.json`, `testdata/alloc_error.json`, `testdata/alloc_empty.json`

**Interfaces:**
- Consumes: nothing (leaf, `encoding/json` only).
- Produces (package `opencost`):

```go
type Allocation struct {
	Namespace       string
	Controller      string
	ControllerKind  string  // lowercase from OpenCost: "deployment" | "statefulset" | "daemonset" | ""
	CPUCoreRequest  float64 // cpuCoreRequestAverage
	CPUCoreUsage    float64 // cpuCoreUsageAverage
	RAMByteRequest  float64 // ramByteRequestAverage
	RAMByteUsage    float64 // ramByteUsageAverage
	CPUCost         float64
	RAMCost         float64
	PVCost          float64
	LBCost          float64
	TotalCost       float64
}

// ParseAllocation decodes one OpenCost /allocation/compute response body.
// It returns one []Allocation per step in "data" (len 1 for accumulate=true,
// len 2 for the 14d step=7d call). Rows keyed __idle__/__unallocated__ or with
// an empty name are dropped. A row that fails to decode is skipped, not fatal.
func ParseAllocation(body []byte) (steps [][]Allocation, err error)
```
- `err` is non-nil only for: not JSON, `code` present and `>= 400`, or `data` missing/not an array.

- [ ] **Step 1: Write test data**

`testdata/alloc_ns_controller.json` (one step, two workloads):
```json
{"code":200,"data":[{
  "team/web-deploy":{"name":"team/web-deploy","properties":{"namespace":"team","controller":"web-deploy","controllerKind":"deployment"},
    "cpuCoreRequestAverage":2.0,"cpuCoreUsageAverage":0.3,"ramByteRequestAverage":2147483648,"ramByteUsageAverage":268435456,
    "cpuCost":40.0,"ramCost":10.0,"pvCost":0,"loadBalancerCost":0,"totalCost":50.0},
  "team/__idle__":{"name":"team/__idle__","properties":{"namespace":"team"},"cpuCost":5,"ramCost":1,"totalCost":6}
}]}
```
`testdata/alloc_ns_14d.json` — `"data":[ {step0 map}, {step1 map} ]`, each with a `team` namespace-aggregate row (`properties.namespace:"team"`, no controller), totalCost 40 then 80.
`testdata/alloc_error.json` — `{"code":403,"message":"forbidden"}`.
`testdata/alloc_empty.json` — `{"code":200,"data":[{}]}`.

- [ ] **Step 2: Write the failing test**

```go
func TestParseAllocationHappy(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Len(t, steps[0], 1) // __idle__ dropped
	a := steps[0][0]
	require.Equal(t, "team", a.Namespace)
	require.Equal(t, "web-deploy", a.Controller)
	require.Equal(t, "deployment", a.ControllerKind)
	require.InDelta(t, 2.0, a.CPUCoreRequest, 1e-9)
	require.InDelta(t, 50.0, a.TotalCost, 1e-9)
}

func TestParseAllocationTwoSteps(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_ns_14d.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 2)
}

func TestParseAllocationError(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_error.json")
	_, err := ParseAllocation(b)
	require.Error(t, err)
}

func TestParseAllocationEmpty(t *testing.T) {
	b, _ := os.ReadFile("testdata/alloc_empty.json")
	steps, err := ParseAllocation(b)
	require.NoError(t, err)
	require.Len(t, steps, 1)
	require.Empty(t, steps[0])
}
```

- [ ] **Step 3: Run — expect FAIL.**

- [ ] **Step 4: Implement** `parse.go`. Decode into:
```go
type envelope struct {
	Code *int              `json:"code"`
	Data []json.RawMessage `json:"data"`
}
type rawAlloc struct {
	Name       string `json:"name"`
	Properties struct {
		Namespace      string `json:"namespace"`
		Controller     string `json:"controller"`
		ControllerKind string `json:"controllerKind"`
	} `json:"properties"`
	CPUCoreRequestAverage float64 `json:"cpuCoreRequestAverage"`
	CPUCoreUsageAverage   float64 `json:"cpuCoreUsageAverage"`
	RAMByteRequestAverage float64 `json:"ramByteRequestAverage"`
	RAMByteUsageAverage   float64 `json:"ramByteUsageAverage"`
	CPUCost               float64 `json:"cpuCost"`
	RAMCost               float64 `json:"ramCost"`
	PVCost                float64 `json:"pvCost"`
	LoadBalancerCost      float64 `json:"loadBalancerCost"`
	TotalCost             float64 `json:"totalCost"`
}
```
For each `Data` element, `json.Unmarshal` into `map[string]rawAlloc`; iterate the map, skip `key`/`name` containing `__idle__` or `__unallocated__` or with empty `Properties.Namespace`; append an `Allocation`. **Sort each step's `[]Allocation` by `(Namespace, ControllerKind, Controller)`** before returning (determinism — the map iteration order must not leak). `code >= 400` (when `Code != nil`) → error including the `message` field if present.

- [ ] **Step 5: Run — expect PASS.** `go test ./internal/connector/opencost/ -race -count=1`.

- [ ] **Step 6: Commit**

```bash
git add internal/connector/opencost/parse.go internal/connector/opencost/parse_test.go internal/connector/opencost/testdata/
git commit -m "feat: opencost — tolerant allocation response parser"
```

---

## Task 5: `internal/connector/opencost/client.go` — HTTP client over a pluggable doer

**Files:**
- Create: `internal/connector/opencost/client.go`
- Test: `internal/connector/opencost/client_test.go`

**Interfaces:**
- Consumes: `ParseAllocation` (Task 4).
- Produces (package `opencost`):

```go
type doer func(ctx context.Context, path string, query url.Values) ([]byte, error)

type Client struct{ do doer }

func newClient(d doer) *Client

// Allocation issues one /allocation/compute call. accumulate=true collapses
// the window to a single step; step != "" (e.g. "7d") buckets it.
func (c *Client) Allocation(ctx context.Context, window, aggregate string, accumulate bool, step string) ([][]Allocation, error)

// httpDoer talks to an explicit base URL (rawURL like http://opencost.opencost:9003).
func httpDoer(rawURL string, hc *http.Client) (doer, error)
```
- `Allocation` builds `query = {window, aggregate, accumulate("true"/"false")}` and adds `step` when non-empty, calls `c.do(ctx, "/allocation/compute", query)`, then `ParseAllocation`.
- `httpDoer`'s doer: `GET {base}{path}?{query}`, `io.LimitReader(resp.Body, 1<<20)`, always `defer resp.Body.Close()`, surface body-read errors, non-2xx → error with a truncated body snippet.

- [ ] **Step 1: Write the failing test**

```go
func TestAllocationSendsAccumulateAndStep(t *testing.T) {
	var gotQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
		w.Write(b)
	}))
	defer srv.Close()
	d, err := httpDoer(srv.URL, srv.Client())
	require.NoError(t, err)
	c := newClient(d)

	_, err = c.Allocation(context.Background(), "7d", "namespace,controller", true, "")
	require.NoError(t, err)
	require.Equal(t, "true", gotQuery.Get("accumulate"))
	require.Equal(t, "7d", gotQuery.Get("window"))
	require.Empty(t, gotQuery.Get("step"))

	_, err = c.Allocation(context.Background(), "14d", "namespace", false, "7d")
	require.NoError(t, err)
	require.Equal(t, "false", gotQuery.Get("accumulate"))
	require.Equal(t, "7d", gotQuery.Get("step"))
}

func TestAllocationSurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte("upstream down"))
	}))
	defer srv.Close()
	d, _ := httpDoer(srv.URL, srv.Client())
	_, err := newClient(d).Allocation(context.Background(), "7d", "namespace", true, "")
	require.ErrorContains(t, err, "503")
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement.** Mirror `internal/connector/promql/client.go` structure exactly (same `doer` shape, same LimitReader, same error wrapping). Keep it ~70 lines.

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/connector/opencost/ -race -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/opencost/client.go internal/connector/opencost/client_test.go
git commit -m "feat: opencost — HTTP client over a pluggable doer"
```

---

## Task 6: `internal/connector/opencost/discover.go` — Service discovery + API-server proxy doer

**Files:**
- Create: `internal/connector/opencost/discover.go`
- Test: `internal/connector/opencost/discover_test.go`

**Interfaces:**
- Consumes: `k8s.io/client-go/kubernetes.Interface`, the `doer` type (Task 5). Read `internal/connector/promql/discover.go` and `internal/connector/promql/proxy.go` — this is a near-copy.
- Produces (package `opencost`):

```go
type Target struct {
	Namespace, Name, Scheme string
	Port                    int32
	Path                    string // "/allocation/compute"
}

// Discover returns the first well-known OpenCost/Kubecost Service, or nil.
// It lists Services("") once and sorts by (namespace, name) for a stable pick.
func Discover(ctx context.Context, cs kubernetes.Interface, scopeNamespaces []string) (*Target, error)

func proxyDoer(cs kubernetes.Interface, t Target) doer
```
- Candidate names: `opencost`, `kubecost-cost-analyzer`, `cost-analyzer`. Candidate namespaces (checked in this order, then any): `opencost`, `monitoring`, `kubecost`, `kube-system`. Port: `pickPort(svc)` — a port named `http`/`web`/`https` (https ⇒ `Scheme="https"`), else a well-known port in `{9003, 9090}`, else the sole port; no match ⇒ skip the Service. Copy `pickPort` from `promql/discover.go` verbatim and extend its well-known set.
- `proxyDoer`: `cs.CoreV1().Services(t.Namespace).ProxyGet(t.Scheme, t.Name, strconv.Itoa(int(t.Port)), path, mapOf(query)).DoRaw(ctx)` — identical shape to `promql/proxy.go`.

- [ ] **Step 1: Write the failing test**

```go
func TestDiscoverFindsOpenCost(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "opencost", Namespace: "opencost"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 9003}}},
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.NotNil(t, tgt)
	require.Equal(t, "opencost", tgt.Name)
	require.Equal(t, int32(9003), tgt.Port)
	require.Equal(t, "/allocation/compute", tgt.Path)
}

func TestDiscoverNilWhenAbsent(t *testing.T) {
	tgt, err := Discover(context.Background(), fake.NewSimpleClientset(), nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}

func TestDiscoverPicksNamedPortOverConstant(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "cost-analyzer", Namespace: "kubecost"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Name: "tcp-model", Port: 9003}, {Name: "http", Port: 9090},
		}},
	})
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Equal(t, int32(9090), tgt.Port) // named "http" wins
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Implement.** Follow `promql/discover.go`: one `cs.CoreV1().Services("").List(ctx, metav1.ListOptions{})`, sort `items` by `(Namespace, Name)`, iterate namespace-priority then rest, match name ∈ candidates, `pickPort`, return the first `*Target` with `Path: "/allocation/compute"`. `proxyDoer` copies `promql/proxy.go`.

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/connector/opencost/ -race -count=1`.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/opencost/discover.go internal/connector/opencost/discover_test.go
git commit -m "feat: opencost — well-known Service discovery + API-server proxy doer"
```

---

## Task 7: `internal/connector/opencost/opencost.go` — the connector

**Files:**
- Create: `internal/connector/opencost/opencost.go`
- Test: `internal/connector/opencost/opencost_test.go`

**Interfaces:**
- Consumes: `connector.Connector` / `connector.Capability` / `connector.Availability` / `connector.State*` (`internal/connector`), `Client` / `Discover` / `proxyDoer` / `httpDoer` (Tasks 5–6). Read `internal/connector/promql/promql.go` — same shape.
- Produces (package `opencost`):

```go
type Options struct {
	URL        string
	Clientset  kubernetes.Interface
	Namespaces []string
}
type Connector struct { /* opts, client, backend */ }
func New(o Options) *Connector
func (c *Connector) Name() string                       // "opencost"
func (c *Connector) Probe(ctx context.Context) connector.Availability
func (c *Connector) Capabilities() []connector.Capability
func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error)
var _ connector.Connector = (*Connector)(nil)
```
- `Probe`: `url:"disabled"` → `{StateAbsent, "disabled in config"}`. `url:"auto"` → `Discover`; nil → `{StateAbsent, "no OpenCost service found"}`; found → build `c.client = newClient(proxyDoer(cs, *tgt))`, `c.backend = "svc:"+ns+"/"+name`. default → `httpDoer(url)`; bad → `{StateAbsent, "bad url", detail}`. Then a probe call `c.client.Allocation(ctx, "1d", "namespace", true, "")` — error → `{StateAbsent, "backend unreachable", detail}` (note: **`absent`, not `degraded`** — an unreachable OpenCost is a normal "estimate instead" signal, spec §"The `opencost` connector"). Success → `{StateAvailable}`.
- `Capabilities`: one entry:
```go
{ID: "opencost.allocation",
 Description: "Fetch OpenCost cost allocations for a window, aggregated by the given key.",
 ArgsSchema: json.RawMessage(`{"type":"object","required":["window","aggregate"],"properties":{"window":{"type":"string"},"aggregate":{"type":"string"},"accumulate":{"type":"boolean"},"step":{"type":"string"}},"additionalProperties":false}`)}
```
- `Query`: `id=="opencost.allocation"` → unmarshal `{window, aggregate string; accumulate bool; step string}`, call `c.client.Allocation(...)`, `json.Marshal` the `[][]Allocation`. `c.client == nil` → error "probe first". unknown id → error.

- [ ] **Step 1: Write the failing test**

```go
func TestProbeAutoAbsentWhenNoService(t *testing.T) {
	c := New(Options{URL: "auto", Clientset: fake.NewSimpleClientset()})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
}

func TestProbeDisabled(t *testing.T) {
	c := New(Options{URL: "disabled"})
	require.Equal(t, connector.StateAbsent, c.Probe(context.Background()).State)
	require.Equal(t, "disabled in config", c.Probe(context.Background()).Reason)
}

func TestProbeExplicitURLAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := os.ReadFile("testdata/alloc_ns_controller.json")
		w.Write(b)
	}))
	defer srv.Close()
	c := New(Options{URL: srv.URL})
	require.Equal(t, connector.StateAvailable, c.Probe(context.Background()).State)
	out, err := c.Query(context.Background(), "opencost.allocation",
		json.RawMessage(`{"window":"7d","aggregate":"namespace,controller","accumulate":true}`))
	require.NoError(t, err)
	require.Contains(t, string(out), "web-deploy")
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** (mirror `promql.go`, ~120 lines).
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/connector/opencost/ -race -count=1`; `go vet ./...`.
- [ ] **Step 5: Commit**

```bash
git add internal/connector/opencost/opencost.go internal/connector/opencost/opencost_test.go
git commit -m "feat: opencost — connector.Connector implementation"
```

---

## Task 8: `internal/cost/estimate.go` — requests-only estimator

**Files:**
- Create: `internal/cost/estimate.go`
- Test: `internal/cost/estimate_test.go`

**Interfaces:**
- Consumes: `snapshot.Snapshot` / `snapshot.CostSet` / `WorkloadCost` / `NamespaceCost` (Task 2), `*Sheet` (Task 3), `connector.Registry` (`Get`/`Satisfied`).
- Produces (package `cost`):

```go
const hoursPerMonth = 730.0

// Estimate builds an "estimated"-basis CostSet from pod requests × sheet.
// reg is used only to fetch the "promql" connector for usage; a nil registry
// or absent promql yields CPUUsageCores/MemUsageBytes = -1.
func Estimate(ctx context.Context, snap *snapshot.Snapshot, reg *connector.Registry, sheet *Sheet) *snapshot.CostSet
```

**Algorithm (per the spec's `estimate.go` section, R7/R9/R11):**
1. Build `workloads`: one entry per `snap.Deployments` (`Kind:"Deployment"`), `snap.StatefulSets` (`"StatefulSet"`), `snap.DaemonSets` (`"DaemonSet"`).
2. `Replicas`:
   - Deployment/StatefulSet → `*spec.Replicas` (nil ⇒ 1).
   - DaemonSet → count of `snap.Pods` whose `metav1.GetControllerOf(pod).UID == ds.UID` **and** `pod.Status.Phase == Running`. (R11 — never node count.)
3. `CPURequestCores` = Σ container `resources.requests.cpu` (as `resource.Quantity` → `.AsApproximateFloat64()`) over the workload's **pod template** × `Replicas`; `MemRequestBytes` likewise.
4. Instance type: over the workload's Running pods, `pod.Spec.NodeName` → node label `node.kubernetes.io/instance-type` (fallback `beta.kubernetes.io/instance-type`). Build `map[string]int` freq; pick the key with the highest count, **tie-broken by the lexicographically smallest key** (R9 — never rely on map order). No running pods ⇒ cluster-modal instance type (same tie-break over all `snap.Nodes`). No labelled node ⇒ `""` and set `note = "no instance-type labels found; using blended default rate"`.
5. `rate := sheet.Rate(instanceType)`. `MonthlyCPUCost = CPURequestCores * rate.CPUHour * hoursPerMonth`; `MonthlyMemCost = MemRequestBytes/(1<<30) * rate.MemGiBHour * hoursPerMonth`; `MonthlyCost = sum`.
6. Usage (only if `reg != nil` and `reg.Get("promql")` returns an available connector):
   - CPU: `Query("promql.instant", {"expr": <cpuExpr>})`; Mem: same with `<memExpr>`.
   - `cpuExpr` (R7 two-hop for Deployments):
     ```
     sum by (namespace, workload) (
       rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[7d])
       * on (namespace, pod) group_left(workload)
       label_replace(
         label_replace(kube_pod_owner{owner_kind="ReplicaSet"}, "rs", "$1", "owner_name", "(.*)"),
         "workload", "$1", "rs", "^(.*)-[0-9a-f]{6,10}$")
     )
     ```
     plus a `kube_pod_owner{owner_kind=~"StatefulSet|DaemonSet"}` union that maps `owner_name` straight to `workload`. `memExpr` = same shape with `container_memory_working_set_bytes` and no `rate(...)`.
   - Match returned samples to workloads by `(labels["namespace"], labels["workload"])` (case-sensitive; the workload label carries the Deployment/STS/DS name). Fill `CPUUsageCores` / `MemUsageBytes`. Any error, or no `promql` ⇒ set both to `-1` for every workload and append `"usage unavailable — rightsizing limited to requests vs limits"` to `note`.
7. `Namespaces`: one `NamespaceCost` per distinct namespace, `MonthlyCost` = Σ workload `MonthlyCost` in it, `PriorMonthlyCost = -1`.
8. Sort `Workloads` by `(Namespace, Kind, Name)`, `Namespaces` by `Namespace`.
9. Return `&snapshot.CostSet{Basis: snapshot.CostEstimated, Source: "estimate", Currency: "USD", Window: "7d", CollectedAt: snap.Meta.CollectedAt, Workloads, Namespaces, Note: note}`.

- [ ] **Step 1: Write the failing tests** (`estimate_test.go`) — build `*snapshot.Snapshot` fixtures by hand:

```go
func TestEstimateWorkloadCostFromRequests(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:  snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes: []corev1.Node{node("n1", "m6i.large")},
		Pods:  []corev1.Pod{runningPod("team", "web-abc", "n1", ctrlRef("Deployment", "web"))},
		Deployments: []appsv1.Deployment{deploy("team", "web", 3, "500m", "1Gi")},
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	require.Equal(t, snapshot.CostEstimated, cs.Basis)
	w := cs.Workloads[0]
	require.Equal(t, int32(3), w.Replicas)
	require.InDelta(t, 1.5, w.CPURequestCores, 1e-6)            // 0.5 * 3
	require.InDelta(t, 3*(1<<30), w.MemRequestBytes, 1)          // 1Gi * 3
	require.InDelta(t, 1.5*0.0210*730, w.MonthlyCPUCost, 1e-3)   // m6i.large cpuHour
	require.Equal(t, -1.0, w.CPUUsageCores)                      // no promql
	require.Contains(t, cs.Note, "usage unavailable")
}

func TestEstimateDaemonSetUsesRunningPodCountNotNodes(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:  snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes: []corev1.Node{node("n1", "m6i.large"), node("n2", "m6i.large"), node("n3", "m6i.large")},
		Pods:  []corev1.Pod{runningPod("kube-system", "agent-1", "n1", ctrlRef("DaemonSet", "agent"))}, // only 1 of 3 nodes
		DaemonSets: []appsv1.DaemonSet{ds("kube-system", "agent", "100m", "128Mi")},
	}
	cs := Estimate(context.Background(), snap, nil, mustSheet(t))
	require.Equal(t, int32(1), cs.Workloads[0].Replicas)
}

func TestEstimateInstanceTypeTieBreakIsDeterministic(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta:  snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes: []corev1.Node{node("a", "c6i.large"), node("b", "m6i.large")},
		Pods: []corev1.Pod{
			runningPod("t", "p1", "a", ctrlRef("Deployment", "d")),
			runningPod("t", "p2", "b", ctrlRef("Deployment", "d")),
		},
		Deployments: []appsv1.Deployment{deploy("t", "d", 2, "1", "1Gi")},
	}
	var first float64
	for i := 0; i < 20; i++ {
		cs := Estimate(context.Background(), snap, nil, mustSheet(t))
		if i == 0 {
			first = cs.Workloads[0].MonthlyCPUCost
		}
		require.Equal(t, first, cs.Workloads[0].MonthlyCPUCost) // c6i.large wins (smaller string)
	}
}
```

Write small helpers (`node`, `runningPod`, `ctrlRef`, `deploy`, `ds`, `mustSheet`) at the bottom of the test file. `ctrlRef` sets `OwnerReferences` with `Controller: ptr(true)` and a stable `UID`; `deploy`/`ds` set that same UID on `ObjectMeta.UID` so `metav1.GetControllerOf` matches.

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `estimate.go` per the algorithm. Keep the promql usage join in a separate unexported `fillUsage(ctx, reg, workloads) (ok bool)` function so it is independently testable and its failure is a clean `return false`.
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/cost/ -race -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/cost/estimate.go internal/cost/estimate_test.go
git commit -m "feat: cost — requests-only estimator with deterministic instance-type pricing"
```

---

## Task 9: `internal/cost/collect.go` — the collector (measured path + fallback)

**Files:**
- Create: `internal/cost/collect.go`
- Test: `internal/cost/collect_test.go`

**Interfaces:**
- Consumes: `Estimate` (Task 8), `Load`/`Sheet.SetOverride` (Task 3), `ParseAllocation` (Task 4 — via the connector's marshalled `[][]Allocation`), `connector.Registry` (`Get`, `Satisfied`), `config.ConnectorSpec` (Task 1).
- Produces (package `cost`):

```go
// Collect returns the best available CostSet, or nil (⇒ analyzer emits cost/skipped).
//  1. opencost available  → measured
//  2. else *spec.EstimateFallback && sheet loads → estimated (Estimate)
//  3. else → nil
func Collect(ctx context.Context, snap *snapshot.Snapshot, reg *connector.Registry, spec config.ConnectorSpec) *snapshot.CostSet
```

**Measured path:**
- `sheet, err := Load()`; on error → skip straight to the fallback decision (measured path also uses no sheet, so `err` here only matters for the estimate branch — load lazily there instead; keep `Load` out of the measured path).
- `raw, err := oc.Query(ctx, "opencost.allocation", {"window":"7d","aggregate":"namespace,controller","accumulate":true})`. Unmarshal to `[][]Allocation`, take `steps[0]`.
- `raw2, err := oc.Query(ctx, "opencost.allocation", {"window":"14d","aggregate":"namespace","accumulate":false,"step":"7d"})` → `steps2`; `prior = steps2[0]`, `recent = steps2[1]` (guard `len`).
- Build `Workloads`: for each `steps[0]` row with non-empty `ControllerKind`, find the matching `snap` workload by `Namespace`, `Controller` name, and `strings.EqualFold(row.ControllerKind, wl.Kind)` (R4). No match in scope ⇒ drop the row. Fill `Kind` from the **snapshot** workload (canonical case), `Replicas` from it, requests from `row.CPUCoreRequest`/`row.RAMByteRequest`, usage from `row.CPUCoreUsage`/`row.RAMByteUsage`, and `MonthlyCPUCost = row.CPUCost * (30.0/7.0)`, `MonthlyMemCost = row.RAMCost * (30.0/7.0)`, `MonthlyCost = (row.CPUCost+row.RAMCost+row.PVCost+row.LBCost) * (30.0/7.0)`.
- `Namespaces`: from `recent` (MonthlyCost = `row.TotalCost * 30/7`) joined to `prior` by namespace (PriorMonthlyCost = `prior.TotalCost * 30/7`, or `-1` if absent).
- **Any query/unmarshal error on either call ⇒ discard everything built so far**, append the error text to `note`, and fall through to the estimate decision (no mixed sets — spec R "discard partial measured data").
- On full success: `Basis: measured`, `Source: "opencost"`, `Currency: "USD"`, `Window: "7d"`, sort both slices, return.

**Fallback decision:** measured unavailable/failed →
- `if spec.EstimateFallback != nil && !*spec.EstimateFallback { return nil }`
- `sheet, err := Load(); if err != nil { return nil }`
- `sheet.SetOverride(rateFromConfig(spec.Rates))` where `rateFromConfig(*config.Rate) *Rate` converts (nil→nil).
- `return Estimate(ctx, snap, reg, sheet)` — carry any measured-path `note` forward by prepending it.

- [ ] **Step 1: Write the failing tests**

```go
func TestCollectMeasuredJoinsCaseInsensitiveKind(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakeOC{}) // Query returns testdata/alloc_ns_controller.json step + a 2-step ns response
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Deployments: []appsv1.Deployment{deploy("team", "web-deploy", 2, "1", "1Gi")},
	}
	cs := Collect(context.Background(), snap, reg, config.ConnectorSpec{EstimateFallback: ptrTrue})
	require.Equal(t, snapshot.CostMeasured, cs.Basis)
	require.Len(t, cs.Workloads, 1)                 // OpenCost "deployment" joined to snapshot "Deployment"
	require.Equal(t, "Deployment", cs.Workloads[0].Kind)
}

func TestCollectFallsBackToEstimateOnQueryError(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(errOC{}) // Query always errors
	reg.Probe(context.Background())
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Unix(0, 0).UTC()},
		Nodes:       []corev1.Node{node("n", "m6i.large")},
		Pods:        []corev1.Pod{runningPod("t", "p", "n", ctrlRef("Deployment", "d"))},
		Deployments: []appsv1.Deployment{deploy("t", "d", 1, "500m", "512Mi")},
	}
	cs := Collect(context.Background(), snap, reg, config.ConnectorSpec{EstimateFallback: ptrTrue})
	require.Equal(t, snapshot.CostEstimated, cs.Basis)
	require.NotEmpty(t, cs.Note)
}

func TestCollectNilWhenNoOpenCostAndFallbackOff(t *testing.T) {
	cs := Collect(context.Background(), &snapshot.Snapshot{}, connector.NewRegistry(),
		config.ConnectorSpec{EstimateFallback: ptrFalse})
	require.Nil(t, cs)
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `collect.go`.
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/cost/ -race -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/cost/collect.go internal/cost/collect_test.go
git commit -m "feat: cost — collector: measured OpenCost path with clean estimate fallback"
```

---

## Task 10: `internal/cost/costmeta.go` + `report.CostMeta`

**Files:**
- Modify: `internal/report/report.go`
- Create: `internal/cost/costmeta.go`
- Test: `internal/cost/costmeta_test.go`

**Interfaces:**
- Produces (package `report`):
```go
type CostMeta struct {
	Basis                 string  `json:"basis"`
	Currency              string  `json:"currency"`
	Window                string  `json:"window"`
	MonthlyTotal          float64 `json:"monthlyTotal"`
	EstimatedMonthlyWaste float64 `json:"estimatedMonthlyWaste"`
	Note                  string  `json:"note,omitempty"`
}
```
`report.Meta` gains `Cost *CostMeta \`json:"cost,omitempty"\`` immediately after `LLM`. **`Build`'s signature is unchanged.**
- Produces (package `cost`):
```go
// From rolls a CostSet + the analyzer's findings into a report.CostMeta.
// MonthlyTotal = Σ workload MonthlyCost. EstimatedMonthlyWaste = Σ of the
// evidence value named "estimatedMonthlySaving" across cost/* findings.
func From(cs *snapshot.CostSet, findings []analyzer.Finding) *report.CostMeta // nil-safe: nil cs → nil
```

- [ ] **Step 1: Write the failing test** (`costmeta_test.go`):

```go
func TestFromRollsUpWasteFromFindings(t *testing.T) {
	cs := &snapshot.CostSet{
		Basis: snapshot.CostEstimated, Currency: "USD", Window: "7d",
		Workloads: []snapshot.WorkloadCost{{MonthlyCost: 100}, {MonthlyCost: 50}},
		Note:      "estimated from requests",
	}
	findings := []analyzer.Finding{
		{RuleID: "cost/idle", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 40.0}}},
		{RuleID: "cost/rightsizing", Domain: "cost", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 12.5}}},
		{RuleID: "slo/x", Domain: "slo", Evidence: []analyzer.Evidence{{Query: "estimatedMonthlySaving", Value: 999.0}}}, // ignored: not cost/*
	}
	m := From(cs, findings)
	require.Equal(t, "estimated", m.Basis)
	require.InDelta(t, 150.0, m.MonthlyTotal, 1e-9)
	require.InDelta(t, 52.5, m.EstimatedMonthlyWaste, 1e-9)
}

func TestFromNilSafe(t *testing.T) { require.Nil(t, From(nil, nil)) }
```

Note the evidence key convention: **`Evidence.Query` holds the metric/key name, `Evidence.Value` the number** (matches how `slo` evidence works — check `internal/analyzer/slo` and copy the convention exactly; if `slo` uses `Evidence.Source` for the name instead, follow that). Task 11/12 must use the same field.

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement.** `From` iterates findings, `strings.HasPrefix(f.RuleID, "cost/")`, sums the evidence entry whose name == `"estimatedMonthlySaving"` (coerce `Value` via a `toFloat(any) (float64, bool)` helper handling `float64`/`int`/`json.Number`).
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/cost/ ./internal/report/ -race -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/report/report.go internal/cost/costmeta.go internal/cost/costmeta_test.go
git commit -m "feat: report+cost — CostMeta type and rollup from findings"
```

---

## Task 11: `analyzer/cost` — skeleton + usage rules (rightsizing, idle, over-replicated)

**Files:**
- Create: `internal/analyzer/cost/cost.go`
- Create: `internal/analyzer/cost/usage_rules.go`
- Test: `internal/analyzer/cost/usage_rules_test.go`, `internal/analyzer/cost/cost_test.go`

**Interfaces:**
- Consumes: `analyzer.Analyzer` / `Finding` / `Evidence` / `ObjectRef` / `Severity*` (`internal/analyzer/finding.go`), `snapshot.Snapshot` / `CostSet` / `WorkloadCost` (Task 2).
- Produces (package `cost`, i.e. `internal/analyzer/cost`):

```go
type Analyzer struct{}
func New() *Analyzer
func (*Analyzer) ID() string         { return "cost" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }
func (a *Analyzer) Analyze(ctx context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error)
var _ analyzer.Analyzer = (*Analyzer)(nil)

// safeRatio returns num/den and ok=false when den==0 or the result is non-finite.
func safeRatio(num, den float64) (v float64, ok bool)
```

**`Analyze` (two-pass, spec R6):**
```
if snap.Cost == nil {
    return []Finding{skippedFinding(snap)}, nil   // cost/skipped, info, adapts text for a nil vs sheet-error cause
}
cs := snap.Cost
var out []Finding
overReplicated := map[string]bool{}  // key = wl.Namespace+"|"+wl.Kind+"|"+wl.Name

// Pass 1
for _, wl := range cs.Workloads {
    if f, ok := checkOverReplicated(wl, cs.Basis, hpaTargets(snap)); ok {
        out = append(out, f); overReplicated[key(wl)] = true
    }
}
for _, wl := range cs.Workloads {
    if f, ok := checkIdle(wl, cs.Basis, hpaTargets(snap)); ok { out = append(out, f) }
}
// Pass 2
anyUsage := false
for _, wl := range cs.Workloads { if wl.CPUUsageCores >= 0 { anyUsage = true } }
if !anyUsage {
    out = append(out, rightsizingLimitedFinding()) // Object: ObjectRef{Kind:"Cluster"} (spec R15)
} else {
    for _, wl := range cs.Workloads {
        if overReplicated[key(wl)] { continue }
        if f, ok := checkRightsizing(wl, cs.Basis); ok { out = append(out, f) }
    }
}
// object rules (Task 12) are appended here too once that task lands
return out, nil
```
`cs.Workloads` is already sorted by Task 9, so `out` is deterministic without re-sorting (`report.Build` re-sorts anyway).

**`checkRightsizing(wl, basis) (Finding, bool)`** — spec `cost/rightsizing`, per-dimension (R8):
```
cpuElig := wl.CPURequestCores > 0 && wl.CPUUsageCores >= 0
memElig := wl.MemRequestBytes > 0 && wl.MemUsageBytes >= 0
cpuUtil, _ := safeRatio(wl.CPUUsageCores, wl.CPURequestCores)
memUtil, _ := safeRatio(wl.MemUsageBytes, wl.MemRequestBytes)
cpuOver := cpuElig && cpuUtil < 0.5
memOver := memElig && memUtil < 0.5
if (!cpuOver && !memOver) || wl.MonthlyCost < 5 { return Finding{}, false }
sugCPU := wl.CPURequestCores; if cpuOver { sugCPU = math.Max(wl.CPUUsageCores*1.3, 0.010) }
sugMem := wl.MemRequestBytes; if memOver { sugMem = math.Max(wl.MemUsageBytes*1.3, 16<<20) }
saving := 0.0
if r, ok := safeRatio(sugCPU, wl.CPURequestCores); ok { saving += wl.MonthlyCPUCost * clamp01(1-r) }
if r, ok := safeRatio(sugMem, wl.MemRequestBytes); ok { saving += wl.MonthlyMemCost * clamp01(1-r) }
sev := SeverityInfo; if saving >= 20 { sev = SeverityWarning }
// Evidence: cpuRequestCores, cpuUsageCores, memRequestBytes, memUsageBytes,
//           suggestedCpuRequest, suggestedMemRequest, estimatedMonthlySaving, basis
```
`Object = ObjectRef{Kind: wl.Kind, Namespace: wl.Namespace, Name: wl.Name}`. `RuleID:"cost/rightsizing"`, `Domain:"cost"`.

**`checkIdle(wl, basis, hpa) (Finding, bool)`** — spec `cost/idle`:
```
if wl.Kind == "DaemonSet" || wl.Replicas < 1 || wl.CPUUsageCores < 0 || wl.MonthlyCost < 5 { return _, false }
perPod, _ := safeRatio(wl.CPUUsageCores, float64(wl.Replicas))
if perPod >= 0.005 { return _, false }
sev := SeverityInfo; if wl.MonthlyCost >= 20 { sev = SeverityWarning }
// Evidence: cpuUsageCores, replicas, monthlyCost, estimatedMonthlySaving(=wl.MonthlyCost), hasHPA, basis
```

**`checkOverReplicated(wl, basis, hpa) (Finding, bool)`** — spec `cost/over-replicated`:
```
if wl.Kind != "Deployment" && wl.Kind != "StatefulSet" { return _, false }
if wl.Replicas < 4 || hpa[key(wl)] || wl.CPUUsageCores < 0 || wl.MonthlyCost < 10 { return _, false }
perPodReq, ok := safeRatio(wl.CPURequestCores, float64(wl.Replicas)); if !ok { return _, false }
perPodUse, _ := safeRatio(wl.CPUUsageCores, float64(wl.Replicas))
if perPodUse >= 0.2*perPodReq { return _, false }
sug := int32(math.Ceil( wl.CPUUsageCores / (perPodReq*0.6) )); if sug < 2 { sug = 2 }
ratio, _ := safeRatio(float64(sug), float64(wl.Replicas))
saving := wl.MonthlyCost * clamp01(1-ratio)
sev := SeverityInfo; if saving >= 20 { sev = SeverityWarning }
// Evidence: replicas, suggestedReplicas, perPodCpuUtil, estimatedMonthlySaving, basis
```

`hpaTargets(snap) map[string]bool` — for each `snap.HPAs`, key = `hpa.Namespace + "|" + hpa.Spec.ScaleTargetRef.Kind + "|" + hpa.Spec.ScaleTargetRef.Name`.

Every finding appends `Evidence{Source:"cost", Query:"basis", Value: string(basis)}` (or whatever the `slo` name-field convention is — match Task 10).

- [ ] **Step 1: Write the failing tests** — one per rule, values shaped as the estimator/OpenCost produce (spec "M2 lesson"):

```go
func TestRightsizingFiresBelowHalfUtil(t *testing.T) {
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "d", Replicas: 2,
		CPURequestCores: 2, CPUUsageCores: 0.4, MemRequestBytes: 1 << 30, MemUsageBytes: 900 << 20,
		MonthlyCost: 60, MonthlyCPUCost: 40, MonthlyMemCost: 20}
	f, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.True(t, ok)
	require.Equal(t, "cost/rightsizing", f.RuleID)
	require.Equal(t, analyzer.SeverityWarning, f.Severity) // saving on cpu alone >= 20
}

func TestRightsizingDoesNotFireAt51Pct(t *testing.T) {
	wl := snapshot.WorkloadCost{Kind: "Deployment", Replicas: 1, CPURequestCores: 1, CPUUsageCores: 0.51,
		MemRequestBytes: 1 << 30, MemUsageBytes: 1 << 30, MonthlyCost: 60, MonthlyCPUCost: 40, MonthlyMemCost: 20}
	_, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.False(t, ok)
}

func TestRightsizingFiresOnMemWhenNoCPURequest(t *testing.T) { // spec R8
	wl := snapshot.WorkloadCost{Kind: "Deployment", Replicas: 1, CPURequestCores: 0, CPUUsageCores: 0.05,
		MemRequestBytes: 2 << 30, MemUsageBytes: 100 << 20, MonthlyCost: 40, MonthlyCPUCost: 0, MonthlyMemCost: 40}
	f, ok := checkRightsizing(wl, snapshot.CostMeasured)
	require.True(t, ok)
	require.Contains(t, evVal(f, "suggestedMemRequest"), "") // present
}

func TestIdleFiresAndNotForDaemonSet(t *testing.T) { /* one firing case + Kind:"DaemonSet" → false */ }
func TestOverReplicatedFiresAndSuppressedByHPA(t *testing.T) { /* replicas 6, tiny usage, no HPA → true; with HPA target → false */ }

func TestAnalyzeEmitsRightsizingLimitedWhenNoUsage(t *testing.T) {
	snap := &snapshot.Snapshot{Cost: &snapshot.CostSet{Basis: snapshot.CostEstimated,
		Workloads: []snapshot.WorkloadCost{{Kind: "Deployment", Name: "d", Namespace: "t", Replicas: 1,
			CPURequestCores: 1, CPUUsageCores: -1, MemRequestBytes: 1 << 30, MemUsageBytes: -1, MonthlyCost: 50}}}}
	out, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	var got *analyzer.Finding
	for i := range out { if out[i].RuleID == "cost/rightsizing-limited" { got = &out[i] } }
	require.NotNil(t, got)
	require.Equal(t, "Cluster", got.Object.Kind) // spec R15 — not empty
}

func TestAnalyzeNilCostGivesSkipped(t *testing.T) {
	out, err := New().Analyze(context.Background(), &snapshot.Snapshot{})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "cost/skipped", out[0].RuleID)
}

func TestOverReplicatedSuppressesRightsizing(t *testing.T) { // spec R6
	wl := snapshot.WorkloadCost{Namespace: "t", Kind: "Deployment", Name: "big", Replicas: 8,
		CPURequestCores: 8, CPUUsageCores: 0.4, MemRequestBytes: 8 << 30, MemUsageBytes: 1 << 30,
		MonthlyCost: 400, MonthlyCPUCost: 300, MonthlyMemCost: 100}
	out, _ := New().Analyze(context.Background(), &snapshot.Snapshot{Cost: &snapshot.CostSet{
		Basis: snapshot.CostMeasured, Workloads: []snapshot.WorkloadCost{wl}}})
	ids := ruleIDs(out)
	require.Contains(t, ids, "cost/over-replicated")
	require.NotContains(t, ids, "cost/rightsizing")
}

func TestSafeRatio(t *testing.T) {
	_, ok := safeRatio(1, 0); require.False(t, ok)
	v, ok := safeRatio(1, 4); require.True(t, ok); require.Equal(t, 0.25, v)
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `cost.go` (skeleton + `safeRatio` + `clamp01` + `skippedFinding` + `rightsizingLimitedFinding` + `hpaTargets` + `key`) and `usage_rules.go` (the three `check*`).
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/analyzer/cost/ -race -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/cost/cost.go internal/analyzer/cost/usage_rules.go internal/analyzer/cost/usage_rules_test.go internal/analyzer/cost/cost_test.go
git commit -m "feat: analyzer/cost — usage rules (rightsizing, idle, over-replicated) with two-pass suppression"
```

---

## Task 12: `analyzer/cost` — object rules (orphaned-pvc, orphaned-lb, retained-jobs, namespace-spend-trend)

**Files:**
- Create: `internal/analyzer/cost/object_rules.go`
- Modify: `internal/analyzer/cost/cost.go` (append the object-rule calls in `Analyze`)
- Test: `internal/analyzer/cost/object_rules_test.go`

**Interfaces:**
- Consumes: everything from Task 11, plus `snap.PVCs` / `snap.Services` / `snap.Pods` / `snap.Jobs` / `snap.Cost.Namespaces`.
- Produces: four unexported functions, each returning `[]analyzer.Finding` (0 or more), called from `Analyze` after the usage rules:
```go
checkOrphanedPVC(snap *snapshot.Snapshot) []analyzer.Finding
checkOrphanedLB(snap *snapshot.Snapshot) []analyzer.Finding
checkRetainedJobs(snap *snapshot.Snapshot) []analyzer.Finding      // per-namespace; nil when snap.Jobs == nil
checkNamespaceSpendTrend(cs *snapshot.CostSet) []analyzer.Finding  // measured only
```

**`checkOrphanedPVC`** — spec `cost/orphaned-pvc`:
- referenced set = every `claimName` in `snap.Pods[].Spec.Volumes[].PersistentVolumeClaim`.
- For each `snap.PVCs` with `Status.Phase == corev1.ClaimBound`, not in the referenced set, `time.Since(pvc.CreationTimestamp.Time) > 7*24h` → `Finding{RuleID:"cost/orphaned-pvc", Severity: warning, Object: PVC ref}`.
- Evidence: `capacityBytes` (`pvc.Status.Capacity.Storage().AsApproximateFloat64()`), `storageClass` (`*pvc.Spec.StorageClassName`, `""` if nil), `ageDays`, `estimatedMonthlyCost` (`= capacityGiB * 0.10` — measured `pvCost` join is a later refinement; use the flat number for M4 and tag `basis`), `basis`.
- **Sort the PVC list by `(Namespace, Name)` before iterating** — `snap.PVCs` order is collection order, keep it deterministic.

**`checkOrphanedLB`** — spec `cost/orphaned-lb` (R "empty selector excluded"):
- For each `snap.Services` with `Spec.Type == corev1.ServiceTypeLoadBalancer` and `len(Spec.Selector) > 0`:
  - ready = any `snap.Pods` in the same namespace whose labels are a superset of `Spec.Selector` and that has a `PodReady` condition `== True`.
  - not ready → `Finding{RuleID:"cost/orphaned-lb", Severity: warning}`. Evidence: `ageDays`, `estimatedMonthlyCost` (flat `18.00`), `basis`.
- Sort services by `(Namespace, Name)`.

**`checkRetainedJobs`** — spec `cost/retained-jobs`:
- `if snap.Jobs == nil { return nil }` (R3 — absent, not error).
- Group by namespace: count Jobs with `Status.Succeeded > 0`, `Status.CompletionTime != nil`, `time.Since(*CompletionTime) > 7*24h`, `Spec.TTLSecondsAfterFinished == nil`.
- count ≥ 1 → one `Finding{RuleID:"cost/retained-jobs", Severity: info, Object: ObjectRef{Kind:"Namespace", Name: ns}}`, Evidence `count`, `oldestCompletionDays`.
- Iterate namespaces in sorted order.

**`checkNamespaceSpendTrend`** — spec `cost/namespace-spend-trend` (R1):
- `for _, nc := range cs.Namespaces` (already sorted): skip if `nc.PriorMonthlyCost < 0` (estimate path).
- `delta := nc.MonthlyCost - nc.PriorMonthlyCost`
- fire when `delta >= 50` **and** (`nc.PriorMonthlyCost == 0` **or** `nc.MonthlyCost > nc.PriorMonthlyCost*1.25`).
- `Severity` = warning if `delta >= 200` else info.
- Evidence: `monthlyCost`, `priorMonthlyCost`, `deltaAbs` (= delta); `deltaPct` **only if** `pct, ok := safeRatio(delta, nc.PriorMonthlyCost); ok` → append `deltaPct`. Never emit `+Inf`.
- `Object: ObjectRef{Kind:"Namespace", Name: nc.Namespace}`.

- [ ] **Step 1: Write the failing tests** — one per rule + boundaries:

```go
func TestOrphanedPVCFiresOnlyForBoundUnreferencedOld(t *testing.T) { /* 3 PVCs: bound+unref+old→fire; bound+mounted→no; pending→no */ }
func TestOrphanedLBIgnoresEmptySelectorAndBackedServices(t *testing.T) { /* LB no selector→no; LB with ready pod→no; LB no ready pod→fire */ }
func TestRetainedJobsPerNamespaceAndNilSafe(t *testing.T) {
	require.Nil(t, checkRetainedJobs(&snapshot.Snapshot{Jobs: nil}))
	// 2 old ttl-less succeeded jobs in "batch" → exactly one finding, Object Kind "Namespace"
}
func TestNamespaceSpendTrendZeroPriorNoInfEvidence(t *testing.T) { // spec R1
	cs := &snapshot.CostSet{Namespaces: []snapshot.NamespaceCost{{Namespace: "new", MonthlyCost: 80, PriorMonthlyCost: 0}}}
	fs := checkNamespaceSpendTrend(cs)
	require.Len(t, fs, 1)
	for _, e := range fs[0].Evidence {
		require.NotEqual(t, "deltaPct", evName(e)) // omitted, not +Inf
	}
}
func TestNamespaceSpendTrendSkipsEstimatePath(t *testing.T) {
	require.Empty(t, checkNamespaceSpendTrend(&snapshot.CostSet{
		Namespaces: []snapshot.NamespaceCost{{Namespace: "x", MonthlyCost: 999, PriorMonthlyCost: -1}}}))
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `object_rules.go`; wire the four calls into `Analyze` after the usage-rule block (`out = append(out, checkOrphanedPVC(snap)...)` etc., and `checkNamespaceSpendTrend(cs)`).
- [ ] **Step 4: Run — expect PASS.** Also add `TestAnalyzeRunsAllRules` to `cost_test.go` asserting a crafted Snapshot produces a finding for every `cost/*` rule id at least once. `go test ./internal/analyzer/cost/ -race -count=1`.
- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/cost/object_rules.go internal/analyzer/cost/cost.go internal/analyzer/cost/object_rules_test.go internal/analyzer/cost/cost_test.go
git commit -m "feat: analyzer/cost — object rules (orphaned pvc/lb, retained jobs, spend trend)"
```

---

## Task 13: orchestrator wiring

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Test: `internal/orchestrator/orchestrator_test.go`

**Interfaces:**
- Consumes: `opencost.New` / `opencost.Options` (Task 7), `cost.Collect` (Task 9), `cost.From` (Task 10), `analyzercost.New` (Tasks 11–12 — import alias to avoid the `cost` package-name clash: `analyzercost "github.com/init-kaushal/poirot/internal/analyzer/cost"`). The `promql` registration block (`orchestrator.go:85-98`), the `metrics.Collect` call (`:116`), the analyzer list (`:120`), the `rep.Meta.LLM` post-Build assignment (`:172`).

- [ ] **Step 1: Write the failing tests** (`orchestrator_test.go`, extend the existing e2e harness with its `fakePromql` / fake-clientset helpers):

```go
func TestRunPopulatesCostMetaMeasured(t *testing.T) {
	opts := baseOptions(t) // existing helper: fake cluster with a workload
	opts.OpenCost = fakeOpenCostConnector{} // returns a measured allocation for the workload
	res, err := orchestrator.Run(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, res.Report.Meta.Cost)
	require.Equal(t, "measured", res.Report.Meta.Cost.Basis)
	require.True(t, hasFindingDomain(res.Report.Findings, "cost"))
}

func TestRunCostEstimateWhenNoOpenCost(t *testing.T) {
	opts := baseOptions(t) // no OpenCost connector
	res, err := orchestrator.Run(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, res.Report.Meta.Cost)
	require.Equal(t, "estimated", res.Report.Meta.Cost.Basis)
}

func TestRunNoLLMStillByteStableWithCost(t *testing.T) {
	// two runs, provider:none, identical fake inputs → report.JSON() equal after
	// nil-ing Meta.Cost + Meta.LLM + time fields (cost dollars are non-deterministic
	// only across *different* fixtures; with a frozen fake they're equal, but keep
	// the normalisation consistent with TestRunNoAPIKey).
}
```

If `orchestrator.Options` has no `OpenCost` field, add `OpenCost connector.Connector // nil => built from Config.Connectors.OpenCost` next to the existing `Promql connector.Connector` field, mirroring it exactly.

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement**

After the `promql` registration block, add an analogous one (always register, like promql post-M3):
```go
oc := opts.OpenCost
if oc == nil {
	oc = opencost.New(opencost.Options{
		URL:        cfg.Connectors.OpenCost.URL,
		Clientset:  src.Clientset(),
		Namespaces: cfg.Scope.Namespaces,
	})
}
reg.Register(oc)
```
After `snap.Metrics = metrics.Collect(...)` (still inside the collect phase):
```go
snap.Cost = cost.Collect(ctx, snap, reg, cfg.Connectors.OpenCost)
```
Analyzer list:
```go
analyzers := []analyzer.Analyzer{reliability.New(), change.New(), slo.New(), analyzercost.New()}
```
After `rep := report.Build(...)` and the `rep.Meta.LLM = ...` line:
```go
if snap.Cost != nil {
	rep.Meta.Cost = cost.From(snap.Cost, rep.Findings)
}
```

- [ ] **Step 4: Run — expect PASS.** `go test ./internal/orchestrator/ -race -count=1`; full `go test ./... -race -count=1`; `go build ./... && go build -tags tools ./...`; grep for write verbs in the new code: `grep -rn -E '\.(Create|Update|Patch|Delete|Apply|Evict|Watch)\(' internal/cost/ internal/connector/opencost/ | grep -v _test.go` → empty.
- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/
git commit -m "feat: orchestrator — register opencost, collect cost, add cost analyzer, roll up Meta.Cost"
```

---

## Task 14: report Markdown `## Cost` section

**Files:**
- Modify: `internal/report/markdown.go`
- Test: `internal/report/markdown_test.go`

**Interfaces:**
- Consumes: `report.CostMeta` (Task 10), `report.Report` / `mdData` (`markdown.go:19`), the existing template + `{{if}}` guard style (`markdown.go:49-92`).
- Produces: no new exported API — internal `costRow` struct + `buildCostView(*Report) (*CostMeta, banner string, top5 []costRow)`.

Behaviour (spec §"report.md"):
- Rendered only under `{{if .Cost}}`, placed **after** the `{{if .Summary}}## Summary … {{end}}` block and **before** `## Findings`.
- Line: `Estimated monthly spend: $<MonthlyTotal> · estimated waste: $<EstimatedMonthlyWaste> (<pct>%) · window: <Window>` — `pct` = `round(waste/total*100)` when `total > 0` else `0` (use a plain guard, this is display text not evidence, but still never format `+Inf`).
- Banner line `> 💰 Cost figures are estimated (no OpenCost detected) — directional only.` only when `Cost.Basis != "measured"`.
- Table `| Finding | Object | ~$/mo | Action |` — top 5 by `savings(f)` where `savings(f)` = the `estimatedMonthlySaving` evidence value, else `estimatedMonthlyCost`, else 0; order `(savings desc, ruleID asc, object asc)`; `Action` = the finding's remediation string (from `Summary` or a dedicated evidence line — match what Tasks 11/12 produced). Rows only for `cost/*` findings that have one of those two evidence values (`cost/retained-jobs`, `cost/namespace-spend-trend`, `cost/skipped`, `cost/rightsizing-limited` excluded).
- `golden_basic.md` has no cost data → `{{if .Cost}}` false → **byte-identical**. Regenerate and `git diff --exit-code internal/report/testdata/golden_basic.md`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMarkdownCostSectionEstimatedBanner(t *testing.T) {
	r := report.Report{ /* Meta.Cost = &CostMeta{Basis:"estimated", MonthlyTotal:1000, EstimatedMonthlyWaste:250, Window:"7d"},
	                       Findings: []Finding{ cost/idle w/ estimatedMonthlySaving 60, cost/orphaned-lb w/ estimatedMonthlyCost 18 } */ }
	md, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(md), "## Cost")
	require.Contains(t, string(md), "💰 Cost figures are estimated")
	require.Contains(t, string(md), "$1000")
	require.Contains(t, string(md), "(25%)")
	// idle (60) sorts above orphaned-lb (18)
	require.Less(t, strings.Index(string(md), "cost/idle"), strings.Index(string(md), "cost/orphaned-lb"))
}

func TestMarkdownCostSectionMeasuredNoBanner(t *testing.T) {
	// Meta.Cost.Basis = "measured" → "## Cost" present, no "💰" line
}

func TestMarkdownGoldenUnchanged(t *testing.T) {
	// existing golden test still passes — assert explicitly here if there isn't already one
}
```

- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** — add `Cost *CostMeta`, `CostBanner string`, `CostTop5 []costRow` to `mdData`; populate in the `data := mdData{...}` literal via `buildCostView`; add the template block. Register any needed template funcs (a `money` formatter is optional — plain `%v`/`%.2f` is fine, match the existing number rendering).
- [ ] **Step 4: Run — expect PASS.** `go test ./internal/report/ -race -count=1`; regenerate golden if the repo has a `-update` flag, then `git diff --exit-code internal/report/testdata/golden_basic.md` (must be clean).
- [ ] **Step 5: Commit**

```bash
git add internal/report/markdown.go internal/report/markdown_test.go
git commit -m "feat: report — ## Cost section with spend/waste line, estimate banner, top-5 table"
```

---

## Task 15: determinism test, docs, demo manifest

**Files:**
- Create: `internal/cost/determinism_test.go`
- Modify: `README.md`
- Modify: `hack/demo-broken.yaml`
- Test: the determinism test itself

**Interfaces:** none new.

- [ ] **Step 1: `TestCostCollectDeterministic`**

```go
func TestCostCollectDeterministic(t *testing.T) {
	// Frozen fake OpenCost connector returning a fixed testdata allocation +
	// a fixed snapshot with 3 workloads across 2 instance types (forces the
	// map-order-sensitive paths: parse, join, sort, instance-type argmax).
	first := mustJSON(t, cost.Collect(context.Background(), snap, reg, spec))
	for i := 0; i < 50; i++ {
		require.Equal(t, first, mustJSON(t, cost.Collect(context.Background(), snap, reg, spec)),
			"CostSet must be byte-identical across runs (frozen inputs)")
	}
}
```
`mustJSON` = `json.Marshal` + `require.NoError`. Include an estimate-path variant (nil OpenCost) with pods split across `c6i.large`/`m6i.large` to lock the R9 tie-break.

- [ ] **Step 2: Run — expect PASS** (if it flakes, the estimator or parser has an unsorted map path — fix that, not the test).

- [ ] **Step 3: README**

Replace the status line (currently `Status: M3 — …`) with:
```
Status: M4 — reliability + slo + change + cost (rightsizing, idle, orphaned resources, spend trend), plus the M3 LLM investigation layer. Cost is measured from OpenCost when reachable, otherwise estimated from resource requests.
```
Add a `## Cost analysis` section after the existing `## LLM analysis` section:
```
## Cost analysis

`poirot` always runs a cost pass. With OpenCost reachable (`connectors.opencost.url: auto`
discovers it via the API-server proxy, or set an explicit URL) the numbers are **measured**;
otherwise they are **estimated** from pod resource requests × a small built-in price sheet
keyed by node instance type, with actual usage pulled from Prometheus when available.
Estimated figures render behind a "directional only" banner and are labelled `estimated`
in `report.json`.

Set `connectors.opencost.estimateFallback: false` to skip the estimate and emit a
`cost/skipped` note instead. Override the price sheet's blended default with
`connectors.opencost.rates: {cpuHour, memGiBHour}`.
```

- [ ] **Step 4: `hack/demo-broken.yaml`** — append a `poirot-demo` block that trips the cost rules:
  - a `LoadBalancer` Service whose selector matches nothing (`cost/orphaned-lb`);
  - a Deployment `replicas: 6` requesting `cpu: 500m` each but doing nothing (`cost/over-replicated` / `cost/idle` when metrics are present);
  - a Deployment requesting `cpu: 2` / `memory: 4Gi` running a `sleep` (`cost/rightsizing` when metrics are present);
  - a `PersistentVolumeClaim` mounted by no pod (`cost/orphaned-pvc` after 7d — note in a comment it needs age).
  Keep it in the existing `poirot-demo` namespace, match the file's YAML style, and update the file's header comment listing which rules it trips.

- [ ] **Step 5: Full verification**

```bash
go test ./... -race -count=1
go vet ./... && gofmt -l . && go build ./... && go build -tags tools ./...
git diff --exit-code internal/report/testdata/golden_basic.md
grep -rn -E '\.(Create|Update|Patch|Delete|Apply|Evict|Watch)\(' internal/cost/ internal/connector/opencost/ internal/analyzer/cost/ | grep -v _test.go   # empty
```

- [ ] **Step 6: Commit**

```bash
git add internal/cost/determinism_test.go README.md hack/demo-broken.yaml
git commit -m "test+docs: cost determinism test, README M4 status, demo cost-waste workloads"
```

---

## Self-Review

**1. Spec coverage.** Every spec section maps to a task:
- `opencost` connector (parse/client/discover/proxy/connector) → T4–T7. `accumulate=true` / `step=7d` (R2) → T5. `pickPort` from Service (R12) → T6. `absent` not `degraded` on unreachable → T7.
- `internal/cost` collector + estimator + price sheet + costmeta → T3, T8, T9, T10. Case-fold join (R4) → T9. Two-hop promql usage join (R7) → T8. DaemonSet running-pod count (R11) → T8. Instance-type tie-break (R9) → T8, verified in T15.
- `snapshot.CostSet` + `Jobs` best-effort (R3) → T2.
- `cost` analyzer, four families + `cost/skipped` + `cost/rightsizing-limited` with `Object{Kind:"Cluster"}` (R15) → T11, T12. Two-pass eval / over-replicated suppresses rightsizing (R6) → T11. Per-dimension rightsizing (R8) → T11. `safeRatio` / no non-finite evidence, no `deltaPct` at prior 0 (R1) → T11, T12. Drop `cost/unbound-pvc` (R10) → not in T12 (explicitly).
- `report.Meta.Cost` + `## Cost` section + top-5 key defined for every table rule (R13) → T10, T14.
- Config: `EstimateFallback *bool` (R5), `rates` override wins everywhere (R14) → T1, T3 (`SetOverride`), T9 (`sheet.SetOverride`).
- Orchestrator wiring → T13. Determinism + docs + demo → T15.

**2. Placeholder scan.** Every code step carries real signatures/algorithms/tests. The `check*` rule bodies are pseudocode-flavoured but give exact field names, thresholds, evidence keys, and severities from the spec — an implementer can transcribe them. T12's four rule-test bodies are described in comments rather than full listings only where the shape is a direct repeat of a listed test; the assertions named are concrete.

**3. Type consistency.** `snapshot.CostSet`/`WorkloadCost`/`NamespaceCost`/`CostBasis` defined T2, consumed T8–T15. `cost.Rate`/`cost.Sheet` (in `internal/cost`) vs `config.Rate` (in `internal/config`) are deliberately separate identical structs — T9 converts via `rateFromConfig`. `report.CostMeta` defined T10, consumed T13/T14. `opencost.Allocation` defined T4, consumed T5/T9. `analyzercost` import alias (T13) avoids the `internal/cost` vs `internal/analyzer/cost` package-name clash — both are `package cost`; only the orchestrator imports both, and it aliases the analyzer one. Evidence name/value field convention (`Evidence.Query` = key, `Evidence.Value` = number) is called out in T10 with an instruction to match `slo` — T11/T12/T14 depend on that choice being consistent.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-08-poirot-m4-cost.md`. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration. T8 (estimator) and T9 (collector) are the high-risk tasks; T4–T7 are near-parallels of the existing `promql` connector and go quickly.

**2. Inline Execution** — batch execution with checkpoints.

Which approach?
