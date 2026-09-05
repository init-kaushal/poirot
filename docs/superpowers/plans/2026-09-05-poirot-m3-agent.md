# poirot M3 (LLM Investigation Agent) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an `investigate` lifecycle phase — a bounded, per-object LLM tool-calling loop that fills each significant finding's `Analysis` block, a cross-finding correlation pass, and an executive synthesis pass — while the deterministic rule findings stay byte-identical run-to-run and `poirot run` still works with no API key.

**Architecture:** A hand-rolled `llm.LLM` interface (Anthropic + OpenAI-compatible providers, `httptest`-tested like the `promql` client). An `agent` package: `ToolProvider` bridges connector `Capabilities()` + Snapshot reads to LLM tools; `Investigate` groups `severity>=warning` findings by owning object and runs one bounded loop per group; `Correlate` and `Synthesize` are one LLM call each. The orchestrator gets phases 4–6 (investigate/correlate/synthesize), all skipped cleanly when the LLM is unavailable. `report.json` gains a `meta.llm` block demarcating the non-deterministic layer.

**Tech Stack:** Go 1.26, `net/http` + `net/http/httptest`, `encoding/json`, `embed` (prompt templates), `k8s.io/client-go` (`Pods().GetLogs`, typed `get`), `github.com/stretchr/testify`. No new module dependencies.

**Spec:** `docs/superpowers/specs/2026-09-05-poirot-m3-agent-design.md` (read it — this plan implements its Sections 1–6). Parent spec: `docs/superpowers/specs/2026-09-01-poirot-sre-assessment-agent-design.md`.

**Predecessors:** M1 (`plans/2026-09-01-poirot-m1-skeleton.md`) and M2 (`plans/2026-09-03-poirot-m2-metrics-change.md`), both merged to `main` (M2 at commit `e90cf69`). This plan builds on the code as it stands on `main` today — read `internal/analyzer/finding.go`, `internal/report/report.go`, `internal/orchestrator/orchestrator.go`, `internal/connector/registry.go`, `internal/connector/k8s/k8s.go`, `internal/connector/promql/promql.go`, `internal/config/config.go` first.

## Global Constraints

- Module path `github.com/init-kaushal/poirot`. `go.mod` `go` directive stays `1.26.0`; `.github/workflows/ci.yml` `go-version` stays `'1.26'` (M1 ruling).
- **Read-only.** No code path creates/updates/patches/deletes/evicts any cluster resource, and nothing writes to an LLM-adjacent service beyond the model API call itself. The only new k8s verbs are `get` and `GetLogs` (both reads).
- **`analyzer.Finding`, `analyzer.Analysis`, `report.Summary` are NOT modified** — the agent only populates their nil fields. `analyzer.Analysis` is exactly `{ProbableCause string; CorrelatedFindings []string; Confidence string; Remediation string}` (json tags already present). `report.Summary` is `{Headline string; Actions []string}`. New fields: `snapshot.Snapshot.PodLogs`, `report.Meta.LLM`.
- **Determinism:** with every `Finding.Analysis == nil` and `Report.Summary == nil` (i.e. `poirot run --no-llm`), `report.json` and `report.md` are byte-identical across runs. The LLM layer may only *annotate* — never reorder, drop, or reshape findings. `meta.llm` token/timing counters are expected to vary and are excluded from every stability assertion.
- **Temperature 0** on every `LLM.Complete` call in M3.
- Every `agent`/`orchestrator` test that exercises an LLM path uses a **scripted `fakeLLM`** returning a pre-programmed `[]llm.Response`. No test makes a live API call. Provider packages (`llm/anthropic`, `llm/openaicompat`) are tested with `httptest` replaying recorded JSON.
- Prompt files live in `internal/llm/prompts/*.txt`, loaded via `embed.FS`. Each starts with a `# vN` comment line; `meta.llm.promptVersion` reports `vN+<first 8 hex of sha256(file bytes)>` so any edit is visible.
- All tests: `go test ./... -race -count=1` green; `go vet ./...` + `gofmt -l .` clean; `go build ./...` and `go build -tags tools ./...` clean — before every commit.
- Commit messages: `<type>: <summary>` (`feat`/`fix`/`test`/`chore`/`refactor`/`docs`). Body ends with exactly:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

---

## File Structure

| Path | Responsibility | New/Mod |
|---|---|---|
| `internal/llm/llm.go` | `LLM` interface; `Role`/`Block`/`Message`/`ToolSpec`/`Request`/`Usage`/`Response` types | New |
| `internal/llm/prompts/prompts.go` | `embed.FS` + `Load(name) (text, version string)` | New |
| `internal/llm/prompts/{investigate_system,correlate,synthesize}.txt` | the three prompts | New |
| `internal/llm/anthropic/anthropic.go` | Messages API client implementing `llm.LLM` | New |
| `internal/llm/openaicompat/openaicompat.go` | `/chat/completions` client implementing `llm.LLM` | New |
| `internal/connector/k8s/capabilities.go` | `k8s.get` + `k8s.logs` capabilities; `podLogs` helper | New |
| `internal/connector/k8s/k8s.go` | drop the stub `Capabilities()`/`Query()` bodies | Mod |
| `internal/connector/k8s/collect.go` | pull logs for flagged pods into `Snapshot.PodLogs` | Mod |
| `internal/snapshot/snapshot.go` | `LogChunk` type + `PodLogs map[string][]LogChunk` field | Mod |
| `internal/agent/toolprovider.go` | `ToolProvider` interface | New |
| `internal/agent/tools.go` | `InProcessToolProvider` (snapshot reads + connector bridge) | New |
| `internal/agent/budget.go` | per-group tool-call / token accounting | New |
| `internal/agent/investigate.go` | group + bounded loop → `Analysis` | New |
| `internal/agent/correlate.go` | one call → `Analysis.CorrelatedFindings` | New |
| `internal/agent/synthesize.go` | one call → headline + actions | New |
| `internal/report/report.go` | `LLMMeta` type + `Meta.LLM` field | Mod |
| `internal/report/markdown.go` | `## Summary` section + skipped/partial banner | Mod |
| `internal/config/config.go` | `LLM` block v2 + validation | Mod |
| `internal/orchestrator/orchestrator.go` | `Options.LLM` seam + phases 4–6 + auto-fallback | Mod |
| `cmd/poirot/run.go` | `--no-llm` flag; status in the stdout line | Mod |
| `internal/report/determinism_test.go` | non-nil-Analysis stability case | Mod |
| `poirot.example.yaml`, `README.md` | `llm` block, M3 status | Mod |
| `metrics/pack.go`, `config.go`, `orchestrator.go`, `ci.yml` | folded M2 carry-forwards (Task 15) | Mod |

---

## Task 1: `llm` interface + types + prompt scaffold

**Files:**
- Create: `internal/llm/llm.go`, `internal/llm/prompts/prompts.go`, `internal/llm/prompts/investigate_system.txt`, `internal/llm/prompts/correlate.txt`, `internal/llm/prompts/synthesize.txt`
- Test: `internal/llm/prompts/prompts_test.go`

**Interfaces:**
- Produces (package `llm`):
  - `type Role string` (`RoleUser="user"`, `RoleAssistant="assistant"`)
  - `type Block struct { Type string; Text string; ToolName string; ToolID string; Input json.RawMessage; Content string; IsError bool }` — `Type` ∈ `"text"|"tool_use"|"tool_result"`
  - `type Message struct { Role Role; Blocks []Block }`
  - `type ToolSpec struct { Name, Description string; InputSchema json.RawMessage }`
  - `type Request struct { System string; Messages []Message; Tools []ToolSpec; MaxTokens int; Temperature float64 }`
  - `type Usage struct { InputTokens, OutputTokens int }`
  - `type Response struct { Blocks []Block; StopReason string; Usage Usage }` — `StopReason` ∈ `"end_turn"|"tool_use"|"max_tokens"`
  - `type LLM interface { Complete(ctx context.Context, req Request) (Response, error); Model() string }`
- Produces (package `prompts`): `func Load(name string) (text string, version string, err error)` — reads `<name>.txt` from the `embed.FS`, requires a first line matching `^# v\d+$`, returns the body (first line stripped) and `version = "<vN>+<sha256(fileBytes)[:8] hex>"`.

