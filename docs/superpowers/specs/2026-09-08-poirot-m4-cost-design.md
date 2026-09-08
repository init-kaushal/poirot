# poirot M4 — OpenCost connector + cost analyzer (design)

**Status:** Design approved in brainstorm, pending spec review
**Date:** 2026-09-08
**Author:** Kaushal Kishor Sharma
**Parent spec:** `docs/superpowers/specs/2026-09-01-poirot-sre-assessment-agent-design.md` (this refines its M4 milestone)
**Predecessors:** M1 (`2026-09-01-poirot-m1-skeleton.md`, merged), M2 (`2026-09-03-poirot-m2-metrics-change.md`, merged `e90cf69`), M3 (`2026-09-05-poirot-m3-agent.md`, merged `972fab5`)

## Summary

M4 adds the **cost** domain: an `opencost` connector, an `internal/cost` collection layer with an
embedded requests-only estimator for clusters without OpenCost, and a `cost` analyzer that turns
allocation + usage data into ranked, actionable waste findings (rightsizing, idle workloads,
orphaned resources, namespace spend trend, over-replication). It follows the M2 three-layer split
exactly: thin connector → `internal/cost` collector (analog of `internal/metrics`) → pure analyzer
over an immutable `snapshot.CostSet`.

The `cost` analyzer **always runs** (`Requires() == ["k8s"]`). When OpenCost is reachable it
reports **measured** costs; otherwise it **estimates** from pod resource requests × a small
embedded price sheet keyed by node instance-type, with actual usage pulled from the `promql`
connector when that is present. Every finding and the rollup carry a `basis: measured | estimated`
tag, and estimated output renders behind a "directional only" banner.

Deterministic core stays byte-identical: cost findings' *structure* (rule id, object, evidence
keys, title, severity, sort order) is stable; dollar *values* in evidence and the entire
`report.Meta.Cost` block are non-deterministic, the same bucket as `slo` metric values and
`report.Meta.LLM`.

## Locked decisions (from brainstorm)

| # | Decision |
|---|---|
| Fallback | OpenCost + a **requests-only estimate** when OpenCost is absent. Estimate = Σ pod requests × embedded price sheet; usage from `promql` if registered, else rightsizing degrades to a requests-vs-limits proxy. Not full self-estimate parity — no node→instance pricing engine beyond a ~6-entry starter sheet. |
| Rules | All four families ship in M4: **rightsizing**, **idle/near-idle**, **orphaned resources** (unbound/orphaned PVC, orphaned LoadBalancer, retained Jobs), **namespace spend trend + over-replication**. |
| Price sheet | Small **embedded** table (`//go:embed prices.yaml`): ~6 common instance families (aws `m6i`/`c6i`/`r6i`, gcp `n2`/`e2`, azure `Dv5`) as `{cpuHour, memGiBHour}`, plus a blended `default`. `connectors.opencost.rates: {cpuHour, memGiBHour}` overrides the default. All estimate output labelled `estimated`. |
| Cost window | **Fixed 7d**, independent of `scope.lookback` (which stays 24h for reliability/event rules). The trend rule uses OpenCost's own `14d` window split into two 7d steps (last-7d vs prior-7d). No poirot-side historical storage. |
| Layering | Approach 1: thin `opencost` connector + `internal/cost` collector (embedded estimator) + always-on `cost` analyzer. The analyzer sees one `snapshot.CostSet` shape regardless of source. |
| Analyzer gating | `Requires() == ["k8s"]`. Nil `snapshot.Cost` (only when `estimateFallback: false` and OpenCost absent, or the price sheet fails to load) → single `cost/skipped` info finding. |
| Determinism | Cost finding structure + sort order byte-stable. Dollar values in evidence and `report.Meta.Cost` are non-deterministic and demarcated, exactly like `slo` values / `report.Meta.LLM`. |

## Non-goals (M4)

