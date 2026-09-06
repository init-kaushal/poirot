# poirot M3 — LLM Investigation Agent (design)

**Status:** Design approved in brainstorm, pending spec review
**Date:** 2026-09-05
**Author:** Kaushal Kishor Sharma
**Parent spec:** `docs/superpowers/specs/2026-09-01-poirot-sre-assessment-agent-design.md` (this refines its M3 milestone)
**Predecessors:** M1 (`2026-09-01-poirot-m1-skeleton.md`, merged), M2 (`2026-09-03-poirot-m2-metrics-change.md`, merged commit `e90cf69`)

## Summary

M3 adds the reasoning layer on top of poirot's deterministic rule findings: an `investigate`
lifecycle phase that runs a bounded LLM tool-calling loop to fill each significant finding's
`Analysis` block (probable cause, confidence, remediation), a cross-finding correlation pass, and
a final synthesis pass (executive headline + prioritised actions). The rule findings, their
evidence, the connector inventory, and the exit code stay byte-identical run-to-run; the LLM
layer is explicitly non-deterministic and clearly demarcated in `report.json`. With no API key
the phase skips cleanly and `poirot run` still produces the M2 report plus a banner.

## Locked decisions (from brainstorm)

| # | Decision |
|---|---|
| Scope | One milestone: LLM abstraction + Anthropic provider + `openai-compatible` provider + batch-by-object investigate loop + correlation pass + synthesis. |
| LLM client | Hand-rolled HTTP behind a small `LLM` interface, `httptest`-tested against recorded fixtures — same pattern as the `promql` connector. No SDK dependency. |
| Agent mandate | **Enrich existing findings only.** The agent fills `Finding.Analysis`; it cannot create, delete, reorder, or reshape findings. |
| Determinism | Deterministic core (rule findings, evidence, connector inventory, exit code, the entire `--no-llm` report) stays byte-identical. `Analysis` blocks, `Summary`, and `meta.llm` counters may drift and are marked as such. |
| No-key / unreachable | Auto-fallback: skip `investigate`/`correlate`/`synthesize`, emit the deterministic report, warning banner in `report.md`, `meta.llm.status` set. Exit code unaffected. |
| Agent tools | `k8s.logs` + `k8s.get` become real read-only capabilities on the `k8s` connector; agent also gets `snapshot.*` read tools and (when available) `promql.instant`/`promql.range`. |
| Loop shape | **Batch by owning object.** One bounded tool-calling loop per `ObjectRef` group; enriches all that object's findings at once, dedupes tool calls, yields local correlation for free. A separate final pass does cross-object correlation. |

**Type-stability note:** `analyzer.Finding`, `analyzer.Analysis` (`ProbableCause`, `CorrelatedFindings []string`, `Confidence`, `Remediation`), and `report.Summary` (`Headline`, `Actions []string`) already exist from M1 and are **not changed** by M3 — the agent only *populates* the nil `Analysis`/`Summary` fields. `snapshot.Snapshot` gains one field (`PodLogs`); `report.Meta` gains one (`LLM`). Nothing else in the existing type surface moves.

**Deviation from the parent spec's milestone table:** the parent spec placed the `openai-compatible` provider in M6. This design pulls it into M3 (it's a ~150-line second adapter behind the same `LLM` interface built here; splitting it out would mean shipping the interface without proving it against a second provider). M6 keeps CronJob/RBAC/Dockerfile.

## Non-goals (M3)

- Agent-proposed findings, remediation execution, or any write to the cluster.
- Streaming responses; multi-turn user chat; an MCP server (still a later, additive step).
- `report.json` byte-stability for the LLM-produced fields.
- Historical diffing, cost/OpenCost analysis (M4), GitOps correlation (M5).

## Component architecture

New packages; one new lifecycle phase between `analyze` and `synthesize`. Nothing existing is
restructured.

```
internal/
  llm/
    llm.go          # LLM interface; Request/Response/Message/Block/Usage/ToolSpec types
    anthropic/      # hand-rolled Messages API client (httptest-tested)
    openaicompat/   # /v1/chat/completions adapter, same interface
    prompts/        # versioned prompt templates via embed.FS
  agent/
    toolprovider.go # ToolProvider interface + InProcessToolProvider
    tools.go        # concrete tools: snapshot.findings/object/events, k8s.logs/k8s.get, promql.*
    investigate.go  # batch-by-object bounded loop -> fills Finding.Analysis
    correlate.go    # one cross-object LLM call -> Analysis.CorrelatedFindings
    synthesize.go   # one LLM call -> report.Summary
    budget.go       # iteration / token / wall-clock accounting
  connector/k8s/
    capabilities.go # NEW: k8s.get + k8s.logs read-only capabilities on the existing Connector
  snapshot/
    snapshot.go     # + PodLogs map[string][]LogChunk
  report/
    report.go       # + meta.llm block; Summary (already defined, unused) now populated
  orchestrator/
    orchestrator.go # + investigate/correlate/synthesize phases; --no-llm / auto-fallback wiring
```