- [ ] **Step 1: Write the failing test**

`internal/llm/prompts/prompts_test.go`:

```go
package prompts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadReturnsBodyAndVersion(t *testing.T) {
	body, ver, err := Load("investigate_system")
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.False(t, strings.HasPrefix(body, "# v"), "header line must be stripped from the body")
	require.Regexp(t, `^v\d+\+[0-9a-f]{8}$`, ver)
}

func TestLoadUnknownName(t *testing.T) {
	_, _, err := Load("nope")
	require.Error(t, err)
}

func TestLoadAllThreePromptsPresent(t *testing.T) {
	for _, n := range []string{"investigate_system", "correlate", "synthesize"} {
		_, _, err := Load(n)
		require.NoError(t, err, n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/prompts/ -v` → FAIL (`undefined: Load`).

- [ ] **Step 3: Write `llm.go`**

```go
// Package llm is a minimal provider-neutral chat-with-tools interface.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Block struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"toolName,omitempty"`
	ToolID   string          `json:"toolId,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Content  string          `json:"content,omitempty"`
	IsError  bool            `json:"isError,omitempty"`
}

type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Temperature float64
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Blocks     []Block
	StopReason string
	Usage      Usage
}

// LLM is a single non-streaming completion with optional tool use.
type LLM interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Model() string
}
```

- [ ] **Step 4: Write `prompts.go` and the three `.txt` files**

`internal/llm/prompts/prompts.go`:

```go
package prompts

import (
	"bufio"
	"crypto/sha256"
	"embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed *.txt
var files embed.FS

var headerRe = regexp.MustCompile(`^# (v\d+)$`)