- `/assets` node- or cluster-level cost breakdown; spot vs on-demand; reserved-instance / savings-plan awareness.
- poirot-side historical cost storage or cross-run diffing (the trend rule uses OpenCost's 14d window only).
- Network-egress cost, shared-cost redistribution, custom allocation config.
- Per-container (vs per-workload) rightsizing; PDB / anti-affinity–aware replica-count math.
- A maintained multi-region price catalog — the embedded sheet is a fixed starter, `estimated` everywhere.
- Kubecost-specific API surface beyond the shared `/allocation/compute` shape.
- New LLM work: `cost` findings pick up `Analysis` in M3's existing investigate phase (batched by object) for free.

## Component architecture

New packages; nothing existing is restructured. Mirrors the M2 `promql` → `metrics` → `slo` split.

```
internal/
  connector/opencost/
    opencost.go     # connector.Connector: Name/Probe/Capabilities/Query
    discover.go     # well-known Service lookup + API-server proxy (mirrors promql/discover.go)
    client.go       # thin HTTP: GET {base}/allocation/compute?window=&aggregate= ; pluggable doer; 1 MiB LimitReader
    parse.go        # OpenCost allocation JSON -> []Allocation
  cost/
    collect.go      # Collect(ctx, snap, reg, spec) *snapshot.CostSet — opencost.allocation, else estimate; sorts everything
    estimate.go     # requests-only estimator; usage from the registry's promql connector if present
    pricesheet.go   # Load() (*Sheet, error); Sheet{ByInstanceType map[string]Rate; Default Rate}; config override
    prices.yaml     # //go:embed asset — ~6 instance families + default
    costmeta.go     # From(*snapshot.CostSet, []analyzer.Finding) *report.CostMeta  (rollup)
  analyzer/cost/
    cost.go         # Analyzer: ID()="cost", Requires()=["k8s"], Analyze
    rules.go        # the four rule families
  snapshot/
    snapshot.go     # + Cost *CostSet ; CostSet/WorkloadCost/NamespaceCost/CostBasis types ; + Jobs []batchv1.Job
  connector/k8s/
    collect.go      # + Jobs list (BatchV1().Jobs(ns).List) per scoped namespace, read-only
  orchestrator/
    orchestrator.go # register opencost; snap.Cost = cost.Collect(...) after metrics.Collect; analyzer list += cost.New(); rep.Meta.Cost post-Build
  report/
    report.go       # + Meta.Cost *CostMeta (json:"cost,omitempty")
    markdown.go     # + "## Cost" section (spend/waste line, estimated banner, top-5 savings table), {{if .Cost}}-guarded
  config/
    config.go       # activate connectors.opencost: url switch validation; rates override validation; tidy the M1 EstimateFallback applyDefaults comment
```

Analyzer list becomes `[]analyzer.Analyzer{reliability.New(), change.New(), slo.New(), cost.New()}`
(`cost` last is cosmetic — it only affects finding grouping in output). The real ordering
constraint is in the *collect* phase: `cost.Collect` must run **after** `metrics.Collect` so the
estimator can read a populated `snapshot.Metrics`.

## Types

### `snapshot` additions (leaf package — json-tagged, must not import `analyzer`)

```go
type CostBasis string // "measured" | "estimated"

type CostSet struct {
    Basis       CostBasis       `json:"basis"`
    Source      string          `json:"source"`   // "opencost" | "estimate"
    Currency    string          `json:"currency"` // "USD"
    Window      string          `json:"window"`   // "7d"
    CollectedAt time.Time       `json:"collectedAt"`
    Workloads   []WorkloadCost  `json:"workloads"`  // sorted by (namespace, kind, name)
    Namespaces  []NamespaceCost `json:"namespaces"` // sorted by namespace
    Note        string          `json:"note,omitempty"`
}

type WorkloadCost struct {
    Namespace string `json:"namespace"`
    Kind      string `json:"kind"` // Deployment | StatefulSet | DaemonSet | ...
    Name      string `json:"name"`
    Replicas  int32  `json:"replicas"`

    CPURequestCores float64 `json:"cpuRequestCores"` // average over the window
    CPUUsageCores   float64 `json:"cpuUsageCores"`   // -1 when usage is unavailable
    MemRequestBytes float64 `json:"memRequestBytes"`
    MemUsageBytes   float64 `json:"memUsageBytes"`   // -1 when usage is unavailable

    MonthlyCost    float64 `json:"monthlyCost"`    // normalized to a 30d month
    MonthlyCPUCost float64 `json:"monthlyCpuCost"`
    MonthlyMemCost float64 `json:"monthlyMemCost"`
}

type NamespaceCost struct {
    Namespace        string  `json:"namespace"`
    MonthlyCost      float64 `json:"monthlyCost"`      // last 7d, normalized
    PriorMonthlyCost float64 `json:"priorMonthlyCost"` // prior 7d; -1 on the estimate path
}
```

`Snapshot` also gains `Jobs []batchv1.Job` (unexported concern: `import batchv1 "k8s.io/api/batch/v1"`).

### `report` additions

```go
type CostMeta struct {
    Basis                 string  `json:"basis"`    // "measured" | "estimated"
    Currency              string  `json:"currency"`
    Window                string  `json:"window"`
    MonthlyTotal          float64 `json:"monthlyTotal"`          // sum of workload monthly cost
    EstimatedMonthlyWaste float64 `json:"estimatedMonthlyWaste"` // sum of findings' estimatedMonthlySaving
    Note                  string  `json:"note,omitempty"`
}
```

`report.Meta` gains `Cost *CostMeta \`json:"cost,omitempty"\`` after `LLM`. `report.Build`'s signature is unchanged; the orchestrator sets `rep.Meta.Cost` on the returned value (same pattern M3 established for `rep.Meta.LLM` / `rep.Summary`).

### `pricesheet`

```go
type Rate struct {
    CPUHour    float64 `json:"cpuHour" yaml:"cpuHour"`       // USD per vCPU-hour
    MemGiBHour float64 `json:"memGiBHour" yaml:"memGiBHour"` // USD per GiB-hour
}
type Sheet struct {
    Default        Rate            `yaml:"default"`
    ByInstanceType map[string]Rate `yaml:"byInstanceType"`
}
func Load() (*Sheet, error)                 // parses the embedded prices.yaml
func (s *Sheet) Rate(instanceType string) Rate // exact match on ByInstanceType, else Default
```

`prices.yaml` (approximate on-demand list prices, each instance's hourly price split ~70/30
across its vCPUs and GiB; USD):

```yaml
default: { cpuHour: 0.031, memGiBHour: 0.004 }
byInstanceType:
  m6i.large:       { cpuHour: 0.0210, memGiBHour: 0.0028 }
  c6i.large:       { cpuHour: 0.0250, memGiBHour: 0.0033 }
  r6i.large:       { cpuHour: 0.0165, memGiBHour: 0.0022 }
  n2-standard-4:   { cpuHour: 0.0240, memGiBHour: 0.0032 }
  e2-standard-4:   { cpuHour: 0.0170, memGiBHour: 0.0023 }
  Standard_D4s_v5: { cpuHour: 0.0230, memGiBHour: 0.0031 }
```

A `connectors.opencost.rates` config value overrides `Sheet.Default` (not the per-type entries).

## The `opencost` connector

- `Name()` → `"opencost"`.
- `Capabilities()` → one capability `opencost.allocation`, `ArgsSchema`:
  `{"window": "string (e.g. 7d or an RFC3339 pair)", "aggregate": "string (e.g. namespace or namespace,controller)"}`.
- `Query("opencost.allocation", args)` → the raw response body as `json.RawMessage`. Parsing into
  `[]Allocation` is `parse.go`'s job, called from `cost.Collect` — identical to how `promql` returns
  raw and `metrics` parses.
- `client.go`: `GET {base}/allocation/compute?window={window}&aggregate={aggregate}&accumulate=false`.
  Pluggable `doer` (like `promql/client.go`), 1 MiB `io.LimitReader` cap, body-read errors surfaced.
- `discover.go`: Service `name ∈ {opencost, kubecost-cost-analyzer, cost-analyzer}` in
  `namespace ∈ {opencost, monitoring, kubecost, kube-system}`, port `9003` (opencost) or `9090`
  (kubecost cost-analyzer). One `Services("").List` + sort by `(namespace, name)` + first match
  (carry-forward from the promql two-pass discovery review). Reached via
  `Services(ns).ProxyGet(scheme, name, port, "/allocation/compute", params)`.
- `Probe(ctx)`:
  - `url: disabled` → `absent`, reason `"disabled in config"` (matches `promql` post-M3).
  - `url: auto` → discover; found and a probe `GET` returns 200 → `available`; not found →
    `absent`, reason `"no OpenCost service found"`.
  - explicit `url:` → cheap `GET {base}/allocation/compute?window=1m&aggregate=namespace`;
    reachable → `available`, else `absent` with the dial/HTTP error as the reason.
- `absent` is a **normal** outcome, never an error. `cost.Collect` then estimates.

### Response shape consumed

`{"code":200,"data":[ { "<key>": {
  "properties": {"namespace","controller","controllerKind","pod"},
  "cpuCoreRequestAverage", "cpuCoreUsageAverage", "cpuCost", "cpuEfficiency",
  "ramByteRequestAverage", "ramByteUsageAverage", "ramCost", "ramEfficiency",
  "pvCost", "loadBalancerCost", "totalCost"
} } ]}`

`data` is an array of per-step maps. `cost.Collect` requests `accumulate=false` and, for the 7d
call, expects a single step; for the 14d trend call it expects two 7d steps and reads
`data[0]` (prior) and `data[1]` (recent). A row with `controllerKind == ""` (unallocated /
idle / `__idle__`) is skipped for workload rows but still counted into `NamespaceCost`.

## `internal/cost` — collection & estimation

### `Collect(ctx, snap *snapshot.Snapshot, reg *connector.Registry, spec config.ConnectorSpec) *snapshot.CostSet`

1. **Measured path** — `reg.Satisfied(["opencost"])` true:
   - Call `opencost.allocation` with `aggregate=namespace,controller`, `window=7d` → `Workloads`.
   - Call `opencost.allocation` with `aggregate=namespace`, `window=14d` → `Namespaces` (recent +
     prior).
   - Join `Workloads` to `snap` by `(namespace, controllerKind, controller)` to fill `Replicas`
     from the live object; drop allocation rows with no matching workload in scope.
   - `Basis: measured`, `Source: opencost`, `Currency` from the response (`"USD"` default).
   - Any query error → append to `CostSet.Note`, then fall through to the estimate path (the
     partial measured data is discarded to avoid a mixed set).
2. **Estimate path** — measured unavailable and `spec.EstimateFallback` true: `estimate.go`.
3. **Skip** — measured unavailable and `spec.EstimateFallback` false, or `pricesheet.Load()`
   errored: return `nil`.

Every slice in the returned `CostSet` is sorted (`Workloads` by `(namespace, kind, name)`,
`Namespaces` by `namespace`) before return — determinism.

### `estimate.go`

For each workload in `snap` (`Deployments`, `StatefulSets`, `DaemonSets`; `Kind` set accordingly):

- `CPURequestCores` = Σ over the pod template's containers of `resources.requests.cpu`,
  `MemRequestBytes` likewise, each × `spec.replicas` (`1` for DaemonSets is replaced by the
  count of ready nodes in scope).
- **Instance type**: for each of the workload's *running* pods, `pod.spec.nodeName` → node's
  `node.kubernetes.io/instance-type` (or the older `beta.kubernetes.io/instance-type`) label.
  Use the modal value across those pods. If the workload has no running pods, use the cluster's
  modal instance-type. If no node carries the label, use `Sheet.Default` and set `CostSet.Note`
  to `"no instance-type labels found; using blended default rate"`.
- `rate = sheet.Rate(instanceType)` (or the `connectors.opencost.rates` override for the default).
  `MonthlyCPUCost = CPURequestCores * rate.CPUHour * 730`,
  `MonthlyMemCost = MemRequestBytes / (1<<30) * rate.MemGiBHour * 730`,
  `MonthlyCost = MonthlyCPUCost + MonthlyMemCost` (PV/LB dollars are `0` on this path).
- **Usage**: if `reg.Satisfied(["promql"])`, issue one dedicated instant query pair via the
  registered `promql` connector —
  `sum by (namespace, owner_name) (rate(container_cpu_usage_seconds_total{...}[7d]))` and the
  working-set equivalent, joined to the workload via the standard `kube_pod_owner` /
  owner-reference chain — and fill `CPUUsageCores` / `MemUsageBytes`. On query error or no
  `promql`, set both to `-1` and append `"usage unavailable — rightsizing limited to requests vs limits"`
  to `CostSet.Note`.
- `NamespaceCost.MonthlyCost` = Σ workload cost in the namespace; `PriorMonthlyCost = -1`
  (no history on this path).
- `Basis: estimated`, `Source: estimate`.

### `costmeta.From(cs *snapshot.CostSet, findings []analyzer.Finding) *report.CostMeta`

`MonthlyTotal` = Σ `cs.Workloads[].MonthlyCost`. `EstimatedMonthlyWaste` = Σ of the
`estimatedMonthlySaving` evidence value across all `cost/*` findings that carry it. `Basis` /
`Currency` / `Window` / `Note` copied from `cs`. Returns `nil` when `cs` is `nil`.

## The `cost` analyzer

`ID() == "cost"`, `Requires() == ["k8s"]`, pure over `*snapshot.Snapshot`. `snap.Cost == nil` →
return a single `Finding{RuleID:"cost/skipped", Domain:"cost", Severity:info, Title:"Cost analysis skipped",
Summary:"cost analysis skipped — no OpenCost and estimate fallback disabled"}` (Summary text adapts if
the cause was a price-sheet load error).

Every non-skipped finding: `Domain = "cost"`, `Object` = the workload / namespace / PVC / Service
`ObjectRef`, and an `Evidence` entry `{Name:"basis", Detail: string(snap.Cost.Basis)}`. Dollar
evidence values use `Evidence.Value` (float) with `Name` ending `MonthlyCost` / `MonthlySaving`.

### `cost/rightsizing`

- **Skip condition**: if *no* workload has usage (`CPUUsageCores < 0` everywhere), emit **one**
  run-wide `Finding{RuleID:"cost/rightsizing-limited", Severity:info,
  Summary:"rightsizing needs usage data — deploy OpenCost or Prometheus"}` and run no per-workload
  rightsizing.
- **Per workload**, when `CPUUsageCores >= 0` and requests are set:
  - `cpuUtil = CPUUsageCores / CPURequestCores`, `memUtil = MemUsageBytes / MemRequestBytes`
    (a `0` request → that dimension's util is treated as `+inf`, i.e. never a rightsizing target).
  - Fire when `max(cpuUtil, memUtil) < 0.5` **and** `MonthlyCost >= 5`.
  - `suggestedCpuRequest = max(CPUUsageCores * 1.3, 0.010)`,
    `suggestedMemRequest = max(MemUsageBytes * 1.3, 16*1<<20)`.
  - `estimatedMonthlySaving = MonthlyCPUCost * (1 - suggestedCpuRequest/CPURequestCores)`
    `+ MonthlyMemCost * (1 - suggestedMemRequest/MemRequestBytes)`, clamped `>= 0`.
  - `Severity` = `warning` if `estimatedMonthlySaving >= 20`, else `info`.
  - Evidence: `cpuRequestCores`, `cpuUsageCores`, `memRequestBytes`, `memUsageBytes`,
    `suggestedCpuRequest`, `suggestedMemRequest`, `estimatedMonthlySaving`, `basis`.
  - `Summary`: `"<kind>/<name> uses <cpuUtil%>/<memUtil%> of its cpu/mem requests"`.
  - Remediation (in `Summary` or a dedicated evidence line — deterministic string):
    `"Set requests cpu=<X> mem=<Y> (usage ×1.3); saves ~$<Z>/mo"`.
- **Precedence**: a workload that also triggers `cost/over-replicated` is *excluded* here
  (replica reduction is the blunter, safer fix and would otherwise double-count).

### `cost/idle`

- Per workload: fire when `Replicas >= 1`, `Kind != "DaemonSet"`, `CPUUsageCores >= 0`,
  `CPUUsageCores / Replicas < 0.005` (< 5 millicores per pod), and `MonthlyCost >= 5`.
  If `promql` traffic data is available and shows a non-trivial request rate, do **not** fire
  (it is serving something).
- `Severity` = `warning` if `MonthlyCost >= 20`, else `info`.
- Evidence: `cpuUsageCores`, `replicas`, `monthlyCost`, `hasHPA` (true when an HPA in the
  snapshot targets this workload), `basis`.
- `Summary`: `"<kind>/<name> runs <replicas> replica(s) at ~<mCPU>m CPU — effectively idle"`.
- Remediation: `"Scale to zero (KEDA/Knative) or delete if unused; frees ~$<Z>/mo"`.

### `cost/orphaned-pvc` and `cost/unbound-pvc`

- `cost/orphaned-pvc`: PVC `status.phase == "Bound"`, **no** Pod in `snap.Pods` references it via
  `spec.volumes[].persistentVolumeClaim.claimName`, and PVC age `> 7d`. `Severity: warning`.
  Evidence: `capacityBytes`, `storageClass`, `ageDays`,
  `estimatedMonthlyCost` (OpenCost `pvCost` for that claim if `basis==measured`, else
  `capacityGiB * 0.10`), `basis`.
- `cost/unbound-pvc`: PVC `status.phase == "Pending"` for `> 1h`. `Severity: info`.
  Evidence: `ageHours`, `storageClass`.

### `cost/orphaned-lb`

- Service `spec.type == "LoadBalancer"` with zero ready backends. M4 derives "ready backends"
  by matching `spec.selector` against `snap.Pods` and checking for at least one `Ready` pod —
  no new collection. (A Service with an empty selector is treated as *not* orphaned, since its
  Endpoints are managed externally.)
- `Severity: warning`. Evidence: `ageDays`,
  `estimatedMonthlyCost` (OpenCost `loadBalancerCost` if measured, else flat `18.00`), `basis`.
- `Summary`: `"Service <ns>/<name> is type=LoadBalancer with no ready backends"`.

### `cost/retained-jobs`

- Per namespace: count Jobs with `status.succeeded > 0`, `status.completionTime` older than 7d,
  and `spec.ttlSecondsAfterFinished == nil`. Fire when count `>= 1`. `Severity: info`.
- One finding per namespace, `Object = ObjectRef{Kind:"Namespace", Name:<ns>}`.
- Evidence: `count`, `oldestCompletionDays`.
- Remediation: `"Set spec.ttlSecondsAfterFinished on Jobs, or prune; retained pods hold node resources"`.

### `cost/namespace-spend-trend`

- **measured only** (`NamespaceCost.PriorMonthlyCost >= 0`). Fire when
  `MonthlyCost > PriorMonthlyCost * 1.25` **and** `MonthlyCost - PriorMonthlyCost >= 50`.
- `Severity` = `warning` if `(MonthlyCost - PriorMonthlyCost) >= 200`, else `info`.
- `Object = ObjectRef{Kind:"Namespace", Name:<ns>}`. Evidence: `monthlyCost`,
  `priorMonthlyCost`, `deltaPct`, `deltaAbs`, `basis`.
- Silently absent on the estimate path.

### `cost/over-replicated`

- Per workload: `Kind ∈ {Deployment, StatefulSet}`, `Replicas >= 4`, no HPA targeting it,
  `CPUUsageCores >= 0`, per-pod CPU usage `< 0.2 *` per-pod CPU request, `MonthlyCost >= 10`.
- `suggestedReplicas = max(ceil(CPUUsageCores / (perPodRequestCores * 0.6)), 2)`.
- `estimatedMonthlySaving = MonthlyCost * (1 - suggestedReplicas/Replicas)`, clamped `>= 0`.
- `Severity` = `warning` if `estimatedMonthlySaving >= 20`, else `info`.
- Evidence: `replicas`, `suggestedReplicas`, `perPodCpuUtil`, `estimatedMonthlySaving`, `basis`.
- Remediation: `"Reduce replicas to <N> or add an HPA; saves ~$<Z>/mo"`.

## Report additions

### `report.json`

`meta.cost` (omitted when `snap.Cost` is nil):

```json
"cost": {
  "basis": "estimated",
  "currency": "USD",
  "window": "7d",
  "monthlyTotal": 4213.55,
  "estimatedMonthlyWaste": 921.40,
  "note": "estimated from resource requests; usage from promql"
}
```

`cost` findings appear in `findings[]` with `domain: "cost"` like every other domain.

### `report.md`

A `## Cost` section, rendered only when `meta.cost` is present, placed after `## Summary`
(if any) and before `## Findings`:

```
## Cost

Estimated monthly spend: $4,213.55 · estimated waste: $921.40 (22%) · window: 7d

> 💰 Cost figures are estimated (no OpenCost detected) — directional only.

| Finding | Object | ~$/mo | Action |
|---|---|---|---|
| cost/over-replicated | Deployment/web/frontend | 180.00 | Reduce replicas to 3 or add an HPA |
| cost/rightsizing     | Deployment/api/worker   | 96.40  | Set requests cpu=250m mem=380Mi |
| cost/idle            | Deployment/demo/old-job | 62.00  | Scale to zero or delete |
| cost/orphaned-lb     | Service/legacy/edge     | 18.00  | Delete the Service |
| cost/orphaned-pvc    | PersistentVolumeClaim/data/scratch | 12.00 | Delete the unused PVC |
```

- The spend/waste line always renders; the `>` banner only when `basis != "measured"`.
- Top-5 rows = the five `cost/*` findings with the highest `estimatedMonthlySaving` /
  `estimatedMonthlyCost` evidence value, ordered `(value desc, ruleID asc, object asc)`.
- `{{if .Cost}}` guard; `golden_basic.md` (no cost data) stays byte-identical.

## Configuration

`connectors.opencost` (already parsed since M1 as `ConnectorSpec{URL, Mode, EstimateFallback}`):

```yaml
connectors:
  opencost:
    url: auto              # auto | disabled | http(s)://…
    estimateFallback: true # when false and OpenCost is absent, emit cost/skipped instead of estimating
    rates:                 # optional flat override for the estimator's default rate
      cpuHour: 0.031
      memGiBHour: 0.004
```

- `ConnectorSpec` gains `Rates *config.Rate` (`{CPUHour, MemGiBHour float64}`), `json:"rates,omitempty"`.
- `Validate`: `connectors.opencost.url` gets the same switch M3 added for `promql.url`
  (`auto` | `disabled` | `http://…` | `https://…`, else error). If `rates` is set, both
  `cpuHour > 0` and `memGiBHour > 0`, else
  `"connectors.opencost.rates.{cpuHour,memGiBHour} must be positive"`.
- `applyDefaults`: replace the muddled M1 `EstimateFallback` block with a clear rule — when the
  `opencost` block is omitted entirely, `Default()` supplies `{URL:"auto", EstimateFallback:true}`;
  a user-written block takes the user's values verbatim, and `EstimateFallback` defaults to
  `true` only when the key is absent from an otherwise-present block (documented in
  `poirot.example.yaml`).
- `poirot.example.yaml` + `internal/config/testdata/full.yaml` updated.

## Determinism

- **Byte-stable core** (`report.json` minus `meta.cost`, `report.md` minus `## Cost`): cost
  findings' `RuleID`, `Domain`, `Severity`, `Object`, `Title`, evidence *keys*, and the
  `Build` sort order. `cost.Collect` and `estimate.go` sort every map-derived slice before it
  can reach a finding.
- **Non-deterministic, demarcated**: dollar *values* in cost evidence and the whole
  `report.Meta.Cost` block — identical treatment to `slo` metric values and `report.Meta.LLM`.
- `TestBuildOutputIsByteStable` / `TestReportSortingIgnoresAnalysis` build from fixed findings
  and are unaffected.
- New `TestCostCollectDeterministic`: a frozen OpenCost fixture + a frozen snapshot → the
  `CostSet` (dollars included, because the fixture is frozen) is byte-identical across 50 runs.
- `golden_basic.md` must come out byte-identical (no cost data in that fixture) — regenerate and
  diff as part of the report task, exactly as M2/M3 did.

## Error handling

Best-effort throughout; the cost phase never aborts a run and never returns an error from `Run`.

| Failure | Behaviour |
|---|---|
| OpenCost query error / timeout | note in `CostSet.Note`, discard partial measured data, fall to estimate (or `nil` if `estimateFallback:false`) |
| `pricesheet.Load()` error | `cost.Collect` returns `nil` → analyzer emits `cost/skipped` with a price-sheet note |
| No nodes / no instance-type label | `Sheet.Default` for all workloads, `CostSet.Note` records it |
| promql usage sub-query error / no promql | `CPUUsageCores` / `MemUsageBytes` = `-1`; rightsizing degrades to the run-wide `cost/rightsizing-limited` info finding |
| Single malformed allocation row | skip the row, continue |
| `context` cancelled mid-collect | return whatever `CostSet` is built so far (or `nil`); never partial-panic |

## Testing

- `connector/opencost/{client,discover,parse,opencost}_test.go`: `httptest` against recorded
  `testdata/*.json` — measured happy path (`aggregate=namespace,controller` and `namespace`),
  `code != 200` body, dial failure, empty `data`, two-step 14d response. Mirrors the `promql`
  connector tests.
- `cost/estimate_test.go`: table-driven — fixture snapshots (workloads with known requests +
  node instance-types) → assert every `WorkloadCost` field against hand-computed values;
  usage-present and usage-absent variants; no-instance-type-label variant.
- `cost/collect_test.go`: fake `connector.Registry` with/without `opencost`, with/without
  `promql` → assert `Basis` / `Source` / `Note`; assert it never returns a value that makes
  `Run` error; `estimateFallback:false` → `nil`.
- `cost/pricesheet_test.go`: exact instance-type match, `Default` fallback, config `rates`
  override, malformed embed (build-tag or a parse test on a bad literal).
- `analyzer/cost/rules_test.go`: **one focused unit test per rule** — `cost/rightsizing`
  (fires, and does not fire at util `0.51`; `rightsizing-limited` when no usage),
  `cost/idle` (fires; not for DaemonSets; not when traffic present), `cost/orphaned-pvc`
  (bound + unreferenced + old; not when a pod mounts it), `cost/unbound-pvc`, `cost/orphaned-lb`
  (no ready backends; not when a ready pod matches the selector), `cost/retained-jobs`
  (per-namespace count; not when `ttlSecondsAfterFinished` set), `cost/namespace-spend-trend`
  (measured only; fires at +26%/$60; not at +10%), `cost/over-replicated` (fires; suppresses
  `cost/rightsizing` for the same workload; not when an HPA targets it). Each test uses values
  shaped as OpenCost / the estimator actually produce — explicitly per the M2 lesson (`slo` had
  no per-rule tests, which hid two Critical bugs).
- `analyzer/cost/cost_test.go`: `Requires() == ["k8s"]`; nil `snap.Cost` → single
  `cost/skipped`; `TestAnalyzeRunsAllRules` asserts each rule id is reachable.
- `orchestrator/orchestrator_test.go`: extend the e2e — fake cluster + fake `opencost` connector
  → assert a `cost/*` finding and `res.Report.Meta.Cost != nil` with `Basis:"measured"`; and
  no-`opencost` → `Basis:"estimated"`; `--no-llm` path unaffected and still byte-stable.
- `report/markdown_test.go`: `## Cost` renders with the banner when estimated, without when
  measured, top-5 ordering; `golden_basic.md` byte-identical.
- `config/config_test.go`: `opencost.url` switch (`"Auto"` → error), `rates` positivity,
  `applyDefaults` for omitted vs partial `opencost` block.

## Milestone boundary

M4 delivers: the `opencost` connector (+ requests-only estimate fallback), the `cost` analyzer
(four rule families), `report.Meta.Cost` + the `## Cost` section, and the config surface. It does
**not** deliver anything in the Non-goals list. `cost` findings are enriched by the existing M3
investigate/correlate/synthesize phases with no additional work.

Later milestones (unchanged): M5 alertmanager + argocd/flux gitops; M6 CronJob/RBAC/Dockerfile.

## Open questions

1. **Endpoints for `cost/orphaned-lb`** — this spec uses selector-match against `snap.Pods`
   (no new collection). If that proves too coarse (headless/ExternalName edge cases), a follow-up
   adds `snap.EndpointSlices`. Leaning: ship selector-match, revisit only if a test exposes a
   false positive.
2. **Rightsizing on `cpuCoreUsageAverage` vs a percentile** — OpenCost's `/allocation` exposes
   only the window average, not p95. M4 uses the average × 1.3 headroom. A percentile would need
   a promql side-query even on the measured path. Leaning: average × 1.3 for M4, note the
   limitation in the finding evidence.
3. **`730` vs `720` hours/month** — this spec uses `730` (365×24/12). Trivial, fixed here for
   consistency across estimator and `costmeta`.