### Data flow — the `investigate` phase

1. Orchestrator builds an `LLM` from config (`provider`, `model`, key from `apiKeyEnv`). `provider: none` / `--no-llm` / missing key / construct error → phase skipped, `meta.llm.status` set, banner queued.
2. Select findings `severity >= warning` (skipped-analyzer `info` findings excluded). Group by `ObjectRef.String()`. Group severity = max of members. Order: severity desc, then object string asc.
3. Keep the first `maxFindingsInvestigated` groups (default 15). Groups past the cap get an `Analysis` stub `{Confidence: "none", ProbableCause: "not investigated — budget"}`.
4. For each group: `agent.Investigate` runs one bounded loop (`LLM.Complete` <-> `ToolProvider.Invoke`), caps from `budget`. Output: each finding in the group gets `Analysis{ProbableCause, Confidence, Remediation}` (or a low-confidence stub on cap-hit / parse failure).
5. `agent.Correlate`: one `LLM.Complete` over all enriched findings -> `Analysis.CorrelatedFindings` (refs to other findings), constrained to existing findings only.
6. `agent.Synthesize`: one `LLM.Complete` -> `report.Summary{Headline, Actions}`, referencing only existing findings.
7. `report.Build` receives enriched findings + `Summary` + a `meta.llm` block.

**Byte-identical run-to-run:** sorted rule findings, `Evidence`, connector inventory, exit code, the whole `--no-llm` report.
**May drift:** every `Analysis` block, `Summary`, `meta.llm` counters.

## The `LLM` interface

```go
package llm

type Role string // "user" | "assistant"

type Block struct {
    Type     string          // "text" | "tool_use" | "tool_result"
    Text     string          // Type=="text"
    ToolName string          // Type=="tool_use"
    ToolID   string          // tool_use id / matching tool_result id
    Input    json.RawMessage // Type=="tool_use" args
    Content  string          // Type=="tool_result" payload
    IsError  bool            // Type=="tool_result"
}

type Message struct {
    Role   Role
    Blocks []Block
}

type ToolSpec struct {
    Name        string
    Description string
    InputSchema json.RawMessage // straight from connector.Capability.ArgsSchema
}

type Request struct {
    System      string
    Messages    []Message
    Tools       []ToolSpec
    MaxTokens   int
    Temperature float64 // 0 in M3
}

type Usage struct{ InputTokens, OutputTokens int }

type Response struct {
    Blocks     []Block
    StopReason string // "end_turn" | "tool_use" | "max_tokens"
    Usage      Usage
}

type LLM interface {
    Complete(ctx context.Context, req Request) (Response, error)
    Model() string
}
```

- **`anthropic`** — `POST {baseURL|https://api.anthropic.com}/v1/messages`, `anthropic-version` header, key from the configured env var. Maps `Message`/`Block` to Anthropic `messages` + `content` blocks (`text`, `tool_use`, `tool_result`); reads back `stop_reason` + `usage`. Retries `429`/`5xx`/`overloaded_error` with capped exponential backoff (3 attempts); other statuses are hard errors. ~200 lines.
- **`openaicompat`** — `POST {baseURL}/chat/completions`, tools as `function` defs, `tool_calls` <-> `tool` messages. Same interface. Covers OpenAI, Ollama, vLLM, LiteLLM.
- **Prompts** — `internal/llm/prompts/*.txt` via `embed.FS`, one per phase (`investigate_system`, `correlate`, `synthesize`), each with a leading `# v1` marker surfaced as `meta.llm.promptVersion`.
- The implementation tasks load the `claude-api` skill for exact model IDs, current `anthropic-version`, token limits, and tool-use edge cases. The interface above is stable regardless.

## `ToolProvider` and the tool surface

```go
package agent

type ToolProvider interface {
    Tools() []llm.ToolSpec
    Invoke(ctx context.Context, name string, args json.RawMessage) (result json.RawMessage, isErr bool, err error)
}
```

`InProcessToolProvider` flattens three sources into one tool list. Every tool is **read-only**,
enforced structurally: the provider only calls `connector.Query` (contract-guaranteed read-only)
or reads the immutable `Snapshot`.