func Load(name string) (text string, version string, err error) {
	raw, err := files.ReadFile(name + ".txt")
	if err != nil {
		return "", "", fmt.Errorf("prompts: %w", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	if !sc.Scan() {
		return "", "", fmt.Errorf("prompts: %s is empty", name)
	}
	m := headerRe.FindStringSubmatch(strings.TrimSpace(sc.Text()))
	if m == nil {
		return "", "", fmt.Errorf("prompts: %s first line must be '# vN'", name)
	}
	sum := sha256.Sum256(raw)
	body := strings.TrimPrefix(string(raw), sc.Text())
	body = strings.TrimPrefix(body, "\n")
	return body, fmt.Sprintf("%s+%x", m[1], sum[:4]), nil
}
```

`internal/llm/prompts/investigate_system.txt` (write real content — this is the agent's brief):

```
# v1
You are an SRE investigating one Kubernetes object that has one or more flagged problems.
You have read-only tools. Use them to find the PROBABLE CAUSE of each flagged finding.

Rules:
- Only state a cause you can back with tool output. If you cannot, say so and set confidence "low".
- Never invent object names, namespaces, or metric values. Quote what the tools returned.
- Prefer the pre-supplied logs and snapshot data; call live tools only when they add something.
- Stop as soon as you can answer. You have a hard tool-call budget.

When done, reply with ONE json object and nothing else:
{"findings":[{"ruleId":"<id>","probableCause":"<one or two sentences>","confidence":"high|medium|low","remediation":"<concrete next step>"}]}
Include exactly one entry per flagged finding you were given.
```

`internal/llm/prompts/correlate.txt`:

```
# v1
You are given a list of findings from one cluster assessment, each already analysed.
Identify which findings share a single root cause.

Rules:
- Only reference findings present in the input, by their "ruleId@object" key.
- A finding correlates with another only if the same underlying event explains both.
- Omit findings that stand alone.

Reply with ONE json object and nothing else:
{"correlations":[{"key":"<ruleId@object>","related":["<ruleId@object>", ...]}]}
```

`internal/llm/prompts/synthesize.txt`:

```
# v1
You are writing the executive summary of a Kubernetes cluster assessment for an on-call engineer.
Input: the full finding set (with analysis) and the connector coverage.

Rules:
- Headline: 1-2 sentences on the single most important thing.
- Actions: an ordered list, most urgent first, each naming a specific finding or object.
- Do not invent findings. Every action must trace to an input finding.

Reply with ONE json object and nothing else:
{"headline":"<text>","actions":["<action>", ...]}
```

- [ ] **Step 5: Run tests / build / commit**

Run: `go test ./internal/llm/... -race -v && go build ./...`
Commit: `feat: llm interface + embedded prompt templates`

---

## Task 2: `llm/anthropic` provider

> **Dispatch note for the implementer:** load the `claude-api` skill before writing `anthropic.go` — it has the current `anthropic-version` header value, exact model IDs, the `/v1/messages` request/response shape, tool-use block format, and token limits. The `llm.LLM` interface from Task 1 is stable regardless of those details.

**Files:**
- Create: `internal/llm/anthropic/anthropic.go`
- Create: `internal/llm/anthropic/testdata/{text_turn,tool_use_turn,max_tokens,rate_limited,auth_error}.json`
- Test: `internal/llm/anthropic/anthropic_test.go`

**Interfaces:**
- Produces (package `anthropic`):
  - `type Options struct { APIKey, Model, BaseURL string; HTTPClient *http.Client }`
  - `func New(o Options) (*Client, error)` — err if `APIKey` or `Model` empty; `BaseURL` defaults to `https://api.anthropic.com`; `HTTPClient` defaults to `&http.Client{Timeout: 120 * time.Second}`.
  - `func (c *Client) Model() string`
  - `func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error)` — maps `req` to a `/v1/messages` POST body:
    - `system` ← `req.System`; `max_tokens` ← `req.MaxTokens` (default 4096 if 0); `temperature` ← `req.Temperature`; `model` ← `c.model`.
    - `messages`: each `llm.Message` → `{role, content:[...]}`. Block mapping: `text` → `{type:"text",text}`; `tool_use` → `{type:"tool_use",id:ToolID,name:ToolName,input:<Input>}`; `tool_result` → `{type:"tool_result",tool_use_id:ToolID,content:Content,is_error:IsError}`.
    - `tools`: each `llm.ToolSpec` → `{name,description,input_schema:<InputSchema>}`.
    - Headers: `x-api-key`, `anthropic-version: <value from claude-api skill>`, `content-type: application/json`.
    - Response: parse `content[]` → `llm.Response.Blocks` (`text` and `tool_use` block types); `stop_reason` → `llm.Response.StopReason` (`"end_turn"|"tool_use"|"max_tokens"`); `usage.input_tokens`/`output_tokens` → `llm.Response.Usage`.
    - Retry: on HTTP 429 or 5xx or a body `type:"error"` with `error.type == "overloaded_error"`, retry up to 3 total attempts with backoff `min(2^n * 500ms, 8s)` respecting `ctx`. Non-retryable status → `error` including status + a ≤512-byte body snippet.

- [ ] **Step 1: Record realistic fixtures** — copy real `/v1/messages` responses (the `claude-api` skill or the Anthropic docs have canonical examples) into the five `testdata` files: a plain text `end_turn`; a `tool_use` stop with one `tool_use` block; a `max_tokens` stop; a `429` error body; a `401` error body.

- [ ] **Step 2: Write the failing test**

`internal/llm/anthropic/anthropic_test.go`:

```go
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/llm"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err)
	return b
}

func TestCompleteTextTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "k", r.Header.Get("x-api-key"))
		require.NotEmpty(t, r.Header.Get("anthropic-version"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, float64(0), body["temperature"])
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, err := New(Options{APIKey: "k", Model: "claude-sonnet-5", BaseURL: srv.URL})
	require.NoError(t, err)
	resp, err := c.Complete(context.Background(), llm.Request{
		System:   "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: "hi"}}}},
	})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
	require.NotEmpty(t, resp.Blocks)
	require.Equal(t, "text", resp.Blocks[0].Type)
	require.Positive(t, resp.Usage.OutputTokens)
}

func TestCompleteToolUseTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "tool_use_turn"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "tool_use", resp.StopReason)
	var tu *llm.Block
	for i := range resp.Blocks {
		if resp.Blocks[i].Type == "tool_use" {
			tu = &resp.Blocks[i]
		}
	}
	require.NotNil(t, tu)
	require.NotEmpty(t, tu.ToolName)
	require.NotEmpty(t, tu.ToolID)
	require.NotEmpty(t, tu.Input)
}

func TestCompleteRetriesOn429ThenSucceeds(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(429)
			w.Write(fixture(t, "rate_limited"))
			return
		}
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
	require.Equal(t, int32(2), atomic.LoadInt32(&n))
}

func TestCompleteAuthErrorIsHard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		w.Write(fixture(t, "auth_error"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), llm.Request{})
	require.ErrorContains(t, err, "401")
}

func TestNewRejectsEmpty(t *testing.T) {
	_, err := New(Options{Model: "m"})
	require.Error(t, err)
	_, err = New(Options{APIKey: "k"})
	require.Error(t, err)
}
```

- [ ] **Step 3: Write `anthropic.go`** — implement per the Interfaces block. Keep request/response structs private to the package. `Model()` returns `c.model`.

- [ ] **Step 4: Run / build / commit**

Run: `go test ./internal/llm/anthropic/ -race -v && go build ./...`
Commit: `feat: llm/anthropic — Messages API provider with retry`

---

## Task 3: `llm/openaicompat` provider

**Files:**
- Create: `internal/llm/openaicompat/openaicompat.go`
- Create: `internal/llm/openaicompat/testdata/{text_turn,tool_call_turn,length_stop,server_error}.json`
- Test: `internal/llm/openaicompat/openaicompat_test.go`

**Interfaces:**
- Produces (package `openaicompat`):
  - `type Options struct { APIKey, Model, BaseURL string; HTTPClient *http.Client }` — `BaseURL` **required** (no default; err if empty), `APIKey` optional (local servers), `Model` required.
  - `func New(o Options) (*Client, error)`; `func (c *Client) Model() string`; `func (c *Client) Complete(ctx, req llm.Request) (llm.Response, error)`.
  - Maps to `POST {BaseURL}/chat/completions`: `req.System` → a leading `{role:"system"}` message; `llm.Message` text blocks → `{role,content}`; `tool_use` blocks → assistant `tool_calls:[{id,type:"function",function:{name,arguments:<Input as string>}}]`; `tool_result` blocks → `{role:"tool",tool_call_id,content}`. `req.Tools` → `tools:[{type:"function",function:{name,description,parameters:<InputSchema>}}]`. `temperature`, `max_tokens` passed through.
  - Response: `choices[0].message.content` → a `text` block; `choices[0].message.tool_calls` → `tool_use` blocks (`Input` = `json.RawMessage(arguments)`); `choices[0].finish_reason` mapped `"stop"→"end_turn"`, `"tool_calls"→"tool_use"`, `"length"→"max_tokens"`; `usage.prompt_tokens`/`completion_tokens` → `llm.Usage`.
  - Retry 429/5xx same shape as Task 2.

- [ ] **Step 1–4:** mirror Task 2 — fixtures, a test file asserting the four scenarios (text, tool_call, length, 5xx retry→success), implement, run, commit `feat: llm/openaicompat — chat/completions provider`.

---

## Task 4: `k8s` capabilities — `k8s.get` + `k8s.logs`

**Files:**
- Create: `internal/connector/k8s/capabilities.go`
- Modify: `internal/connector/k8s/k8s.go` — delete the two stub method bodies (`Capabilities` returning nil, `Query` returning the "no queryable capabilities" error); the real ones now live in `capabilities.go`.
- Test: `internal/connector/k8s/capabilities_test.go`

**Interfaces:**
- Produces (package `k8s`):
  - `func (c *Connector) Capabilities() []connector.Capability` — returns two, fixed order:
    - `k8s.get`: description `"Fetch one Kubernetes object (spec + status) by kind/namespace/name."`, `ArgsSchema` requires `kind` + `name`, optional `namespace`, `additionalProperties:false`.
    - `k8s.logs`: description `"Read recent log lines from a pod container (optionally the previously-terminated container)."`, `ArgsSchema` requires `namespace` + `pod`, optional `container`/`previous`(bool)/`tailLines`(int, default 100, max 200), `additionalProperties:false`.
  - `func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error)`:
    - `"k8s.logs"` → unmarshal args; clamp `tailLines` to `[1,200]` default 100; `c.cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{Container, Previous, TailLines: *int64}).DoRaw(ctx)`; truncate the returned bytes to the last `tailLines` lines AND a 256 KiB cap, prefixing `"…(truncated)\n"` when either fired; return `json.Marshal(map[string]any{"pod": pod, "container": container, "previous": previous, "lines": string(text)})`.
    - `"k8s.get"` → unmarshal; `switch strings.ToLower(kind)` over the collected set (`pod`,`deployment`,`replicaset`,`statefulset`,`daemonset`,`node`,`event`,`poddisruptionbudget`/`pdb`,`horizontalpodautoscaler`/`hpa`,`service`/`svc`,`persistentvolumeclaim`/`pvc`); each case does the typed `Get(ctx, name, metav1.GetOptions{})`; set `obj.ManagedFields = nil`; `return json.Marshal(obj)`. Unknown kind → `nil, fmt.Errorf("k8s.get: unsupported kind %q", kind)`.
    - unknown `id` → `nil, fmt.Errorf("k8s: unknown capability %q", id)`.
  - `func (c *Connector) podLogs(ctx context.Context, ns, pod, container string, previous bool, tail int) (string, error)` — the shared helper the `k8s.logs` case and Task 5's collector both use. Returns the truncated log text.
- Add `var _ connector.Connector = (*Connector)(nil)` if not already present.

- [ ] **Step 1: Write the failing test**

`internal/connector/k8s/capabilities_test.go`:

```go
package k8s

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestCapabilitiesListsGetAndLogs(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	caps := c.Capabilities()
	require.Len(t, caps, 2)
	require.Equal(t, "k8s.get", caps[0].ID)
	require.Equal(t, "k8s.logs", caps[1].ID)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(caps[1].ArgsSchema, &schema))
	require.Equal(t, false, schema["additionalProperties"])
}

func TestQueryK8sGetStripsManagedFields(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-1", Namespace: "p",
			ManagedFields: []metav1.ManagedFieldsEntry{{Manager: "x"}},
		},
	})
	c := NewWithClient(cs, "ctx", Scope{})
	raw, err := c.Query(context.Background(), "k8s.get",
		json.RawMessage(`{"kind":"Pod","namespace":"p","name":"api-1"}`))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "managedFields")
	require.Contains(t, string(raw), "api-1")
}

func TestQueryK8sGetUnknownKind(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	_, err := c.Query(context.Background(), "k8s.get", json.RawMessage(`{"kind":"Secret","name":"s"}`))
	require.ErrorContains(t, err, "unsupported kind")
}

func TestQueryK8sLogsTruncatesAndTails(t *testing.T) {
	cs := fake.NewSimpleClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "n"}})
	// fake GetLogs returns the string "fake logs" by default; override via reactor for a big body.
	big := strings.Repeat("line\n", 500)
	cs.PrependReactor("get", "pods/log", func(k8stesting.Action) (bool, interface{}, error) {
		return true, nil, nil // fall through — see note
	})
	_ = big
	c := NewWithClient(cs, "ctx", Scope{})
	raw, err := c.Query(context.Background(), "k8s.logs",
		json.RawMessage(`{"namespace":"n","pod":"p","tailLines":10}`))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(raw, &out))
	require.Equal(t, "p", out["pod"])
	require.Contains(t, out, "lines")
}

func TestQueryUnknownCapability(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "ctx", Scope{})
	_, err := c.Query(context.Background(), "k8s.bogus", nil)
	require.ErrorContains(t, err, "unknown capability")
}
```

> **Implementer note on the fake:** `fake.Clientset`'s `Pods(ns).GetLogs(...).DoRaw(ctx)` returns the literal bytes `"fake logs"` and no error by default — enough for `TestQueryK8sLogsTruncatesAndTails` to assert the JSON envelope shape and the `pod` field. To exercise the tail/byte truncation logic directly, unit-test the `truncateLog(text string, tail int) (string, bool)` helper you extract, with a 500-line input and `tail=10` → 10 lines + a truncation marker. Add that as `TestTruncateLog`. Do NOT try to make the fake return a large body — its log reactor doesn't support it cleanly.

- [ ] **Step 2–4:** run RED, implement `capabilities.go` (+ `truncateLog` helper + its test), delete the `k8s.go` stubs, run full suite, `go build`, commit `feat: k8s connector — k8s.get and k8s.logs read-only capabilities`.

---

## Task 5: `Snapshot.PodLogs` + collect-phase log pull

**Files:**
- Modify: `internal/snapshot/snapshot.go` — add types + field.
- Modify: `internal/connector/k8s/collect.go` — pull logs for flagged pods after the pod list.
- Test: `internal/connector/k8s/collect_test.go` (add cases), `internal/snapshot/snapshot_test.go` (field presence).

**Interfaces:**
- Produces (package `snapshot`):
  - `type LogChunk struct { Container string; Previous bool; Lines string }` — json tags `container`,`previous`,`lines`.
  - `Snapshot` gains `PodLogs map[string][]LogChunk` (json `podLogs,omitempty`), keyed by the pod's `analyzer.ObjectRef.String()` form — but `snapshot` must not import `analyzer`, so the key is built locally as `"Pod/" + namespace + "/" + name`. Document that.
- Modifies (package `k8s`): after `snap.Pods` is populated in `Collect`, iterate pods; a pod is **flagged** if any container status has `State.Waiting.Reason ∈ {"CrashLoopBackOff","ImagePullBackOff","ErrImagePull"}`, or `LastTerminationState.Terminated.Reason == "OOMKilled"`, or the pod has a `Ready` condition with `Status != "True"` while `Phase == "Running"`. For each flagged pod, for the first container name (and each container in `waiting`/`terminated` state), call `c.podLogs(ctx, ns, pod, container, false, 100)` and `c.podLogs(ctx, ns, pod, container, true, 100)`; append non-empty results to `snap.PodLogs[key]`. A `podLogs` error for any pod is logged-and-skipped (never fails `Collect`). Cap total pods logged at 20 (deterministic order: namespace, name).

- [ ] **Step 1: Write the failing test** (add to `collect_test.go`):

```go
func TestCollectPullsLogsForFlaggedPods(t *testing.T) {
	crash := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "p"},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:  "api",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	healthy := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "ok-1", Namespace: "p"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p"}}, crash, healthy)
	c := NewWithClient(cs, "ctx", Scope{Lookback: time.Hour})

	snap, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Contains(t, snap.PodLogs, "Pod/p/api-1")
	require.NotContains(t, snap.PodLogs, "Pod/p/ok-1")
	require.NotEmpty(t, snap.PodLogs["Pod/p/api-1"][0].Lines) // fake returns "fake logs"
}
```

- [ ] **Step 2–4:** RED, implement, full suite, `go build`, commit `feat: collect pod logs for flagged pods into Snapshot.PodLogs`.

---

## Task 6: `agent.ToolProvider` + `InProcessToolProvider`

**Files:**
- Create: `internal/agent/toolprovider.go`, `internal/agent/tools.go`
- Test: `internal/agent/tools_test.go`

**Interfaces:**
- Produces (package `agent`):
  - `type ToolProvider interface { Tools() []llm.ToolSpec; Invoke(ctx context.Context, name string, args json.RawMessage) (result json.RawMessage, isErr bool, err error) }`
    - `isErr == true` → the tool ran but returned an error the model should see (feed it back as a `tool_result` with `IsError: true`); `err == nil` in that case.
    - `err != nil` → infrastructure failure (context cancelled, marshalling bug); the caller aborts the loop.
  - `type InProcessToolProvider struct { ... }` and `func NewInProcessToolProvider(reg *connector.Registry, snap *snapshot.Snapshot) *InProcessToolProvider`.
  - `Tools()`: always includes three snapshot tools:
    - `snapshot.findings` — args `{severityGte?:string, domain?:string, object?:string}` — returns `[]{ruleId,domain,severity,object,summary,evidence}` from the Snapshot's findings *(the provider is constructed with the findings slice too — adjust the constructor to `NewInProcessToolProvider(reg, snap, findings)`)*.
    - `snapshot.object` — args `{kind,namespace?,name}` — finds the matching object in `snap` (Pods/Deployments/…) and returns its `spec`+`status` as JSON; not found → `isErr`.
    - `snapshot.events` — args `{kind,namespace?,name}` — returns `snap.Events` filtered to that `InvolvedObject`.
  - Plus, for every connector in `reg.Available()`, one `llm.ToolSpec` per `Capability` (`Name = cap.ID`, `Description = cap.Description`, `InputSchema = cap.ArgsSchema`). So `k8s.get`/`k8s.logs`/`promql.instant`/`promql.range` appear iff their connector probed available.
  - `Invoke`: `snapshot.*` handled locally; a `cap.ID` with a `.` prefix matching a connector name (`k8s.` / `promql.`) → `conn.Query(ctx, name, args)`, mapping a returned `error` to `(json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil)`; unknown tool → `(nil, true, nil)` carrying `"unknown tool: <name>"`; a `ctx.Err()` → `(nil, false, ctx.Err())`.
  - Tool list order is deterministic: the three `snapshot.*` first (fixed order), then connectors in `reg` registration order, capabilities in `Capabilities()` order.

- [ ] **Step 1: Write the failing test**

`internal/agent/tools_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// fakeConn implements connector.Connector with one capability "x.echo".
type fakeConn struct{ name string; avail connector.State }

func (f fakeConn) Name() string { return f.name }
func (f fakeConn) Probe(context.Context) connector.Availability {
	return connector.Availability{State: f.avail}
}
func (f fakeConn) Capabilities() []connector.Capability {
	return []connector.Capability{{ID: f.name + ".echo", Description: "echo",
		ArgsSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (f fakeConn) Query(_ context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	return args, nil
}

func TestToolsListsOnlyAvailableConnectors(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(fakeConn{name: "k8s", avail: connector.StateAvailable})
	reg.Register(fakeConn{name: "promql", avail: connector.StateAbsent})
	reg.Probe(context.Background())

	tp := NewInProcessToolProvider(reg, &snapshot.Snapshot{}, nil)
	names := map[string]bool{}
	for _, tspec := range tp.Tools() {
		names[tspec.Name] = true
	}
	require.True(t, names["snapshot.findings"])
	require.True(t, names["snapshot.object"])
	require.True(t, names["snapshot.events"])
	require.True(t, names["k8s.echo"])
	require.False(t, names["promql.echo"], "absent connector's caps must not be listed")
}

func TestInvokeSnapshotObject(t *testing.T) {
	snap := &snapshot.Snapshot{Pods: []corev1.Pod{{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "p"},
	}}}
	tp := NewInProcessToolProvider(connector.NewRegistry(), snap, nil)
	res, isErr, err := tp.Invoke(context.Background(), "snapshot.object",
		json.RawMessage(`{"kind":"Pod","namespace":"p","name":"api-1"}`))
	require.NoError(t, err)
	require.False(t, isErr)
	require.Contains(t, string(res), "api-1")
}

func TestInvokeConnectorQueryErrorBecomesIsErr(t *testing.T) {
	reg := connector.NewRegistry()
	reg.Register(erroringConn{}) // Name "k8s", Query returns an error
	reg.Probe(context.Background())
	tp := NewInProcessToolProvider(reg, &snapshot.Snapshot{}, nil)
	res, isErr, err := tp.Invoke(context.Background(), "k8s.get", json.RawMessage(`{}`))
	require.NoError(t, err)     // infra ok
	require.True(t, isErr)      // tool reported an error
	require.Contains(t, string(res), "tool error")
}

func TestInvokeUnknownTool(t *testing.T) {
	tp := NewInProcessToolProvider(connector.NewRegistry(), &snapshot.Snapshot{}, nil)
	_, isErr, err := tp.Invoke(context.Background(), "nope", nil)
	require.NoError(t, err)
	require.True(t, isErr)
}
```

> Implementer: write the small `erroringConn` fake (Name "k8s", available, `Query` returns `errors.New("boom")`, one `k8s.get` capability).

- [ ] **Step 2–4:** RED, implement, full suite, `go build`, commit `feat: agent ToolProvider — snapshot reads + connector capability bridge`.

---

## Task 7: `agent.budget`

**Files:**
- Create: `internal/agent/budget.go`
- Test: `internal/agent/budget_test.go`

**Interfaces:**
- Produces (package `agent`):
  - `type Budget struct { MaxToolCalls int; MaxTokens int }` (per group)
  - `func (b *Budget) Add(u llm.Usage)` — accumulates `InputTokens+OutputTokens`.
  - `func (b *Budget) ToolCall()` — increments a counter.
  - `func (b *Budget) Exceeded() (bool, string)` — true + a reason string once tool calls `>= MaxToolCalls` or accumulated tokens `>= MaxTokens`.
  - `func (b *Budget) Used() (toolCalls, tokens int)` — for meta reporting.
  - Zero-value `MaxToolCalls`/`MaxTokens` mean "no limit" for that axis (defensive; the orchestrator always sets both).

- [ ] **Step 1: failing test** — construct `Budget{MaxToolCalls: 2, MaxTokens: 100}`; `ToolCall()` once → not exceeded; twice → exceeded with a reason mentioning "tool"; separately `Add(llm.Usage{InputTokens: 60, OutputTokens: 50})` → exceeded mentioning "token". `Used()` returns the accumulated pair.
- [ ] **Step 2–4:** RED, implement (~30 lines), commit `feat: agent budget — per-group tool-call and token caps`.

---

## Task 8: `agent.Investigate`

**Files:**
- Create: `internal/agent/investigate.go`
- Test: `internal/agent/investigate_test.go`

**Interfaces:**
- Produces (package `agent`):
  - `type InvestigateConfig struct { MaxGroups int; Budget Budget; MaxTokensPerCall int }`
  - `type InvestigateMeta struct { InputTokens, OutputTokens, ToolCalls int; Warnings []string }`
  - `func Investigate(ctx context.Context, l llm.LLM, tp ToolProvider, findings []analyzer.Finding, cfg InvestigateConfig) ([]analyzer.Finding, InvestigateMeta)`
    - Returns a **copy** of `findings` (never mutates the input slice or its `*Analysis` pointers) with `Analysis` set on every `severity>=warning` finding.
    - Grouping: key = `Finding.Object.String()`; group severity = max member `Severity.Rank()`. Order groups: severity desc, then key asc. Keep the first `cfg.MaxGroups`; each remaining group's findings get `Analysis{Confidence:"none", ProbableCause:"not investigated — budget", Remediation:""}`.
    - Per group: build the system prompt (`prompts.Load("investigate_system")`) + a user message listing the group's findings (ruleId, severity, summary, evidence JSON) and any `snap` pre-pulled logs (the provider exposes them via `snapshot.*`, so the loop can just tell the model "logs are available via snapshot tools" — do NOT stuff raw logs into the prompt; keep the prompt small). Run:
      ```
      loop:
        if exceeded, _ := cfg.Budget.Exceeded(); exceeded { force final answer (see below); break }
        resp := l.Complete(ctx, Request{System, Messages, Tools: tp.Tools(), MaxTokens: cfg.MaxTokensPerCall, Temperature: 0})
        cfg.Budget.Add(resp.Usage); meta.InputTokens += ...; meta.OutputTokens += ...
        if err from Complete → record warning, break (partial); the group's findings get a low-confidence stub
        if resp.StopReason == "tool_use":
          for each tool_use block: res, isErr, ierr := tp.Invoke(...); if ierr != nil { warning; break loop }
            append a tool_result block (Content=res, IsError=isErr); cfg.Budget.ToolCall(); meta.ToolCalls++
          append the assistant turn + the tool_result user turn to Messages; continue
        else (end_turn): parse the last text block as the answer JSON
      ```
    - "Force final answer": append a user message `"Budget reached. Reply now with the JSON object, best effort."` and do exactly one more `Complete` with `Tools: nil`.
    - Answer JSON schema: `{"findings":[{"ruleId","probableCause","confidence","remediation"}]}`. Map each entry onto the group finding with that `ruleId` (first match). `confidence` not in `{high,medium,low}` → coerce to `low`. Missing entry for a finding → low-confidence stub `{ProbableCause:"no analysis returned", Confidence:"low"}`.
    - Parse failure: one retry with a `"Return ONLY the JSON object, nothing else."` nudge and `Tools: nil`; second failure → every finding in the group gets `{ProbableCause:"agent response could not be parsed", Confidence:"low"}` and a warning is recorded.

- [ ] **Step 1: Write the failing test**

`internal/agent/investigate_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// fakeLLM replays a scripted list of responses; each Complete pops the next.
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

func warn(f analyzer.Finding) analyzer.Finding { f.Severity = analyzer.SeverityWarning; return f }

func TestInvestigateEnrichesFromToolThenJSON(t *testing.T) {
	findings := []analyzer.Finding{warn(analyzer.Finding{
		RuleID: "reliability/crashloop", Domain: "reliability",
		Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "p", Name: "api-1"},
		Summary: "crashlooping",
	})}
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "tool_use", Blocks: []llm.Block{{Type: "tool_use", ToolName: "k8s.logs", ToolID: "t1", Input: json.RawMessage(`{"namespace":"p","pod":"api-1"}`)}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"reliability/crashloop","probableCause":"bad config env var","confidence":"high","remediation":"fix FOO"}]}`}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, findings)

	out, meta := Investigate(context.Background(), l, tp, findings, InvestigateConfig{
		MaxGroups: 10, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096,
	})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "bad config env var", out[0].Analysis.ProbableCause)
	require.Equal(t, "high", out[0].Analysis.Confidence)
	require.Equal(t, 1, meta.ToolCalls)
	require.Nil(t, findings[0].Analysis, "input slice must not be mutated")
}

