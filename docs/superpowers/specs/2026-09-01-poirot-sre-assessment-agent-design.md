# poirot — Kubernetes SRE Assessment Agent

**Status:** Design approved, pending spec review
**Date:** 2026-09-01
**Author:** Kaushal Kishor Sharma

## Summary

`poirot` is a single Go binary that produces a **point-in-time reliability, cost, and
change-risk assessment report** for a Kubernetes cluster. The user provides a small
YAML config (at minimum a kubeconfig) and runs `poirot run`. The tool discovers what
observability and platform backends are available, collects data, runs deterministic
rule-based checks to build a baseline, uses a bounded LLM tool-calling loop to
investigate the significant findings, and emits a structured `report.json` plus a
rendered `report.md`.

It is **not** an interactive incident investigator and **not** a resident in-cluster
daemon. It is an on-demand / scheduled audit: run it from a laptop with an admin
kubeconfig, or deploy it as a Kubernetes `CronJob` for recurring reports.

## Motivation

The "AI investigates a Kubernetes cluster and explains issues" space is already
crowded — HolmesGPT (CNCF, agentic, 30+ integrations, read-only + operator mode),
K8sGPT (CNCF, scan-and-explain), Aurora, Cleric, and others. Competing head-on with an
incident investigator is not worthwhile.

There are, however, clear unfilled gaps:

- **OpenCost** answers "what does this cost?" but produces no rightsizing
  recommendations and no guidance on what to do. Turning cost + usage data into
  ranked actions is left entirely to the user.
- **ArgoCD** detects structural drift but not semantic drift or operator-injected
  mutations, and offers no pre-assessment of rollout risk.
- **No existing AI SRE tool bundles reliability + cost waste + GitOps/change risk into
  a single assessment artifact.**

`poirot` occupies that gap: a broad, honest, reproducible **cluster audit report**
that spans reliability, SLO/metrics, cost waste, recent-change risk, and GitOps drift,
degrading gracefully to whatever backends the cluster actually has.

## Non-goals

- Real-time alerting or continuous watch loops.
- Writing to the cluster or opening remediation PRs (read-only, always).
- Interactive chat / free-form Q&A over the cluster.
- Being a Prometheus/Grafana/OpenCost replacement — `poirot` consumes them.
- Vendor-specific APM integrations in v1 (Datadog, New Relic, CloudWatch) — deferred.
- Distributed tracing analysis in v1 — deferred to v2.

## Key decisions (locked)

| Decision | Choice |
|---|---|
| Core purpose | Point-in-time assessment report (not incident investigator) |
| Packaging | Single Go binary: CLI (`run`, `init`, `version`) + Kubernetes CronJob mode |
| Analysis engine | Hybrid: deterministic baseline + bounded agentic deep-dive on flagged findings |
| Composition | Approach 1: monolith binary, in-process connectors + in-process agent, with interfaces designed so an MCP server is a purely additive v2 step |
| v1 connectors | Kubernetes API (mandatory) + PromQL + OpenCost (+ self-estimate fallback) + Alertmanager + native change-tracking + ArgoCD/Flux CRDs |
| LLM | Provider-agnostic interface; Anthropic/Claude default; `openai-compatible` option; `none` (`--no-llm`) fallback |
| Input | Single `poirot.yaml`; zero-arg `poirot run`; everything `auto` by default, config only overrides |
| Output | `report.json` (canonical) + `report.md` (rendered from JSON), both always |
| Language | Go |

## Architecture

### Components (one binary)

```
CLI (cmd/poirot)  — run, init, version; --cronjob flag
      │  loads config, builds run context
      ▼
Orchestrator  — 5-phase lifecycle: discover → collect → analyze → investigate → synthesize
      │
      ├── Connector Registry — probes connectors, records available|degraded|absent + reason,
      │                        exposes live connectors to Analyzers and to the agent ToolProvider
      ├── Analyzers (rule-based, no LLM) — pure functions Snapshot → []Finding
      ├── Agent (LLM tool-calling loop) — enriches high-severity findings with probable cause
      └── Reporter — renders report.json (canonical) + report.md (templated from JSON)
```