| Tool | Source | Args | Returns |
|---|---|---|---|
| `snapshot.findings` | Snapshot | `{severityGte?, domain?, object?}` | rule findings (id, severity, object, summary, evidence) |
| `snapshot.object` | Snapshot | `{kind, namespace?, name}` | that object's spec+status as already collected (no live call) |
| `snapshot.events` | Snapshot | `{kind, namespace?, name}` | events for the object from the Snapshot |
| `k8s.get` | new `k8s` capability | `{apiVersion, kind, namespace?, name}` | one object fetched fresh via client-go `get` |
| `k8s.logs` | new `k8s` capability | `{namespace, pod, container?, previous?, tailLines?}` (tailLines cap 200) | pod logs via `GetLogs`; `previous:true` for the last-terminated container |
| `promql.instant` | existing `promql` capability | `{expr, time?}` | vector samples |
| `promql.range` | existing `promql` capability | `{expr, start?, end?, stepSeconds?}` | latest sample per series over a window |

`promql.*` and `k8s.*` tools are listed only when their connector probed `available`; the system
prompt states which tools exist. `snapshot.*` tools are always present.

### `k8s` connector `capabilities.go`

- `Capabilities()` stops returning `nil`; returns `k8s.get` + `k8s.logs` with real JSON-Schema args.
- `Query(ctx, "k8s.logs", args)` -> `clientset.CoreV1().Pods(ns).GetLogs(pod, &PodLogOptions{Container, Previous, TailLines})` -> `.DoRaw(ctx)`, truncated to `tailLines` (<=200) and a 256 KiB hard byte cap with a `"...truncated"` marker.
- `Query(ctx, "k8s.get", args)` -> switch on `kind` for the set poirot collects (Pod, Deployment, ReplicaSet, StatefulSet, DaemonSet, Node, Event, PodDisruptionBudget, HorizontalPodAutoscaler, Service, PersistentVolumeClaim), `get`, strip `managedFields`, return JSON. Unknown kind -> error result.
- No write verb anywhere.

### Collect-phase change

For every pod the `reliability` analyzer flags as CrashLoopBackOff / OOMKilled / ImagePull /
probe-failing, the collect phase also pulls logs (current + previous, 100 lines each) into
`Snapshot.PodLogs map[string][]LogChunk`. So `report.json` carries logs for a human even in
`--no-llm` mode, and the agent frequently needs no live `k8s.logs` call.

## The `investigate` phase in detail

### Selection & grouping
- `Severity >= warning`; group by `ObjectRef.String()`; group severity = max of members.
- Order groups: severity desc, then object string asc (deterministic).
- First `maxFindingsInvestigated` groups only; the rest get the budget stub.

### Per-group bounded loop (`agent.Investigate`)

```
system = investigate_system prompt + tool inventory + hard rules
         (read-only; cite tool output; "unknown" is valid; never invent object names)
user   = the group's findings (id, severity, summary, evidence) + pre-pulled Snapshot.PodLogs for this object
loop:
  resp = LLM.Complete(ctx, {system, messages, tools, MaxTokens: perGroupTokenCap, Temperature: 0})
  if resp.StopReason == "tool_use":
     for each tool_use block: ToolProvider.Invoke(...) -> append tool_result to messages
     continue                       # subject to caps
  else:                             # end_turn
     parse final assistant JSON: { findings: [{ruleId, probableCause, confidence, remediation}] }
     break
```

- Final assistant turn must be one JSON object, one entry per finding in the group. Parse failure -> one retry with a "return only the JSON" nudge; second failure -> low-confidence stub for the whole group + a `meta.llm.warnings` entry.
- `confidence in {high, medium, low}`; instructed to use `low` without decisive tool evidence.

### Budget (`agent/budget.go`), all config-driven with defaults

| Knob | Default | On breach |
|---|---|---|
| `maxToolCallsPerGroup` | 8 | stop looping, force a final-answer turn |
| `maxTokensPerGroup` (in+out) | 40000 | same |
| `maxFindingsInvestigated` (groups) | 15 | remaining groups get the stub |
| `globalBudget` (wall-clock) | 5m | abort investigate cleanly, keep what's done, banner |

Every `LLM.Complete` return adds to running totals; the loop checks before each iteration.

### Correlation (`agent.Correlate`) — one call
Input: all enriched findings (ruleID, object, severity, one-line summary, probableCause).
Output: per finding, `CorrelatedFindings []string` of *other* findings' `ruleID@object` refs
sharing a root cause. May only reference findings present in the input; unresolved refs are
dropped on parse.