func TestInvestigateBudgetForcesFinalAnswer(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	toolResp := llm.Response{StopReason: "tool_use", Blocks: []llm.Block{{Type: "tool_use", ToolName: "snapshot.events", ToolID: "t", Input: json.RawMessage(`{"kind":"Pod","name":"p"}`)}}}
	l := &fakeLLM{responses: []llm.Response{toolResp, toolResp, // 2 tool calls hits MaxToolCalls:2
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: `{"findings":[{"ruleId":"x/y","probableCause":"c","confidence":"low","remediation":"r"}]}`}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})
	out, _ := Investigate(context.Background(), l, tp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 2, MaxTokens: 40000}, MaxTokensPerCall: 4096})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "low", out[0].Analysis.Confidence)
}

func TestInvestigateBadJSONTwiceStubs(t *testing.T) {
	f := warn(analyzer.Finding{RuleID: "x/y", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}})
	l := &fakeLLM{responses: []llm.Response{
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: "not json"}}},
		{StopReason: "end_turn", Blocks: []llm.Block{{Type: "text", Text: "still not json"}}},
	}}
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{f})
	out, meta := Investigate(context.Background(), l, tp, []analyzer.Finding{f},
		InvestigateConfig{MaxGroups: 10, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096})
	require.NotNil(t, out[0].Analysis)
	require.Equal(t, "low", out[0].Analysis.Confidence)
	require.NotEmpty(t, meta.Warnings)
}