- **CLI** — thin. Parse args, load `poirot.yaml`, construct run context, invoke
  Orchestrator, write output files, set exit code.
- **Connector Registry** — probes each configured/auto-discoverable connector, records
  status, exposes the live ones. Single choke point for all backend access.
- **Analyzers** — pure functions over an immutable `Snapshot`. Each returns `[]Finding`
  with severity, evidence, and a stable `RuleID`. No LLM. Fully unit-testable against
  recorded fixtures. This is the deterministic baseline; `--no-llm` stops after this.
- **Agent** — bounded tool-calling loop. Input: the high-severity findings. Tools:
  read-only connector queries via `ToolProvider`. Output: findings enriched with
  `Analysis` (probable cause, correlated findings, confidence, remediation). Hard caps
  on iterations and tokens.
- **Reporter** — builds `report.json` from enriched findings + metadata + connector
  inventory; renders `report.md` via templates. One LLM call for the executive
  summary, constrained to reference only existing findings.
- **Orchestrator** — sequences phases, enforces per-phase budgets, handles partial
  failure: a dead connector degrades its section, never aborts the run.

### Core interfaces

These contracts are the heart of the design. Everything else is plumbing.

#### Finding — the universal currency

```go
type Severity string // "critical" | "warning" | "info"

type Finding struct {
    RuleID   string      // stable, e.g. "reliability/crashloop"
    Domain   string      // reliability | slo | cost | change | gitops
    Severity Severity
    Title    string
    Object   ObjectRef   // kind, namespace, name, apiVersion
    Evidence []Evidence  // raw facts: metric values, event messages, log lines, timestamps
    Summary  string       // one-line, templated — no LLM
    Analysis *Analysis    // filled by the Agent; nil in --no-llm mode
}

type Evidence struct {
    Source string    // "k8s" | "promql" | "opencost" | "alertmanager" | "change" | "gitops"
    Query  string    // exact query/selector used — for reproducibility
    Value  any       // JSON-serializable
    At     time.Time
}

type Analysis struct {
    ProbableCause      string
    CorrelatedFindings []string // RuleIDs
    Confidence         string   // "high" | "medium" | "low"
    Remediation        string
}
```

`report.json` is essentially `{ meta, connectors[], findings[] }`. All downstream
output (Markdown, future HTML, future MCP) renders from this structure.

#### Connector — read-only capability provider

```go
type Availability struct {
    State  string // "available" | "degraded" | "absent"
    Reason string
    Detail string
}

type Capability struct {
    ID          string          // "promql.instant", "opencost.allocation"
    Description string           // becomes the MCP tool description verbatim in v2
    ArgsSchema  json.RawMessage  // JSON Schema — validates args, becomes MCP inputSchema in v2
}

type Connector interface {
    Name() string
    Probe(ctx context.Context) Availability
    Capabilities() []Capability
    Query(ctx context.Context, capabilityID string, args json.RawMessage) (json.RawMessage, error)
}
```

Every connector query is `(capabilityID, JSON args) → JSON result`. Connectors import
only their own client libraries — never the agent, analyzers, or orchestrator.

#### Analyzer — deterministic rules

```go
type Analyzer interface {
    ID() string           // "reliability" | "slo" | "cost" | "change" | "gitops"
    Requires() []string   // connector names it needs; skipped with an info Finding if absent
    Analyze(ctx context.Context, snap *Snapshot) ([]Finding, error)
}
```

`Snapshot` is the immutable bag of everything the collect phase gathered (k8s objects,
metric series, OpenCost allocation, alerts, rollout history). Analyzers are pure: same
`Snapshot` → same `[]Finding`.

#### ToolProvider — what the agent calls

```go
type ToolSpec struct {
    Name        string
    Description string
    InputSchema json.RawMessage
}

type ToolProvider interface {
    Tools() []ToolSpec
    Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error)
}
```

v1 ships `InProcessToolProvider` wrapping the Connector Registry. v2 adds
`MCPToolProvider`. The agent only ever sees this interface.

#### LLM — provider abstraction

```go
type LLM interface {
    Complete(ctx context.Context, req Request) (Response, error) // supports tool-calling turns
}
```