### Synthesis (`agent.Synthesize`) — one call
Input: the enriched finding set + counts + connector inventory.
Output: `report.Summary{Headline string, Actions []string}` — Headline 1-2 sentences; Actions an
ordered "do this first" list where each item names a specific finding/object. Same
reference-resolution constraint.

### Failure posture
- LLM error mid-investigate (after retries) -> stop the phase, keep every enriched group, `meta.llm.status: "partial: <err>"`, banner. Not fatal.
- Single tool `Invoke` error -> returned to the model as a `tool_result` with `isError: true`; the agent adapts. Not a phase failure.
- Correlate / Synthesize failure -> skip that step, keep findings, note in `meta.llm.warnings`.

## Config

```yaml
llm:
  provider: anthropic            # anthropic | openai-compatible | none
  model: claude-sonnet-5
  apiKeyEnv: POIROT_LLM_API_KEY  # env var name to read the key from
  baseURL: ""                    # override for proxies / openai-compatible
  maxToolCallsPerGroup: 8
  maxTokensPerGroup: 40000
  maxFindingsInvestigated: 15
  globalBudget: 5m
```

- `--no-llm` flag on `run` forces `provider: none` regardless of config.
- `Validate`: `provider in {anthropic, openai-compatible, none}`; `globalBudget` parses as a duration > 0; `maxTokensPerGroup >= 1000`; `maxToolCallsPerGroup >= 1`; `maxFindingsInvestigated >= 1`.
- The M1/M2 keys `maxToolCallsPerFinding` / (existing) `maxFindingsInvestigated` are superseded by the names above; migrate in the config task.

## `report.json` additions

```json
"meta": {
  "... M1/M2 fields ...": "unchanged",
  "llm": {
    "status": "ok",              // ok | disabled | skipped: no api key | skipped: no baseURL | skipped: <ctor err> | partial: <reason>
                                 // "disabled" == provider:none / --no-llm (deliberate opt-out — renders no banner);
                                 // "skipped: *" == wanted LLM but couldn't reach it (renders the ⚠️ banner)
    "provider": "anthropic",
    "model": "claude-sonnet-5",
    "promptVersion": "v1",
    "inputTokens": 18420,
    "outputTokens": 3110,
    "toolCalls": 27,
    "wallClockMs": 41230,
    "warnings": []
  }
},
"summary": { "headline": "...", "actions": ["..."] },
"findings": [
  { "... unchanged ...": "",
    "analysis": {
      "probableCause": "...",
      "confidence": "medium",
      "correlatedFindings": ["change/recent-rollout@Deployment/payments/api"],
      "remediation": "..." } }
]
```

- `analysis` is `omitempty`; a `--no-llm` report is byte-identical to the M2 shape, field for field.
- `summary` is `null` unless `meta.llm.status` is `ok` or `partial`.
- Markdown: a `## Summary` section (Headline + numbered Actions) when present; an **"AI analysis skipped: &lt;reason&gt;"** banner directly under the title when `status` starts `skipped`/`partial`.

## Determinism enforcement (test split)

- **`TestBuildOutputIsByteStable`** (from M2) already builds reports with nil `Analysis`/`Summary`, so it passes unchanged — Task 14 only adds a comment noting that this *is* the `poirot run --no-llm` guarantee, and adds a case building 50× with a fixed non-nil `Analysis`/`Summary` to confirm the deterministic-core fields still serialise identically (the varying `meta.llm` counters are excluded from that comparison).
- **New `TestReportSortingIgnoresAnalysis`**: two `Report`s from identical findings but different `Analysis`/`Summary` content sort/group identically and have identical `Findings[i].{RuleID,Domain,Severity,Object,Evidence}`. The LLM layer can annotate but never reorder or reshape the deterministic core.
- `meta.llm` fields (`wallClockMs`, token counts) are expected to vary; no test claims they're stable.

## Orchestrator phase order (final)

```
1. Discover     (k8s, promql — unchanged)
2. Collect      (k8s objects + pod logs for flagged pods; promql metrics pack — unchanged shape)
3. Analyze      (reliability, change, slo — unchanged, fully deterministic)
4. Investigate  (NEW — build LLM + ToolProvider, group, bounded loop; skip cleanly if no-llm/no-key/disabled)
5. Correlate    (NEW — one call; skipped with Investigate)
6. Synthesize   (NEW — one call; skipped with Investigate -> no summary block)
7. Report       (Build unchanged; Markdown gains Summary section + skipped banner)
```

## Testing strategy

- **`agent` + orchestrator LLM paths** use a scripted `fakeLLM` returning a pre-programmed
  `Response` sequence. Tests assert *loop behaviour*, not model quality.