func TestInvestigateSkipsInfoAndCapsGroups(t *testing.T) {
	info := analyzer.Finding{RuleID: "slo/skipped", Severity: analyzer.SeverityInfo,
		Object: analyzer.ObjectRef{Kind: "X", Name: "y"}}
	g1 := warn(analyzer.Finding{RuleID: "a/1", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p1"}})
	g2 := warn(analyzer.Finding{RuleID: "a/2", Object: analyzer.ObjectRef{Kind: "Pod", Name: "p2"}})
	l := &fakeLLM{} // every Complete → empty end_turn → parse fails → stub
	tp := NewInProcessToolProvider(regWithK8s(t), &snapshot.Snapshot{}, []analyzer.Finding{info, g1, g2})
	out, _ := Investigate(context.Background(), l, tp, []analyzer.Finding{info, g1, g2},
		InvestigateConfig{MaxGroups: 1, Budget: Budget{MaxToolCalls: 8, MaxTokens: 40000}, MaxTokensPerCall: 4096})

	byID := map[string]*analyzer.Analysis{}
	for _, f := range out {
		byID[f.RuleID] = f.Analysis
	}
	require.Nil(t, byID["slo/skipped"], "info findings are not investigated")
	require.NotNil(t, byID["a/1"])
	require.NotNil(t, byID["a/2"])
	require.Equal(t, "not investigated — budget", byID["a/2"].ProbableCause) // beyond MaxGroups:1
}
```

> Implementer: `regWithK8s(t)` is a helper (put it in `tools_test.go` or a shared `agent_test.go`) returning a probed `*connector.Registry` with an available `fakeConn{name:"k8s"}`.

- [ ] **Step 2–4:** RED, implement `investigate.go`, full suite, `go build`, commit `feat: agent Investigate — batch-by-object bounded loop`.

---

## Task 9: `agent.Correlate`

**Files:**
- Create: `internal/agent/correlate.go`
- Test: `internal/agent/correlate_test.go`

**Interfaces:**
- Produces: `func Correlate(ctx context.Context, l llm.LLM, findings []analyzer.Finding, maxTokens int) ([]analyzer.Finding, []string /*warnings*/)`
  - One `l.Complete` call: system = `prompts.Load("correlate")`, user = a JSON list of `{key: "<ruleId>@<object>", severity, summary, probableCause}` for every finding that has an `Analysis`.
  - Parse `{"correlations":[{"key","related":[...]}]}`. For each `key` that resolves to an input finding, set that finding's `Analysis.CorrelatedFindings` to the subset of `related` that also resolve (dedup, sorted). Unresolvable keys → dropped, one aggregated warning.
  - Complete error / parse failure → return `findings` unchanged + a warning. Never fatal.
  - Returns a copy; input not mutated.

- [ ] **Step 1: failing test** — two findings with `Analysis`; `fakeLLM` returns `{"correlations":[{"key":"a/1@Pod/p/x","related":["b/2@Pod/p/x","ghost/9@Pod/none"]}]}` → `out` finding `a/1` has `CorrelatedFindings == ["b/2@Pod/p/x"]` (ghost dropped), a warning mentions the dropped ref. A second test: `fakeLLM` errors → `out` equals input, one warning.
- [ ] **Step 2–4:** RED, implement, commit `feat: agent Correlate — cross-finding root-cause links`.

---

## Task 10: `agent.Synthesize`

**Files:**
- Create: `internal/agent/synthesize.go`
- Test: `internal/agent/synthesize_test.go`

**Interfaces:**
- Produces: `func Synthesize(ctx context.Context, l llm.LLM, findings []analyzer.Finding, counts SynthCounts, connectors []string, maxTokens int) (headline string, actions []string, warnings []string)`
  - `type SynthCounts struct { Critical, Warning, Info int }`
  - One call: system = `prompts.Load("synthesize")`, user = JSON of `{counts, connectors, findings:[{key,severity,summary,probableCause,confidence}]}`.
  - Parse `{"headline","actions":[...]}`. Keep `headline` verbatim. Keep `actions` verbatim BUT drop any action string that names a `ruleId`/object token not present in the input (soft check: only drop if it clearly cites a non-existent `x/y@Kind/...` key; otherwise keep) — record a warning per drop.
  - Error / parse failure → `("", nil, []string{warn})`. The orchestrator then leaves `report.Summary` nil.

- [ ] **Step 1: failing test** — `fakeLLM` returns `{"headline":"payments is down","actions":["Fix reliability/crashloop@Pod/p/api-1 first","Investigate ghost/0@Pod/none"]}` with only the first finding present → `headline` kept, `actions == ["Fix reliability/crashloop@Pod/p/api-1 first"]`, a warning about the dropped action. Error path → empty + warning.
- [ ] **Step 2–4:** RED, implement, commit `feat: agent Synthesize — executive headline + prioritised actions`.

---

## Task 11: `report` — `meta.llm` + Markdown Summary section + banner

**Files:**
- Modify: `internal/report/report.go`, `internal/report/markdown.go`
- Test: `internal/report/report_test.go`, `internal/report/markdown_test.go`, regenerate `internal/report/testdata/golden_basic.md`

**Interfaces:**
- Produces (package `report`):
  - `type LLMMeta struct { Status, Provider, Model, PromptVersion string; InputTokens, OutputTokens, ToolCalls int; WallClockMs int64; Warnings []string }` — json `status`,`provider`,`model`,`promptVersion`,`inputTokens`,`outputTokens`,`toolCalls`,`wallClockMs`,`warnings`.
  - `report.Meta` gains `LLM *LLMMeta` (json `llm,omitempty`).
  - `Build` is unchanged (still `Build(meta, statuses, findings)`); the orchestrator sets `rep.Summary` and `rep.Meta.LLM` after `Build` returns. No signature change.
- Modifies `markdown.go`:
  - Directly under the `**<context>** · lookback …` line: if `Meta.LLM != nil` and `strings.HasPrefix(Meta.LLM.Status, "skipped")` or `"partial"` → a line `> ⚠️ AI analysis <status>` (blockquote). Nothing when status is `ok` or `LLM` is nil.
  - A `## Summary` section immediately before `## Findings`, rendered only when `r.Summary != nil`: the `Headline` as a paragraph, then `Actions` as a numbered list. When `r.Summary == nil`, no section (M2 output shape preserved).
  - The existing `{{if .Analysis}}` block under each finding already renders `Probable cause` / `Remediation` / `Confidence` (M1) — verify it still does; add a `Correlated:` line listing `Analysis.CorrelatedFindings` when non-empty.

- [ ] **Step 1: Write the failing tests** — add:
  - `report_test.go`: `TestBuildThenSetLLMMetaMarshals` — build a report, set `rep.Meta.LLM = &LLMMeta{Status:"ok",Provider:"anthropic",...}`, `rep.JSON()` contains `"llm"` and `"status": "ok"`; a report with `Meta.LLM == nil` marshals with no `"llm"` key.
  - `markdown_test.go`: `TestMarkdownSummarySection` (Summary set → `## Summary` + numbered actions present); `TestMarkdownSkippedBanner` (`Meta.LLM.Status = "skipped: no api key"` → blockquote banner present; status `"ok"` → absent); `TestMarkdownCorrelatedLine` (finding with `Analysis.CorrelatedFindings` → a `Correlated:` line).
- [ ] **Step 2: RED.**
- [ ] **Step 3: implement**; then `UPDATE_GOLDEN=1 go test ./internal/report/ -run TestMarkdownGolden` and **`git diff internal/report/testdata/golden_basic.md` MUST be empty** (the M1 golden has no Summary, no LLM meta, one finding with evidence and no Analysis → all the new branches are `{{if}}`-false). If the diff is non-empty, fix the template trim markers and regenerate — do not commit a churned golden without justifying every line.
- [ ] **Step 4:** full suite, `go build`, commit `feat: report — meta.llm block, Summary section, AI-skipped banner`.

---

## Task 12: `config` — `llm` block v2

**Files:**
- Modify: `internal/config/config.go`, `internal/config/testdata/full.yaml` (add an `llm` block), `poirot.example.yaml`
- Test: `internal/config/config_test.go`

**Interfaces:**
- `type LLM struct` becomes:
  ```go
  type LLM struct {
      Provider                string   `json:"provider"`
      Model                   string   `json:"model"`
      APIKeyEnv               string   `json:"apiKeyEnv"`
      BaseURL                 string   `json:"baseURL"`
      MaxToolCallsPerGroup    int      `json:"maxToolCallsPerGroup"`
      MaxTokensPerGroup       int      `json:"maxTokensPerGroup"`
      MaxFindingsInvestigated int      `json:"maxFindingsInvestigated"`
      GlobalBudget            Duration `json:"globalBudget"`
  }
  ```
  (`MaxToolCallsPerFinding` is **removed**.)
- `Default()`: `Provider:"anthropic"`, `Model:"claude-sonnet-5"`, `APIKeyEnv:"POIROT_LLM_API_KEY"`, `BaseURL:""`, `MaxToolCallsPerGroup:8`, `MaxTokensPerGroup:40000`, `MaxFindingsInvestigated:15`, `GlobalBudget: Duration(5*time.Minute)`.
- `applyDefaults()`: fill each zero field from `Default()` (mirror the existing per-field pattern).
- `Validate()`: keep the provider enum + failOn + lookback checks; add:
  - `if c.LLM.Provider != "none" && c.LLM.Model == "" → error("llm.model is required unless llm.provider is none")`
  - `if time.Duration(c.LLM.GlobalBudget) <= 0 → error`
  - `if c.LLM.MaxTokensPerGroup < 1000 → error`
  - `if c.LLM.MaxToolCallsPerGroup < 1 || c.LLM.MaxFindingsInvestigated < 1 → error`
- `poirot.example.yaml` `llm:` block updated to the new keys with comments (provider/model/apiKeyEnv/baseURL and the four numeric knobs + `globalBudget: 5m`).

- [ ] **Step 1: failing tests** — `TestLoadFullOverridesDefaults` extended to assert the new keys parse; new `TestValidateRejectsEmptyModelWithProvider` (`provider: anthropic`, `model: ""` → error); `TestValidateRejectsZeroGlobalBudget`; `TestDefaultLLMShape`.
- [ ] **Step 2–4:** RED, implement, update the two yaml files, full suite (`grep -rn MaxToolCallsPerFinding internal/` must be empty afterwards), `go build`, commit `feat: config — llm block v2 (per-group caps, apiKeyEnv, baseURL, globalBudget)`.

---

## Task 13: `--no-llm` flag

**Files:**
- Modify: `cmd/poirot/run.go`
- Test: `cmd/poirot/run_test.go`

**Interfaces:**
- `newRunCmd`: add `cmd.Flags().Bool("no-llm", false, "skip the LLM investigation/synthesis phases")`. In `RunE`, after `config.Load`, `if noLLM, _ := cmd.Flags().GetBool("no-llm"); noLLM { cfg.LLM.Provider = "none" }`.
- The stdout summary line gains ` — AI analysis: <status>` where `<status>` comes from `res.Report.Meta.LLM` (`"disabled"` when `nil`).

- [ ] **Step 1: failing test** — `run_test.go`: invoke `newRootCmd("t")` with `["run","-c",<clusterless cfg>,"--no-llm"]`; assert it still errors on the missing cluster (wiring only) AND that the flag is registered (`cmd.Flags().Lookup("no-llm") != nil` via a direct `newRunCmd` call). A fuller assertion (status string in output) is covered at the orchestrator level in Task 14.
- [ ] **Step 2–4:** RED, implement, commit `feat: --no-llm flag on run`.

---

## Task 14: orchestrator — investigate/correlate/synthesize phases + auto-fallback

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Test: `internal/orchestrator/orchestrator_test.go`

**Interfaces:**
- `Options` gains `LLM llm.LLM` (`// nil => built from Config.LLM`).
- A new unexported `buildLLM(cfg *config.Config) (llm.LLM, string /*skipReason, "" if built*/)`:
  - `cfg.LLM.Provider == "none"` → `nil, "disabled"`.
  - key := `os.Getenv(cfg.LLM.APIKeyEnv)`; if `provider != "openai-compatible"` and `key == ""` → `nil, "skipped: no api key"`. (openai-compatible allows empty key for local servers, but still needs `BaseURL`; empty `BaseURL` there → `nil, "skipped: no baseURL"`.)
  - `provider == "anthropic"` → `anthropic.New(anthropic.Options{APIKey: key, Model: cfg.LLM.Model, BaseURL: cfg.LLM.BaseURL})`; construct error → `nil, "skipped: " + err`.
  - `provider == "openai-compatible"` → `openaicompat.New(...)` likewise.
- In `Run`, after the analyze loop builds `findings` and before `report.Build`:
  ```go
  var summary *report.Summary
  llmMeta := &report.LLMMeta{Status: "ok", Provider: cfg.LLM.Provider, Model: cfg.LLM.Model}
  l := opts.LLM
  if l == nil {
      var reason string
      l, reason = buildLLM(cfg)
      if l == nil {
          llmMeta.Status = reason
      }
  }
  if l != nil {
      start := time.Now()
      llmMeta.Model = l.Model()
      _, pv, _ := prompts.Load("investigate_system"); llmMeta.PromptVersion = pv
      bctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.LLM.GlobalBudget))
      enriched, im := agent.Investigate(bctx, l, agent.NewInProcessToolProvider(reg, snap, findings), findings, agent.InvestigateConfig{
          MaxGroups: cfg.LLM.MaxFindingsInvestigated,
          Budget:    agent.Budget{MaxToolCalls: cfg.LLM.MaxToolCallsPerGroup, MaxTokens: cfg.LLM.MaxTokensPerGroup},
          MaxTokensPerCall: 4096,
      })
      findings = enriched
      corr, cw := agent.Correlate(bctx, l, findings, 4096); findings = corr
      hl, acts, sw := agent.Synthesize(bctx, l, findings, agent.SynthCounts{...}, connectorNames(statuses), 4096)
      cancel()
      llmMeta.InputTokens = im.InputTokens; llmMeta.OutputTokens = im.OutputTokens; llmMeta.ToolCalls = im.ToolCalls
      llmMeta.Warnings = append(append(append([]string{}, im.Warnings...), cw...), sw...)
      llmMeta.WallClockMs = time.Since(start).Milliseconds()
      if hl != "" { summary = &report.Summary{Headline: hl, Actions: acts} }
      if bctx.Err() == context.DeadlineExceeded { llmMeta.Status = "partial: global budget exceeded" }
      if len(llmMeta.Warnings) > 0 && llmMeta.Status == "ok" { llmMeta.Status = "partial: see warnings" }
  }
  ```
  Then `rep := report.Build(meta, statuses, findings)`; `rep.Summary = summary`; `rep.Meta.LLM = llmMeta` (always set — `status:"disabled"` shortened from the `"disabled"` reason if you prefer, but keep the field present so the Markdown banner logic and tests have something to read; when `Provider=="none"` set `llmMeta = &report.LLMMeta{Status:"disabled"}` only).
  Exit code path unchanged.
- The `investigate` phase never returns an error from `Run` — every failure mode degrades into `llmMeta.Status` / `Warnings`.

- [ ] **Step 1: Write the failing tests** — add to `orchestrator_test.go`, using the existing `fakePromql` + a new `fakeLLM` (copy the one from `agent` tests or a local minimal version) + `k8s.NewWithClient`:
  - `TestRunFullLLMPath`: `Options.LLM = <fakeLLM scripted: end_turn with valid investigate JSON, then correlate JSON, then synthesize JSON>`; a fake cluster with a crashlooping pod. Assert `res.Report.Findings` has a non-nil `Analysis` on the crashloop finding, `res.Report.Summary != nil`, `res.Report.Meta.LLM.Status == "ok"`, `Meta.LLM.PromptVersion` matches `^v\d+\+`.
  - `TestRunNoAPIKey`: `Options.LLM == nil`, `cfg.LLM.Provider == "anthropic"`, `POIROT_LLM_API_KEY` unset (use `t.Setenv(cfg.LLM.APIKeyEnv, "")`). Assert phase skipped: every `Analysis == nil`, `Summary == nil`, `Meta.LLM.Status == "skipped: no api key"`, exit code unchanged from the deterministic run, and `res.Report` byte-for-byte equals a `provider:"none"` run of the same inputs (build both, `JSON()` both, compare — they must be identical).
  - `TestRunLLMErrorMidInvestigate`: `fakeLLM` returns an error on the 2nd `Complete`. Assert `Meta.LLM.Status` starts `"partial"`, at least one finding still got a (stub) `Analysis`, `Run` returns no error, exit code unaffected.
- [ ] **Step 2–4:** RED, implement, **run `go test ./internal/orchestrator/ -race -v` and the full suite**, `go build ./... && go build -tags tools ./...`, commit `feat: orchestrator — investigate/correlate/synthesize phases with clean LLM auto-fallback`.

---

## Task 15: determinism test split, docs, folded M2 carry-forwards

**Files:**
- Modify: `internal/report/determinism_test.go`, `internal/report/report_test.go`
- Modify: `internal/metrics/pack.go`
- Modify: `internal/config/config.go`
- Modify: `internal/orchestrator/orchestrator.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `README.md`
- Test: the above + `internal/connector/promql/promql_test.go` (disabled-branch), `internal/config/config_test.go`

**Interfaces / changes:**

1. **Determinism split** (`determinism_test.go`):
   - Add `TestReportSortingIgnoresAnalysis`: build two `Report`s from the same `[]analyzer.Finding` — one with every `Analysis == nil`, one with a distinct non-nil `Analysis` + `Summary` on each — and assert `len(Findings)` equal, and for every `i`: `a.Findings[i].{RuleID,Domain,Severity,Object,Evidence}` deep-equal `b.Findings[i].{...}` (i.e. order + core fields identical; only `Analysis` differs).
   - Extend `TestBuildOutputIsByteStable`: also build 50× with a **fixed** non-nil `Analysis`/`Summary` and assert the JSON is identical across the 50 — after blanking `Meta.LLM` (or asserting on a sub-object that excludes it). Comment: "byte-stability holds for the deterministic core + any *fixed* LLM content; live LLM content varies and is out of scope for this test."

2. **M2 carry-forward: `promql.url` validation** (`config.go` `Validate`):
   ```go
   switch u := c.Connectors.PromQL.URL; {
   case u == "auto" || u == "disabled":
   case strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://"):
   default:
       return fmt.Errorf("connectors.promql.url must be auto|disabled|an http(s) URL, got %q", u)
   }
   ```
   Test: `TestValidateRejectsBadPromqlURL` (`"Auto"` → error).

3. **M2 carry-forward: always register promql** (`orchestrator.go`): delete the `if cfg.Connectors.PromQL.URL != "disabled"` guard around the promql registration — always `reg.Register(promql.New(...))` (or `opts.Promql`). `promql.Probe` already returns `absent / "disabled in config"` for `URL=="disabled"`, so the connector table now shows `promql | absent | disabled in config` instead of omitting it. Update `TestRunSkipsSLOWhenPromqlAbsent` / the M2 `promqlConfig()` split if needed (the `slo/skipped` assertion still holds — `reg.Satisfied(["promql"])` is false when absent). Add `TestPromqlProbeDisabled` in `promql_test.go` asserting `New(Options{URL:"disabled"}).Probe(ctx)` → `{State: absent, Reason: "disabled in config"}` (may already exist — keep/confirm).

4. **M2 carry-forward: `cpu_saturation` denominator guard + comment** (`metrics/pack.go`):
   - `cpu_saturation` expr → `max by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]) / ((container_spec_cpu_quota{container!="",container!="POD"} / container_spec_cpu_period{container!="",container!="POD"}) > 0))`
   - Add a comment above `DefaultPack`: `// NOTE: PromQL "expr == 0" / "expr > 0" are FILTERS — they return matching series carrying their original value, not a boolean. pod_not_ready and targets_down rely on this; the saturation exprs guard their divisors with "(... > 0)" so an unlimited container yields no row rather than +Inf.`
   - No behaviour test change required (query-string fix); if `collect_test.go` asserts the old `cpu_saturation` string, update it.