`anthropic` implementation is the default; `openaicompat` covers OpenAI, Ollama, vLLM,
and Bedrock proxies. Prompts live as versioned templates in `internal/agent/prompts/`,
never inline.

### v1 → v2 migration discipline (Approach 1 → Approach 2)

To keep MCP exposure a purely additive step, v1 must observe:

1. Connectors never import the agent package.
2. The agent consumes tools only through the abstract `ToolProvider`.
3. Tool schemas are declared as data next to each connector capability, not hardcoded
   in the agent.
4. Connector request/response types are JSON-serializable from day one.

## Assessment lifecycle (data flow)

Five phases, run by the Orchestrator. Each has a time budget; a phase that overruns
yields what it has and moves on.

### 1. Discover
- Build clients from `poirot.yaml` (kubeconfig/context always; connector endpoints
  explicit or `auto`).
- `auto` discovery looks for well-known in-cluster Services: `prometheus` /
  `victoria-metrics` / `thanos-query` on common namespaces and ports; `opencost`;
  `alertmanager`; ArgoCD `Application` CRD presence; Flux CRDs.
- Each connector `Probe()`s. Registry records `available | degraded | absent` + reason.
- Output: `[]ConnectorStatus`, written into `report.json` and the top of `report.md`.

### 2. Collect
- Each available connector pulls its slice of the world for the configured lookback
  window (default 24h) and scope (all namespaces or the `scope`/`focus` filter).
  - k8s: workloads, pods, events, nodes, PDBs, HPAs, PVCs, Services.
  - promql: a fixed pack of golden-signal + saturation queries.
  - opencost: one `/allocation` call for the window.
  - alertmanager: active alerts.
  - change-tracker: rollout history; ArgoCD/Flux status.
- Everything lands in an immutable `Snapshot`. Raw responses optionally saved to
  `--dump-dir` for fixtures and debugging.
- Per-connector timeout; a slow or broken connector marks its section degraded and the
  run continues.

### 3. Analyze (deterministic)
- Run every Analyzer whose `Requires()` is satisfied. A skipped analyzer emits an
  `info` Finding: e.g. "cost analysis skipped — no OpenCost and price-sheet estimation
  disabled."
- Produces the full `[]Finding` baseline with severities and evidence. This alone is a
  usable report; `--no-llm` stops here.

### 4. Investigate (agentic, bounded)
- Select findings with `Severity >= warning`, capped at `maxFindingsInvestigated`
  (default 15).
- For each, the Agent runs a tool-calling loop via `ToolProvider`:
  - system prompt = role + rubric + hard rules (read-only, cite evidence, say
    "unknown" when unsure);
  - user turn = the finding + its evidence + the connector inventory;
  - loop: model requests a tool call → Registry executes → result returned → repeat.
  - Caps: `maxToolCallsPerFinding` (default 6), max tokens per finding, global
    wall-clock budget. On cap hit: stop, keep partial `Analysis`, set
    `confidence: low`.
- Cross-correlation pass: one LLM call over all enriched findings to link related ones
  (`CorrelatedFindings`) and collapse duplicates — e.g. "these 4 crashloops + this SLO
  burn all trace to app `payments` sync at 14:22".
- Output: findings with `Analysis` populated.

### 5. Synthesize
- Reporter builds canonical `report.json` from enriched findings + metadata +
  connector inventory.
- One LLM call: executive summary + prioritized "what to do first" list, constrained
  to reference only existing findings by `RuleID` / `Object` (guards against
  invention).
- Render `report.md` from the JSON via templates: summary, connector coverage,
  findings grouped by domain then severity (each with evidence and remediation),
  appendix of skipped checks.
- Exit code: `0` clean, `1` warnings present, `2` critical present (so CronJob/CI can
  gate on `failOn`).

### Failure posture
- Only the k8s connector is required. Missing k8s access = hard fail at Discover.
- Any phase can partially fail; the report always renders and always states what was
  degraded.
- LLM unreachable = automatic `--no-llm` fallback with a warning banner in the report.

## Analyzer catalog (v1)