- **`llm/anthropic`, `llm/openaicompat`** — `httptest` replaying recorded API JSON: plain turn,
  `tool_use` turn, `max_tokens`, `429`->`200` retry, `401` hard error.
- **`agent.Investigate`** — `fakeLLM` scripted for: (a) tool call -> result -> valid JSON ->
  assert `Analysis` populated; (b) loop past `maxToolCallsPerGroup` -> forced final answer +
  `confidence` downgraded; (c) malformed JSON x2 -> low-confidence stub + `meta.llm.warnings`;
  (d) tool `Invoke` returns `isError` -> fed back, loop continues.
- **`ToolProvider`** — real `InProcessToolProvider` over a fake clientset + fake promql connector +
  hand-built Snapshot: `Tools()` lists only available connectors' capabilities; `Invoke("k8s.logs",…)`
  round-trips; unknown tool -> `isErr`.
- **`k8s` capabilities** — against `fake.Clientset`: truncation caps, `previous:true`,
  `managedFields` stripped, unknown kind -> error.
- **`agent.Correlate` / `agent.Synthesize`** — `fakeLLM` returns refs to one real + one bogus
  finding -> bogus dropped, real kept.
- **`orchestrator`** — fake k8s + fake promql + `fakeLLM`: (a) full run -> `analysis` blocks +
  `summary` + `status: ok`; (b) no key -> phase skipped, `status: "skipped: no api key"`,
  deterministic report, banner; (c) `fakeLLM` errors mid-investigate -> `status: "partial: …"`,
  partial enrichment, exit code unaffected.
- **Determinism** — the split above.
- **One optional live smoke test** behind `//go:build llmlive`.

## Task shape (~15 TDD tasks, one milestone)

1. `llm` interface + types + prompt `embed.FS` scaffold
2. `llm/anthropic` provider (Messages API, retry, httptest)
3. `llm/openaicompat` provider (chat/completions, httptest)
4. `k8s` capabilities: `k8s.get` + `k8s.logs` (`capabilities.go`, fake-clientset tests) — the `GetLogs` helper lands here first
5. `Snapshot.PodLogs` type + `k8s` collect-phase log pull for flagged pods (reuses the Task 4 `GetLogs` helper)
6. `agent.ToolProvider` + `InProcessToolProvider` (snapshot-read tools + connector bridge)
7. `agent.budget` (caps accounting)
8. `agent.Investigate` — group selection + bounded loop + JSON-answer parsing
9. `agent.Correlate` — one call, ref-validation
10. `agent.Synthesize` — one call -> `report.Summary`, ref-validation
11. `report` — `meta.llm` block, `Summary` wiring, Markdown Summary section + skipped-banner
12. config — `llm` block extension + validation + `--no-llm` flag
13. orchestrator — `investigate`/`correlate`/`synthesize` phases + auto-fallback wiring
14. determinism test split + `TestReportSortingIgnoresAnalysis`
15. docs (`poirot.example.yaml` `llm` block, README M3 status) + fold in cheap M2 carry-forwards:
    config validation of `promql.url`; the unreachable `disabled` branch in `promql.Probe`
    (delete the orchestrator guard, always register); `cpu_saturation` denominator guard
    (`/ ((container_spec_cpu_quota/container_spec_cpu_period) > 0)`); the "PromQL filter
    comparisons return the original value" comment in `metrics/pack.go`; add
    `go build -tags tools ./...` to `ci.yml`.

## Explicitly deferred past M3

- Namespace-scoping fixes for promql discovery and the metrics pack (M2 carry-forward I3/I4) —
  larger, land with M4/M5.
- `Services("").List` single-call discovery + partial-RBAC tolerance.
- `change` rule-quality (per-Deployment `ownedReplicaSets` recompute; StatefulSet recency window;
  `deploymentStuck` using `Progressing.LastUpdateTime`).
- `reliability` rule-quality carry-forwards from M1.
- MCP server exposing connectors/tools; interactive chat mode; streaming.

## Open questions for spec review

- Prompt versioning: is a `# v1` header in each prompt file enough, or does `meta.llm.promptVersion`
  want to be a content hash so any edit is visible?
- `k8s.get` kind switch vs. a dynamic-client `RESTMapper` path — the switch is simpler and covers
  everything poirot collects, but a new kind means a code change. Acceptable for M3?
- Should `--no-llm` also be the behaviour when `provider: anthropic` but `model` is unset/typo'd,
  or is that a hard config error? (Leaning hard error — model names aren't guessable.)