5. **CI**: `.github/workflows/ci.yml` — add `- run: go build -tags tools ./...` after the existing `go build ./...` step. Do NOT touch `go-version`.

6. **README**: status line → `Status: M3 — reliability + slo + change, plus an LLM investigation layer (probable cause, correlation, executive summary). Runs with no API key (analysis skipped, deterministic report).` Add a short `## LLM analysis` paragraph: set `POIROT_LLM_API_KEY`, configure `llm.provider`/`llm.model`; `poirot run --no-llm` for the deterministic-only report.

- [ ] **Step 1:** write/adjust the tests listed above → RED where applicable.
- [ ] **Step 2:** apply all six changes.
- [ ] **Step 3:** `go test ./... -race -count=1`, `go vet ./...`, `gofmt -l .`, `go build ./...`, `go build -tags tools ./...`, `go test ./internal/report/ -run TestMarkdownGolden` — all green; golden unchanged.
- [ ] **Step 4:** commit `fix: determinism test split + folded M2 carry-forwards (promql.url validation, always-register promql, cpu_saturation guard, ci tools build); docs for M3`.

---

## Self-Review

**1. Spec coverage.** Every Section 1–6 element maps to a task:
- LLM interface + providers → T1–T3. Prompts + versioning → T1 (`vN+hash`). ✅
- `k8s.logs`/`k8s.get` capabilities + collect-phase log pull → T4, T5. ✅
- `ToolProvider` + snapshot tools + connector bridge + isErr/err split → T6. ✅
- Budget caps → T7. Batch-by-object bounded loop + JSON answer + retry + stub → T8. ✅
- Correlation pass (ref-validated) → T9. Synthesis (ref-validated) → T10. ✅
- `meta.llm` block + Markdown Summary + skipped banner + `omitempty` shape preservation → T11. ✅
- Config `llm` v2 (rename, new knobs, model-required validation) → T12. `--no-llm` → T13. ✅
- Orchestrator phases 4–6 + `buildLLM` auto-fallback + global budget + partial status → T14. ✅
- Determinism split (`TestReportSortingIgnoresAnalysis`, extended `TestBuildOutputIsByteStable`) → T15. ✅
- Folded M2 carry-forwards (promql.url validate, always-register promql, cpu_saturation guard + comment, ci `-tags tools`) → T15. Docs → T15. ✅
- Deferred items (namespace scoping, `Services("").List`, change rule-quality, MCP) — left deferred, not in this plan. ✅