### `reliability` (k8s API; richer with promql)
- CrashLoopBackOff / high restart counts (rate over lookback)
- OOMKilled containers; last-terminated reasons
- Pods Pending (unschedulable) / Evicted / ImagePullBackOff
- Readiness/liveness probe failures; containers never ready
- Node conditions: MemoryPressure, DiskPressure, PIDPressure, NotReady
- Workload risk: single replica, no resource limits, no probes, no PDB on
  multi-replica, `latest` image tag
- HPA maxed out or unable to scale
- PVC near-full (needs metrics); unbound PVCs
- Recent `Warning` events clustered by object

### `slo` (promql / VictoriaMetrics / Thanos / Mimir; degrades to `kubectl top` for saturation only)
- Golden signals per Service/workload: error rate, p95/p99 latency, throughput,
  saturation (CPU/mem vs limits)
- Multi-window burn rate on any discoverable SLO recording rules
- Targets down / stale scrape (`up == 0`, missing series)

### `cost` (opencost API → self-estimate fallback)
- Request-vs-usage gap → ranked rightsizing candidates with suggested requests
- Idle / near-idle workloads (running, ~zero traffic + ~zero CPU)
- Orphaned resources: unbound PVCs, `LoadBalancer` Services with no endpoints,
  retained completed Jobs
- Namespace spend outliers vs 7d trend
- Over-replicated deployments (low per-pod utilization × high replica count)
- Estimated monthly waste total + top-5 savings actions

Self-estimate fallback: sum pod resource requests → map nodes to instance types via
the `node.kubernetes.io/instance-type` label → apply a bundled cloud price sheet →
compute request-vs-usage gap from metrics. Results clearly labelled "estimated".

### `change` (native rollout history; always on)
- Workloads with a rollout in the lookback window + their current health
- Deployments stuck: `generation != observedGeneration`, `Progressing` stalled
- ReplicaSet churn (repeated rollouts / rollbacks)

### `gitops` (ArgoCD/Flux CRDs; whole analyzer skipped if absent)
- Apps `OutOfSync` — drift between Git and live state
- Last sync failed (`operationState` error) or app `Degraded` / `Missing`
- Auto-sync or self-heal disabled on prod apps
- Apps not synced in >90d (stale)
- Join: unhealthy workloads ↔ owning app's recent sync/revision (feeds the
  correlation pass)

Change-tracking fallback chain: ArgoCD `Application` CRDs → Flux
`Kustomization`/`HelmRelease` `.status` → Helm release history
(`sh.helm.release.v1.*` secrets) → native K8s (`generation` vs `observedGeneration`,
ReplicaSet timestamps + `deployment.kubernetes.io/revision`, container image tags +
start times, `ScalingReplicaSet` events). The native floor is always available.

Every Finding carries the exact query/selector used, so the report is reproducible and
reviewable.

## Configuration

### `poirot.yaml`

```yaml
cluster:
  kubeconfig: ~/.kube/config      # or in-cluster
  context: prod                   # optional

scope:
  namespaces: []                  # empty = all
  exclude: [kube-system, kube-node-lease]
  lookback: 24h

connectors:
  promql:       { url: auto }     # auto | http://... | disabled
  opencost:     { url: auto, estimateFallback: true }
  alertmanager: { url: auto }
  gitops:       { mode: auto }    # auto | argocd | flux | disabled

focus: []                         # e.g. [cost] or [reliability, slo] — empty = all analyzers

llm:
  provider: anthropic             # anthropic | openai-compatible | none
  model: claude-sonnet-5
  # API key via POIROT_LLM_API_KEY env
  maxToolCallsPerFinding: 6
  maxFindingsInvestigated: 15

output:
  dir: ./poirot-out               # writes report.json + report.md
  failOn: critical                # none | warning | critical -> exit code
```

Zero-arg `poirot run` reads `./poirot.yaml`. Everything under `connectors` defaults to
`auto`, so the minimal viable config is just `cluster:`.

## Repo layout