**2. Placeholder scan.** No `TBD`/`TODO`/"handle errors". The two `Step 2–4` compressions (T3, T5, T7, T9, T10) each carry a full Interfaces block + explicit test scenarios + a commit message; they are shorthand for the identical RED→GREEN→build→commit rhythm, not missing content. T2 defers exact Anthropic wire details to the `claude-api` skill *by name*, with the mapping fully specified — that is a real instruction, not a gap.

**3. Type consistency.** `llm.LLM` (`Complete`/`Model`) — same signature in T1 (def), T2/T3 (impl), T8/T9/T10/T14 (consumers). `llm.Response`/`Block`/`StopReason` values (`"end_turn"|"tool_use"|"max_tokens"`) consistent T1↔T2↔T8. `ToolProvider.Invoke` 3-return `(json.RawMessage, bool, error)` — T6 def, T8 consumer, matches spec (deliberately wider than `connector.Query`'s 2-return; T6 documents the mapping). `agent.Budget{MaxToolCalls,MaxTokens}` — T7 def, T8/T14 use. `agent.InvestigateConfig{MaxGroups,Budget,MaxTokensPerCall}` — T8 def, T14 constructs with `cfg.LLM.MaxFindingsInvestigated`/`MaxToolCallsPerGroup`/`MaxTokensPerGroup`. `report.LLMMeta` fields — T11 def, T14 populates every field, T11 markdown reads `Status`. `report.Build` signature **unchanged** (T11 explicit) — T14 sets `rep.Summary`/`rep.Meta.LLM` post-Build, consistent with how M2 orchestrator already sets fields on the returned `Report`. `config.LLM` new shape — T12 def; T14 reads `Provider`,`Model`,`APIKeyEnv`,`BaseURL`,`MaxFindingsInvestigated`,`MaxToolCallsPerGroup`,`MaxTokensPerGroup`,`GlobalBudget`; `MaxToolCallsPerFinding` removed in T12 and referenced nowhere after. `snapshot.PodLogs` key format `"Pod/<ns>/<name>"` — T5 def (built without importing `analyzer`), matches `analyzer.ObjectRef.String()` output for pods, which T6's `snapshot.*` tools rely on.

**4. Ordering.** T4 (k8s caps + `podLogs` helper + `truncateLog`) precedes T5 (collect uses `podLogs`). T1 precedes T2/T3 (interface) and T8–T10 (prompts). T6 precedes T8 (ToolProvider). T7 precedes T8 (Budget). T11 (report types) precedes T14 (orchestrator sets them). T12 (config) precedes T13/T14. T14 precedes T15's orchestrator carry-forward edit only in that T15 re-touches `orchestrator.go` — T15 is last by design.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-09-05-poirot-m3-agent.md`. Two execution options:

**1. Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration. T2 (anthropic) and T14 (orchestrator integration) are the high-risk tasks; the rest are mechanical against tight interfaces.

**2. Inline Execution** — batch execution with checkpoints for review.

Which approach?