```
poirot/
  cmd/poirot/            # CLI entrypoint: run, init, version
  internal/
    config/              # poirot.yaml load + validate + defaults
    orchestrator/        # the 5-phase lifecycle
    connector/
      registry.go        # Connector iface, ToolProvider, probe/inventory
      k8s/               # client-go wrapper, collectors
      promql/            # PromQL HTTP client (Prom / VM / Thanos / Mimir)
      opencost/          # allocation pull + self-estimate fallback + price sheet
      alertmanager/
      change/            # native rollout history
      gitops/            # ArgoCD + Flux CRD readers
    analyzer/
      reliability/  slo/  cost/  change/  gitops/
      finding.go         # Finding, Evidence, Severity, Snapshot
    agent/
      loop.go            # bounded tool-calling investigation
      correlate.go       # cross-finding correlation pass
      prompts/           # versioned prompt templates
    llm/
      llm.go  anthropic/  openaicompat/
    report/
      json.go  markdown.go  templates/
  testdata/              # recorded cluster/connector fixtures
  deploy/                # CronJob + read-only RBAC manifests; Helm later
  docs/
  Makefile  Dockerfile  README.md
```

## Testing strategy

- **Analyzers — the test core.** Recorded `Snapshot` fixtures (real cluster dumps,
  scrubbed) → golden `[]Finding` JSON. Every rule gets a positive and a negative
  fixture. Fast, deterministic, no cluster.
- **Connectors.** `Query` against `httptest` servers replaying recorded backend
  responses (PromQL JSON, OpenCost allocation, Alertmanager v2, ArgoCD CRD lists). k8s
  collectors tested against `envtest` or a fake clientset.
- **Agent.** `LLM` faked with a scripted transcript (predetermined tool-call
  sequence); assert the loop respects caps, cites evidence, populates `Analysis`. One
  optional live smoke test behind a build tag.
- **Reporter.** Golden `report.json` → golden `report.md`. Snapshot tests.
- **End-to-end.** `kind` cluster in CI: install a broken workload + stub
  Prometheus/OpenCost, run `poirot run --no-llm`, assert findings + exit code.
- Target: analyzers + report >85% line coverage. CI runs unit + kind e2e on every PR.

## Milestones

| # | Deliverable | Demoable outcome |
|---|---|---|
| M1 | Skeleton: CLI, config, k8s connector, `reliability` analyzer, JSON+MD reporter, `--no-llm` | `poirot run` on any cluster → real reliability report, no LLM |
| M2 | `change` analyzer + `promql` connector + `slo` analyzer | Report covers golden signals + recent-change health |
| M3 | LLM abstraction + Anthropic + bounded agent loop + correlation pass + synthesis | Full hybrid report with probable causes and prioritized actions |
| M4 | `opencost` connector (+ self-estimate) + `cost` analyzer | Cost-waste section with rightsizing + savings estimate |
| M5 | `alertmanager` + `gitops` (ArgoCD/Flux) connectors + `gitops` analyzer | Drift + firing-alert correlation in the report |
| M6 | `deploy/` CronJob + read-only RBAC + Dockerfile + `openai-compatible` LLM + README/docs | `kubectl apply` scheduled reports; portfolio write-up |

Each milestone is independently shippable and demoable — M1 alone is a useful tool.

## Deferred to v2+

- MCP server exposing connectors as reusable tools (Approach 2).
- Interactive "ask a question" investigation mode on the same connector layer.
- Loki (LogQL) structured-log connector.
- Distributed tracing (Tempo / OTLP) connector and analyzer.
- Vendor APM connectors: Datadog, New Relic, CloudWatch.
- Ingestion of existing policy/security scanner output (Trivy Operator, Polaris,
  kube-bench) instead of / alongside built-in rules.
- Self-contained HTML report output.
- Interactive first-run wizard (`poirot init` that probes and writes the YAML).
- Historical diffing between successive reports ("what changed since last week").

## Open questions for spec review

- Is the milestone ordering right, or should cost (M4) come before the agent (M3) so
  the deterministic tool has broader domain coverage sooner?
- Default lookback of 24h — right for a first report, or too short for cost trends
  (which want 7d)? Possibly per-domain lookback.
- Should `report.md` be split into `report.md` (human) + `summary.md` (Slack-sized),
  or is one file with a summary section enough for v1?
