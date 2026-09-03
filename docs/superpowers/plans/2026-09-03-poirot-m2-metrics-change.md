# poirot M2 (Metrics + Change) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a PromQL-compatible metrics connector, an `slo` analyzer (infra golden signals + scrape health), and a `change` analyzer (native rollout-risk), so `poirot run` reports on saturation, restart rate, target health, and recent deploys — degrading cleanly to the M1 reliability-only report when no metrics backend is reachable.

**Architecture:** A new `promql` connector implements the M1 `connector.Connector` contract (first real use of `Capabilities()`/`Query()`). It reaches Prometheus/VictoriaMetrics/Thanos/Mimir either at an explicit `url:` or, for `url: auto`, by discovering a well-known Service and querying it through the **Kubernetes API-server proxy** (works from a laptop with only a kubeconfig — no port-forward). The orchestrator's collect phase gains a second step: when `promql` is available, a fixed query pack runs and its results are stashed in `snapshot.Snapshot.Metrics`, keeping analyzers pure functions over the Snapshot (same rule as M1). `slo` reads `snap.Metrics`; `change` reads the k8s objects already in the Snapshot. Both are added to the orchestrator's analyzer list; `slo.Requires()` is `["promql"]`, so an absent metrics backend produces the M1 `slo/skipped` info-finding rather than an error.

**Tech Stack:** Go 1.26, `k8s.io/client-go` (`CoreV1().Services(ns).ProxyGet(...)` for the API-server proxy; `fake` clientset for tests), `net/http` + `net/http/httptest` (explicit-URL path + tests), `encoding/json`, `github.com/stretchr/testify`.

**Spec:** `docs/superpowers/specs/2026-09-01-poirot-sre-assessment-agent-design.md` (M2 = the `slo` and `change` analyzers + the PromQL connector from the connector table).

**Predecessor:** M1 plan `docs/superpowers/plans/2026-09-01-poirot-m1-skeleton.md` (merged; branch `main` at the M1 merge commit). Read its "Core interfaces" section — this plan builds directly on `analyzer.Analyzer`, `connector.Connector`/`Registry`, `snapshot.Snapshot`, `report.Build`, and `orchestrator.Run` as they exist on `main` today.

## Global Constraints

- Module path: `github.com/init-kaushal/poirot`. Go directive in `go.mod`: `go 1.26.0` — do **not** change it or `.github/workflows/ci.yml`'s `go-version: '1.26'`.
- **Read-only.** No code path may create/update/patch/delete/evict any cluster resource, or issue any write to the metrics backend. Metrics access is HTTP GET only (`/api/v1/query`, `/api/v1/query_range`), directly or via `Services(ns).ProxyGet`.
- Connector packages import only their own client libraries plus `internal/connector` and `internal/snapshot` — never `analyzer`, `orchestrator`, `agent`, `report`, or another connector.
- Every connector query is `(capabilityID string, args json.RawMessage) → (json.RawMessage, error)`. `Capability.ArgsSchema` is a real JSON Schema, declared as data.
- Analyzers are pure: `Analyze(ctx, *snapshot.Snapshot) ([]Finding, error)` with no I/O, no goroutines, no Snapshot mutation. Same `Snapshot` in ⇒ same `[]Finding` out, in slice order.
- Domain string values stay lowercase: severity `critical|warning|info`; connector state `available|degraded|absent`; new finding domains `slo` and `change`.
- Every `Finding` carries ≥1 `Evidence` whose `Query` field is the exact PromQL expression or k8s selector used. (The orchestrator's synthetic `<id>/skipped` finding is the one documented exception — it carries none.)
- Determinism: any slice derived from a Go map (label sets, reason lists) MUST be sorted before it reaches a `Finding`. `report.json` and `report.md` must be byte-identical across runs on an identical Snapshot.
- All tests run with `go test ./... -race -count=1` and pass before every commit. `go vet ./...` and `gofmt -l .` clean. `go build -tags tools ./...` must also stay clean (no `tools.go` reintroduced).
- Commit messages: `<type>: <summary>` (`feat`, `fix`, `test`, `chore`, `refactor`, `docs`). Body ends with exactly:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

---

## File Structure

| Path | Responsibility | New/Modified |
|---|---|---|
| `internal/snapshot/snapshot.go` | add `Metrics *MetricSet` field; add `MetricSample` / `MetricResult` / `MetricSet` types + `MetricSet.Result(name)` | Modified |
| `internal/connector/k8s/k8s.go` | add `Clientset() kubernetes.Interface` getter | Modified |
| `internal/orchestrator/orchestrator.go` | `K8sSource` gains `Clientset()`; register `promql`; run the metrics pack in the collect phase; add `change` + `slo` to the analyzer list | Modified |
| `internal/connector/promql/parse.go` | Prometheus HTTP API JSON envelope → `[]snapshot.MetricSample` / typed error | New |
| `internal/connector/promql/client.go` | `Client` over a `doer` func; `Instant` / `Range`; `httpDoer` for explicit-URL mode | New |
| `internal/connector/promql/discover.go` | `Discover(ctx, cs, namespaces)` → `*Target` (well-known Service match) | New |
| `internal/connector/promql/proxy.go` | `proxyDoer(cs, target)` — API-server proxy `doer` via `Services(ns).ProxyGet` | New |
| `internal/connector/promql/promql.go` | the `Connector`: `New`, `Name`, `Probe`, `Capabilities`, `Query` | New |
| `internal/metrics/pack.go` | `Entry` + `DefaultPack()` — the 5 fixed PromQL expressions | New |
| `internal/metrics/collect.go` | `Collect(ctx, connector.Connector, []Entry, at)` → `*snapshot.MetricSet` | New |
| `internal/analyzer/slo/slo.go` | `Analyzer` impl: `ID`/`Requires`/`Analyze` dispatch | New |
| `internal/analyzer/slo/rules.go` | the 5 slo rule funcs + shared `finding`/`ev` helpers | New |
| `internal/analyzer/change/change.go` | `Analyzer` impl: `ID`/`Requires`/`Analyze` dispatch | New |
| `internal/analyzer/change/rules.go` | the 3 change rule funcs + helpers | New |
| `internal/report/markdown.go` | `{{if .Evidence}}` guard; all-skipped message; escape `|` in table cells | Modified |
| `internal/report/report.go` | normalise `meta.Namespaces` nil → `[]string{}` | Modified |
| `internal/report/determinism_test.go` | strengthen the byte-stability fixture (map-derived slice) | Modified |
| `internal/report/testdata/golden_basic.md` | regenerate if template whitespace shifts | Modified (maybe) |
| `poirot.example.yaml` | uncomment + document `connectors.promql` | Modified |
| `README.md` | M2 status line + one PromQL sentence | Modified |
| `hack/demo-broken.yaml` | add a CPU-saturating workload for the `slo` demo | Modified |

Test files sit beside their targets. New fixture dirs: `internal/connector/promql/testdata/` (recorded Prometheus JSON), `internal/analyzer/slo/testdata/` and `internal/analyzer/change/testdata/` (only if a fixture is large enough to warrant a file; small ones are inline).

---

## Task 1: Snapshot metric types

**Files:**
- Modify: `internal/snapshot/snapshot.go`
- Test: `internal/snapshot/snapshot_test.go` (new)

**Interfaces:**
- Consumes: stdlib `time` only.
- Produces (package `snapshot`):
  - `type MetricSample struct { Labels map[string]string; Value float64 }` — json tags `labels`, `value`.
  - `type MetricResult struct { Name string; Expr string; Samples []MetricSample; Error string }` — json tags `name`, `expr`, `samples`, `error,omitempty`. `Name` is the pack-entry name (e.g. `"cpu_saturation"`); `Expr` is the PromQL; `Error` set (non-empty) when that one query failed.
  - `type MetricSet struct { Backend string; CollectedAt time.Time; Results []MetricResult }` — json tags `backend`, `collectedAt`, `results`.
  - `func (m *MetricSet) Result(name string) *MetricResult` — returns the matching result or `nil`. Nil-receiver safe (`if m == nil { return nil }`).
  - `Snapshot` gains a field: `Metrics *MetricSet` (nil = metrics not collected). Place it after `Services`.

- [ ] **Step 1: Write the failing test**

`internal/snapshot/snapshot_test.go`:

```go
package snapshot

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetricSetResult(t *testing.T) {
	m := &MetricSet{Results: []MetricResult{
		{Name: "cpu_saturation", Expr: "rate(...)"},
		{Name: "targets_down", Expr: "up == 0"},
	}}
	require.Equal(t, "up == 0", m.Result("targets_down").Expr)
	require.Nil(t, m.Result("nope"))

	var nilSet *MetricSet
	require.Nil(t, nilSet.Result("cpu_saturation"))
}

func TestSnapshotHasMetricsField(t *testing.T) {
	var s Snapshot
	require.Nil(t, s.Metrics) // zero value is nil pointer
	s.Metrics = &MetricSet{Backend: "prometheus"}
	require.Equal(t, "prometheus", s.Metrics.Backend)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/snapshot/ -v`
Expected: FAIL — `undefined: MetricSet`.

- [ ] **Step 3: Write the implementation**

Add to `internal/snapshot/snapshot.go` (after the existing `Snapshot` struct):

```go
// MetricSample is one time series' current value with its label set.
type MetricSample struct {
	Labels map[string]string `json:"labels"`
	Value  float64           `json:"value"`
}

// MetricResult is the outcome of one query-pack entry. Error is set (and
// Samples empty) when that single query failed; a failed entry never aborts
// the collect.
type MetricResult struct {
	Name    string         `json:"name"`
	Expr    string         `json:"expr"`
	Samples []MetricSample `json:"samples"`
	Error   string         `json:"error,omitempty"`
}

// MetricSet is the fixed query pack's results, collected once per run.
type MetricSet struct {
	Backend     string         `json:"backend"`
	CollectedAt time.Time      `json:"collectedAt"`
	Results     []MetricResult `json:"results"`
}

// Result returns the pack entry by name, or nil. Safe on a nil receiver.
func (m *MetricSet) Result(name string) *MetricResult {
	if m == nil {
		return nil
	}
	for i := range m.Results {
		if m.Results[i].Name == name {
			return &m.Results[i]
		}
	}
	return nil
}
```

Add the field to `Snapshot`:

```go
	Services     []corev1.Service
	Metrics      *MetricSet // nil until the metrics collect step runs
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/snapshot/ ./... -race -count=1`
Expected: snapshot tests PASS; every other package still builds and passes (the new field is additive).

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/
git commit -m "feat: snapshot metric types — MetricSet, MetricResult, MetricSample"
```

---

## Task 2: Expose the k8s clientset

**Files:**
- Modify: `internal/connector/k8s/k8s.go`
- Modify: `internal/orchestrator/orchestrator.go` (interface only — add one method to `K8sSource`)
- Test: `internal/connector/k8s/k8s_test.go` (add one test)

**Interfaces:**
- Produces (package `k8s`): `func (c *Connector) Clientset() kubernetes.Interface` — returns the connector's clientset (the injected one for `NewWithClient`, the built one for `New`).
- Modifies (package `orchestrator`): `K8sSource` interface gains `Clientset() kubernetes.Interface`. This requires importing `k8s.io/client-go/kubernetes` in `orchestrator.go`. `*k8s.Connector` already satisfies it after the getter is added; the orchestrator test fakes must add the method (they wrap a `fake.Clientset`, so `return f.cs`).

- [ ] **Step 1: Write the failing test**

Add to `internal/connector/k8s/k8s_test.go`:

```go
func TestClientsetGetterReturnsInjected(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewWithClient(cs, "ctx", Scope{})
	require.Same(t, cs, c.Clientset())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/k8s/ -run TestClientsetGetter -v`
Expected: FAIL — `c.Clientset undefined`.

- [ ] **Step 3: Write the implementation**

In `internal/connector/k8s/k8s.go`, next to `ContextName()`:

```go
// Clientset returns the underlying Kubernetes client, for connectors that
// reach in-cluster services through the API server (e.g. promql via the
// service proxy). Read-only use only.
func (c *Connector) Clientset() kubernetes.Interface { return c.cs }
```

In `internal/orchestrator/orchestrator.go`, extend the interface and add the import:

```go
import (
	// ...existing...
	"k8s.io/client-go/kubernetes"
)

type K8sSource interface {
	connector.Connector
	Collect(ctx context.Context) (*snapshot.Snapshot, error)
	ContextName() string
	Clientset() kubernetes.Interface
}
```

- [ ] **Step 4: Fix the orchestrator test fakes**

`internal/orchestrator/orchestrator_test.go` — the `unreachableK8s` stub (and any other `K8sSource` fake) needs the method. `unreachableK8s` has no real client; give it:

```go
func (unreachableK8s) Clientset() kubernetes.Interface { return fake.NewSimpleClientset() }
```

(add imports `"k8s.io/client-go/kubernetes"` and `"k8s.io/client-go/kubernetes/fake"` to the test file if not already present). `TestRunProducesReliabilityReport` uses `k8s.NewWithClient(cs, ...)` which now has `Clientset()` for free.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -race -count=1`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/connector/k8s/ internal/orchestrator/
git commit -m "feat: expose k8s clientset via K8sSource for the metrics connector"
```

---

## Task 3: PromQL response parsing

**Files:**
- Create: `internal/connector/promql/parse.go`
- Create: `internal/connector/promql/testdata/vector_ok.json`, `matrix_ok.json`, `error.json`, `empty_vector.json`
- Test: `internal/connector/promql/parse_test.go`

**Interfaces:**
- Consumes: `snapshot.MetricSample`, `encoding/json`, `strconv`.
- Produces (package `promql`):
  - `type apiError struct { Type string; Message string }` with `func (e *apiError) Error() string` → `fmt.Sprintf("promql %s: %s", e.Type, e.Message)`.
  - `func parseInstant(raw []byte) ([]snapshot.MetricSample, error)` — decodes `{"status","data":{"resultType":"vector","result":[{"metric":{k:v},"value":[<float ts>,"<string val>"]}]}}`. `status=="error"` → `*apiError`. `resultType` other than `vector` or `scalar` → error. Each sample's `Value` parsed from the string via `strconv.ParseFloat`; a non-parseable value → error. `NaN`/`+Inf`/`-Inf` strings parse via `ParseFloat` and are kept as-is.
  - `func parseRange(raw []byte) ([]snapshot.MetricSample, error)` — decodes `resultType:"matrix"`; for each series, emit ONE `MetricSample` using the **last** `[ts,val]` pair in `values` (M2 only needs the latest point of a range; the full series is out of scope). Empty `values` for a series → skip that series.
  - Both tolerate `data.result` being `null` or `[]` → return an empty slice, nil error.

- [ ] **Step 1: Write the failing test + fixtures**

`internal/connector/promql/testdata/vector_ok.json`:

```json
{"status":"success","data":{"resultType":"vector","result":[
  {"metric":{"namespace":"payments","pod":"api-1","container":"api"},"value":[1735732800,"0.94"]},
  {"metric":{"namespace":"payments","pod":"api-2","container":"api"},"value":[1735732800,"0.31"]}
]}}
```

`internal/connector/promql/testdata/matrix_ok.json`:

```json
{"status":"success","data":{"resultType":"matrix","result":[
  {"metric":{"job":"kubelet","instance":"10.0.0.1:10250"},"values":[[1735732700,"1"],[1735732800,"0"]]}
]}}
```

`internal/connector/promql/testdata/error.json`:

```json
{"status":"error","errorType":"bad_data","error":"invalid parameter \"query\": parse error"}
```

`internal/connector/promql/testdata/empty_vector.json`:

```json
{"status":"success","data":{"resultType":"vector","result":[]}}
```

`internal/connector/promql/parse_test.go`:

```go
package promql

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func read(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return b
}

func TestParseInstantVector(t *testing.T) {
	s, err := parseInstant(read(t, "vector_ok.json"))
	require.NoError(t, err)
	require.Len(t, s, 2)
	require.Equal(t, "payments", s[0].Labels["namespace"])
	require.InDelta(t, 0.94, s[0].Value, 1e-9)
}

func TestParseInstantEmpty(t *testing.T) {
	s, err := parseInstant(read(t, "empty_vector.json"))
	require.NoError(t, err)
	require.Empty(t, s)
}

func TestParseInstantError(t *testing.T) {
	_, err := parseInstant(read(t, "error.json"))
	var ae *apiError
	require.True(t, errors.As(err, &ae))
	require.Equal(t, "bad_data", ae.Type)
}

func TestParseRangeUsesLastPoint(t *testing.T) {
	s, err := parseRange(read(t, "matrix_ok.json"))
	require.NoError(t, err)
	require.Len(t, s, 1)
	require.Equal(t, "kubelet", s[0].Labels["job"])
	require.InDelta(t, 0.0, s[0].Value, 1e-9) // last of [["...","1"],["...","0"]]
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/promql/ -v`
Expected: FAIL — `undefined: parseInstant`.

- [ ] **Step 3: Write the implementation**

`internal/connector/promql/parse.go`:

```go
package promql

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type apiError struct {
	Type    string
	Message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("promql %s: %s", e.Type, e.Message)
}

type envelope struct {
	Status    string          `json:"status"`
	ErrorType string          `json:"errorType"`
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
}

type vectorData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`  // [ <ts float>, "<val string>" ]
		Values [][2]any          `json:"values"` // matrix rows
	} `json:"result"`
}

func decode(raw []byte) (*vectorData, error) {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("promql: decode envelope: %w", err)
	}
	if env.Status == "error" {
		return nil, &apiError{Type: env.ErrorType, Message: env.Error}
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("promql: unexpected status %q", env.Status)
	}
	var d vectorData
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &d); err != nil {
			return nil, fmt.Errorf("promql: decode data: %w", err)
		}
	}
	return &d, nil
}

func sampleValue(pair [2]any) (float64, error) {
	s, ok := pair[1].(string)
	if !ok {
		return 0, fmt.Errorf("promql: sample value is %T, want string", pair[1])
	}
	return strconv.ParseFloat(s, 64)
}

func parseInstant(raw []byte) ([]snapshot.MetricSample, error) {
	d, err := decode(raw)
	if err != nil {
		return nil, err
	}
	switch d.ResultType {
	case "", "vector", "scalar":
	default:
		return nil, fmt.Errorf("promql: instant query returned resultType %q", d.ResultType)
	}
	out := make([]snapshot.MetricSample, 0, len(d.Result))
	for _, r := range d.Result {
		v, err := sampleValue(r.Value)
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot.MetricSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}

func parseRange(raw []byte) ([]snapshot.MetricSample, error) {
	d, err := decode(raw)
	if err != nil {
		return nil, err
	}
	if d.ResultType != "" && d.ResultType != "matrix" {
		return nil, fmt.Errorf("promql: range query returned resultType %q", d.ResultType)
	}
	out := make([]snapshot.MetricSample, 0, len(d.Result))
	for _, r := range d.Result {
		if len(r.Values) == 0 {
			continue
		}
		v, err := sampleValue(r.Values[len(r.Values)-1])
		if err != nil {
			return nil, err
		}
		out = append(out, snapshot.MetricSample{Labels: r.Metric, Value: v})
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/promql/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/promql/
git commit -m "feat: promql — Prometheus HTTP API response parsing"
```

---

## Task 4: PromQL client over a pluggable doer

**Files:**
- Create: `internal/connector/promql/client.go`
- Test: `internal/connector/promql/client_test.go`

**Interfaces:**
- Consumes: `parseInstant`/`parseRange` (Task 3), `net/http`, `net/url`, `time`, `context`.
- Produces (package `promql`):
  - `type doer func(ctx context.Context, path string, params url.Values) ([]byte, error)` — `path` is relative to the Prometheus API root, e.g. `"api/v1/query"`.
  - `type Client struct { do doer }` with `func newClient(do doer) *Client`.
  - `func (c *Client) Instant(ctx context.Context, expr string, at time.Time) ([]snapshot.MetricSample, error)` — calls `c.do(ctx, "api/v1/query", {query: expr, time: <unix>})` then `parseInstant`.
  - `func (c *Client) Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]snapshot.MetricSample, error)` — `"api/v1/query_range"` with `query,start,end,step` (`step` as integer seconds), then `parseRange`.
  - `func httpDoer(base string, hc *http.Client) (doer, error)` — validates `base` is an absolute http(s) URL, trims a trailing `/`, and returns a `doer` that GETs `base + "/" + path + "?" + params.Encode()`. Non-2xx → error including the status and a truncated body (≤512 bytes). `hc` nil → `&http.Client{Timeout: 15 * time.Second}`.

- [ ] **Step 1: Write the failing test**

`internal/connector/promql/client_test.go`:

```go
package promql

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientInstantViaFakeDoer(t *testing.T) {
	var gotPath string
	var gotParams url.Values
	c := newClient(func(_ context.Context, path string, p url.Values) ([]byte, error) {
		gotPath, gotParams = path, p
		return read(t, "vector_ok.json"), nil
	})

	s, err := c.Instant(context.Background(), "up", time.Unix(1735732800, 0))
	require.NoError(t, err)
	require.Len(t, s, 2)
	require.Equal(t, "api/v1/query", gotPath)
	require.Equal(t, "up", gotParams.Get("query"))
	require.Equal(t, "1735732800", gotParams.Get("time"))
}

func TestHTTPDoerHitsRealPathAndParses(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/query", r.URL.Path)
		require.Equal(t, "vector(1)", r.URL.Query().Get("query"))
		w.Write(read(t, "vector_ok.json"))
	}))
	defer srv.Close()

	d, err := httpDoer(srv.URL+"/", nil)
	require.NoError(t, err)
	c := newClient(d)
	s, err := c.Instant(context.Background(), "vector(1)", time.Unix(1735732800, 0))
	require.NoError(t, err)
	require.Len(t, s, 2)
}

func TestHTTPDoerRejectsBadBase(t *testing.T) {
	_, err := httpDoer("not-a-url", nil)
	require.Error(t, err)
}

func TestHTTPDoerNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	d, _ := httpDoer(srv.URL, nil)
	_, err := newClient(d).Instant(context.Background(), "up", time.Now())
	require.ErrorContains(t, err, "502")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/promql/ -run 'Client|HTTPDoer' -v`
Expected: FAIL — `undefined: newClient`.

- [ ] **Step 3: Write the implementation**

`internal/connector/promql/client.go`:

```go
package promql

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type doer func(ctx context.Context, path string, params url.Values) ([]byte, error)

type Client struct{ do doer }

func newClient(do doer) *Client { return &Client{do: do} }

func (c *Client) Instant(ctx context.Context, expr string, at time.Time) ([]snapshot.MetricSample, error) {
	p := url.Values{}
	p.Set("query", expr)
	p.Set("time", strconv.FormatInt(at.Unix(), 10))
	raw, err := c.do(ctx, "api/v1/query", p)
	if err != nil {
		return nil, err
	}
	return parseInstant(raw)
}

func (c *Client) Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]snapshot.MetricSample, error) {
	p := url.Values{}
	p.Set("query", expr)
	p.Set("start", strconv.FormatInt(start.Unix(), 10))
	p.Set("end", strconv.FormatInt(end.Unix(), 10))
	p.Set("step", strconv.Itoa(int(step.Seconds())))
	raw, err := c.do(ctx, "api/v1/query_range", p)
	if err != nil {
		return nil, err
	}
	return parseRange(raw)
}

func httpDoer(base string, hc *http.Client) (doer, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("promql: %q is not an absolute http(s) URL", base)
	}
	root := strings.TrimRight(base, "/")
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		full := root + "/" + path + "?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 != 2 {
			snip := body
			if len(snip) > 512 {
				snip = snip[:512]
			}
			return nil, fmt.Errorf("promql: GET %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snip)))
		}
		return body, nil
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/promql/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/promql/client.go internal/connector/promql/client_test.go
git commit -m "feat: promql — client over a pluggable doer (http + fake)"
```

---

## Task 5: PromQL Service discovery

**Files:**
- Create: `internal/connector/promql/discover.go`
- Test: `internal/connector/promql/discover_test.go`

**Interfaces:**
- Consumes: `k8s.io/client-go/kubernetes`, `k8s.io/api/core/v1`, `k8s.io/apimachinery/.../metav1`, `context`, `sort`.
- Produces (package `promql`):
  - `type Target struct { Namespace, Name, Port, Scheme string }` — `Port` is a **string** (a name or number, whichever the Service exposes; `ProxyGet` accepts either). `Scheme` is `"http"` or `"https"`.
  - `func Discover(ctx context.Context, cs kubernetes.Interface, namespaces []string) (*Target, error)` — for each namespace (in the given order; empty slice ⇒ list all namespaces, sorted), list Services and return the first whose name matches a known pattern (case-insensitive exact match against the candidate set below, OR name has one of the candidate prefixes). Pick the port: prefer a port named `http`, `web`, or `http-web`; else the port named `https` (and set `Scheme="https"`); else if exactly one port, use it; else the first port whose number is in `{9090, 8080, 8429, 8481, 10902, 9091}`. No match in any namespace ⇒ `(nil, nil)` (not an error — "no metrics backend found" is a normal outcome).
  - Candidate name set (exact, case-insensitive): `prometheus`, `prometheus-server`, `prometheus-k8s`, `prometheus-operated`, `kube-prometheus-stack-prometheus`, `thanos-query`, `thanos-query-frontend`, `victoria-metrics`, `victoria-metrics-single-server`, `vmsingle`, `vmselect`, `mimir`, `mimir-query-frontend`, `mimir-nginx`.
  - Candidate prefixes: `prometheus-`, `thanos-query`, `vmselect-`, `vmsingle-`, `mimir-query`.

- [ ] **Step 1: Write the failing test**

`internal/connector/promql/discover_test.go`:

```go
package promql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func svc(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Ports: ports},
	}
}

func TestDiscoverFindsPrometheusByName(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "monitoring"}},
		svc("monitoring", "grafana", corev1.ServicePort{Name: "http", Port: 80}),
		svc("monitoring", "prometheus-server", corev1.ServicePort{Name: "http", Port: 9090}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.NotNil(t, tgt)
	require.Equal(t, "monitoring", tgt.Namespace)
	require.Equal(t, "prometheus-server", tgt.Name)
	require.Equal(t, "http", tgt.Port)
	require.Equal(t, "http", tgt.Scheme)
}

func TestDiscoverPicksNumericPortWhenUnnamed(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "obs"}},
		svc("obs", "vmsingle", corev1.ServicePort{Port: 8429}),
	)
	tgt, err := Discover(context.Background(), cs, []string{"obs"})
	require.NoError(t, err)
	require.Equal(t, "8429", tgt.Port)
}

func TestDiscoverNoMatchIsNotAnError(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		svc("default", "web", corev1.ServicePort{Port: 80}),
	)
	tgt, err := Discover(context.Background(), cs, nil)
	require.NoError(t, err)
	require.Nil(t, tgt)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/promql/ -run Discover -v`
Expected: FAIL — `undefined: Discover`.

- [ ] **Step 3: Write the implementation**

`internal/connector/promql/discover.go`:

```go
package promql

import (
	"context"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Target struct {
	Namespace string
	Name      string
	Port      string
	Scheme    string
}

var candidateNames = map[string]bool{
	"prometheus": true, "prometheus-server": true, "prometheus-k8s": true,
	"prometheus-operated": true, "kube-prometheus-stack-prometheus": true,
	"thanos-query": true, "thanos-query-frontend": true,
	"victoria-metrics": true, "victoria-metrics-single-server": true,
	"vmsingle": true, "vmselect": true,
	"mimir": true, "mimir-query-frontend": true, "mimir-nginx": true,
}

var candidatePrefixes = []string{
	"prometheus-", "thanos-query", "vmselect-", "vmsingle-", "mimir-query",
}

var knownPorts = map[int32]bool{9090: true, 8080: true, 8429: true, 8481: true, 10902: true, 9091: true}

func nameMatches(n string) bool {
	n = strings.ToLower(n)
	if candidateNames[n] {
		return true
	}
	for _, p := range candidatePrefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func pickPort(ports []corev1.ServicePort) (port, scheme string, ok bool) {
	for _, p := range ports {
		switch strings.ToLower(p.Name) {
		case "http", "web", "http-web":
			return portString(p), "http", true
		}
	}
	for _, p := range ports {
		if strings.ToLower(p.Name) == "https" {
			return portString(p), "https", true
		}
	}
	if len(ports) == 1 {
		return portString(ports[0]), "http", true
	}
	for _, p := range ports {
		if knownPorts[p.Port] {
			return portString(p), "http", true
		}
	}
	return "", "", false
}

func portString(p corev1.ServicePort) string {
	if p.Name != "" {
		return p.Name
	}
	return strconv.Itoa(int(p.Port))
}

func Discover(ctx context.Context, cs kubernetes.Interface, namespaces []string) (*Target, error) {
	nss := namespaces
	if len(nss) == 0 {
		list, err := cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		for _, n := range list.Items {
			nss = append(nss, n.Name)
		}
		sort.Strings(nss)
	}
	for _, ns := range nss {
		svcs, err := cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		items := append([]corev1.Service(nil), svcs.Items...)
		sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
		for _, s := range items {
			if !nameMatches(s.Name) {
				continue
			}
			if port, scheme, ok := pickPort(s.Spec.Ports); ok {
				return &Target{Namespace: s.Namespace, Name: s.Name, Port: port, Scheme: scheme}, nil
			}
		}
	}
	return nil, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/promql/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/promql/discover.go internal/connector/promql/discover_test.go
git commit -m "feat: promql — well-known Service discovery for url:auto"
```

---

## Task 6: PromQL API-server proxy doer

**Files:**
- Create: `internal/connector/promql/proxy.go`
- Test: `internal/connector/promql/proxy_test.go`

**Interfaces:**
- Consumes: `k8s.io/client-go/kubernetes`, the `doer` type (Task 4), `Target` (Task 5).
- Produces (package `promql`):
  - `func proxyDoer(cs kubernetes.Interface, t Target) doer` — returns a `doer` that calls `cs.CoreV1().Services(t.Namespace).ProxyGet(t.Scheme, t.Name, t.Port, path, params) → ResponseWrapper`, then `.DoRaw(ctx)`. `params url.Values` is flattened to `map[string]string` by taking the first value of each key (the pack only ever sends single-valued params).
  - `func flattenParams(p url.Values) map[string]string` (unexported helper, exported enough to unit-test within the package).

- [ ] **Step 1: Write the failing test**

> **Fake-clientset caveat (mirrors M1's fake-discovery note):** `k8s.io/client-go/kubernetes/fake`'s `ServiceInterface` **does** provide `ProxyGet`, returning a `*restclient.Request` whose `DoRaw` will attempt a real HTTP call to a fake host and error. So `proxyDoer`'s round-trip is NOT meaningfully exercisable against the fake. Test what is deterministic: `flattenParams`, and that `proxyDoer` returns a non-nil `doer` for a given `Target`. The proxy round-trip itself is covered by the manual e2e in Task 12 (run against a real cluster with Prometheus).

`internal/connector/promql/proxy_test.go`:

```go
package promql

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"
)

func TestFlattenParams(t *testing.T) {
	p := url.Values{}
	p.Set("query", "up == 0")
	p.Set("time", "123")
	got := flattenParams(p)
	require.Equal(t, map[string]string{"query": "up == 0", "time": "123"}, got)
}

func TestProxyDoerConstructs(t *testing.T) {
	d := proxyDoer(fake.NewSimpleClientset(), Target{Namespace: "m", Name: "prom", Port: "http", Scheme: "http"})
	require.NotNil(t, d)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/promql/ -run 'Flatten|ProxyDoer' -v`
Expected: FAIL — `undefined: flattenParams`.

- [ ] **Step 3: Write the implementation**

`internal/connector/promql/proxy.go`:

```go
package promql

import (
	"context"
	"net/url"

	"k8s.io/client-go/kubernetes"
)

func flattenParams(p url.Values) map[string]string {
	m := make(map[string]string, len(p))
	for k, v := range p {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

// proxyDoer reaches the metrics backend Service through the Kubernetes
// API-server proxy subresource. Read-only: ProxyGet issues an HTTP GET.
func proxyDoer(cs kubernetes.Interface, t Target) doer {
	return func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		return cs.CoreV1().
			Services(t.Namespace).
			ProxyGet(t.Scheme, t.Name, t.Port, path, flattenParams(params)).
			DoRaw(ctx)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/promql/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/promql/proxy.go internal/connector/promql/proxy_test.go
git commit -m "feat: promql — API-server proxy doer for url:auto"
```

---

## Task 7: The PromQL connector

**Files:**
- Create: `internal/connector/promql/promql.go`
- Test: `internal/connector/promql/promql_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3-6, plus `internal/connector` (`Availability`, `Capability`, `State*`), `encoding/json`, `k8s.io/client-go/kubernetes`.
- Produces (package `promql`):
  - `type Options struct { URL string; Clientset kubernetes.Interface; Namespaces []string }` — `URL` is `cfg.Connectors.PromQL.URL` (`"auto"`, `"disabled"`, or an absolute URL). `Namespaces` is the run's scope (for discovery); empty ⇒ all.
  - `type Connector struct { ... }` (unexported: `opts Options`, and after Probe: `client *Client`, `backend string`).
  - `func New(o Options) *Connector`.
  - `func (c *Connector) Name() string` → `"promql"`.
  - `func (c *Connector) Probe(ctx context.Context) connector.Availability`:
    - `URL == "disabled"` → `{State: absent, Reason: "disabled in config"}`.
    - `URL == "auto"` → `Discover`; nil target → `{State: absent, Reason: "no metrics Service found"}`; else build `proxyDoer`, set `c.backend = "svc:" + ns + "/" + name`.
    - else (explicit URL) → `httpDoer`; bad URL → `{State: absent, Reason: "bad url", Detail: err}`; set `c.backend = sanitizeURL(URL)` (host only, no query).
    - With a doer in hand: run a health query `c.client.Instant(ctx, "vector(1)", now)`. Success → `{State: available}`. Error → `{State: degraded, Reason: "backend unreachable", Detail: <err, truncated 200 chars>}` (degraded, not absent — we found a target/URL, it just did not answer).
  - `func (c *Connector) Capabilities() []connector.Capability` — two entries, ALWAYS returned (independent of probe):
    - `promql.instant`: description `"Run an instant PromQL query. Returns the current value of each matching series."`, ArgsSchema = JSON Schema `{"type":"object","required":["expr"],"properties":{"expr":{"type":"string"},"time":{"type":"string","description":"RFC3339; defaults to now"}},"additionalProperties":false}`.
    - `promql.range`: description `"Run a PromQL range query. Returns the latest value of each matching series over the window."`, ArgsSchema requires `expr`, optional `start`,`end` (RFC3339), `stepSeconds` (integer, default 60).
  - `func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error)`:
    - Requires `c.client != nil` (i.e. Probe ran and found a doer) — else `errors.New("promql: not probed / unavailable")`.
    - `promql.instant`: unmarshal `{Expr string; Time string}`; parse `Time` (RFC3339) or `time.Now()`; `c.client.Instant(...)`; marshal `[]snapshot.MetricSample` → JSON.
    - `promql.range`: unmarshal `{Expr string; Start, End string; StepSeconds int}`; defaults: `End`=now, `Start`=end−15m, `StepSeconds`=60; `c.client.Range(...)`; marshal samples.
    - unknown id → `fmt.Errorf("promql: unknown capability %q", id)`.
  - `func sanitizeURL(raw string) string` (unexported) → scheme+host, or `raw` if unparseable.

- [ ] **Step 1: Write the failing test**

`internal/connector/promql/promql_test.go`:

```go
package promql

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func TestConnectorDisabled(t *testing.T) {
	c := New(Options{URL: "disabled"})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
	require.Contains(t, av.Reason, "disabled")
}

func TestConnectorExplicitURLProbeAndQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(read(t, "vector_ok.json"))
	}))
	defer srv.Close()

	c := New(Options{URL: srv.URL})
	require.Equal(t, "promql", c.Name())
	require.Equal(t, connector.StateAvailable, c.Probe(context.Background()).State)

	caps := c.Capabilities()
	require.Len(t, caps, 2)
	require.Equal(t, "promql.instant", caps[0].ID)
	require.NotEmpty(t, caps[0].ArgsSchema)

	args, _ := json.Marshal(map[string]string{"expr": "up"})
	raw, err := c.Query(context.Background(), "promql.instant", args)
	require.NoError(t, err)
	var got []snapshot.MetricSample
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Len(t, got, 2)
}

func TestConnectorExplicitURLDegradedWhenDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	srv.Close() // immediately closed → connection refused

	c := New(Options{URL: srv.URL})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateDegraded, av.State)
}

func TestConnectorAutoNoServiceIsAbsent(t *testing.T) {
	c := New(Options{URL: "auto", Clientset: fakeCS(), Namespaces: []string{"default"}})
	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAbsent, av.State)
	require.Contains(t, av.Reason, "no metrics Service")
}

func TestQueryBeforeProbeErrors(t *testing.T) {
	c := New(Options{URL: "disabled"})
	_, err := c.Query(context.Background(), "promql.instant", []byte(`{"expr":"up"}`))
	require.Error(t, err)
}
```

Add a tiny helper in this test file: `func fakeCS() *fake.Clientset { return fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}) }` with the appropriate imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/promql/ -run Connector -v`
Expected: FAIL — `undefined: New` (as `promql.New` with `Options`).

- [ ] **Step 3: Write the implementation**

`internal/connector/promql/promql.go`:

```go
package promql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Options struct {
	URL        string
	Clientset  kubernetes.Interface
	Namespaces []string
}

type Connector struct {
	opts    Options
	client  *Client
	backend string
}

func New(o Options) *Connector { return &Connector{opts: o} }

func (c *Connector) Name() string { return "promql" }

func (c *Connector) Probe(ctx context.Context) connector.Availability {
	switch c.opts.URL {
	case "disabled":
		return connector.Availability{State: connector.StateAbsent, Reason: "disabled in config"}
	case "auto":
		tgt, err := Discover(ctx, c.opts.Clientset, c.opts.Namespaces)
		if err != nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "service discovery failed", Detail: trunc(err.Error(), 200)}
		}
		if tgt == nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "no metrics Service found"}
		}
		c.client = newClient(proxyDoer(c.opts.Clientset, *tgt))
		c.backend = "svc:" + tgt.Namespace + "/" + tgt.Name
	default:
		d, err := httpDoer(c.opts.URL, nil)
		if err != nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "bad url", Detail: trunc(err.Error(), 200)}
		}
		c.client = newClient(d)
		c.backend = sanitizeURL(c.opts.URL)
	}

	if _, err := c.client.Instant(ctx, "vector(1)", time.Now()); err != nil {
		return connector.Availability{State: connector.StateDegraded, Reason: "backend unreachable", Detail: trunc(err.Error(), 200)}
	}
	return connector.Availability{State: connector.StateAvailable}
}

func (c *Connector) Backend() string { return c.backend }

func (c *Connector) Capabilities() []connector.Capability {
	return []connector.Capability{
		{
			ID:          "promql.instant",
			Description: "Run an instant PromQL query. Returns the current value of each matching series.",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["expr"],` +
				`"properties":{"expr":{"type":"string"},"time":{"type":"string","description":"RFC3339; defaults to now"}},` +
				`"additionalProperties":false}`),
		},
		{
			ID:          "promql.range",
			Description: "Run a PromQL range query. Returns the latest value of each matching series over the window.",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["expr"],` +
				`"properties":{"expr":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"},` +
				`"stepSeconds":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		},
	}
}

func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("promql: connector is not available (probe first)")
	}
	switch id {
	case "promql.instant":
		var a struct {
			Expr string `json:"expr"`
			Time string `json:"time"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		at := time.Now()
		if a.Time != "" {
			t, err := time.Parse(time.RFC3339, a.Time)
			if err != nil {
				return nil, fmt.Errorf("promql: bad time %q: %w", a.Time, err)
			}
			at = t
		}
		s, err := c.client.Instant(ctx, a.Expr, at)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	case "promql.range":
		var a struct {
			Expr        string `json:"expr"`
			Start       string `json:"start"`
			End         string `json:"end"`
			StepSeconds int    `json:"stepSeconds"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		end := time.Now()
		if a.End != "" {
			t, err := time.Parse(time.RFC3339, a.End)
			if err != nil {
				return nil, err
			}
			end = t
		}
		start := end.Add(-15 * time.Minute)
		if a.Start != "" {
			t, err := time.Parse(time.RFC3339, a.Start)
			if err != nil {
				return nil, err
			}
			start = t
		}
		step := time.Duration(a.StepSeconds) * time.Second
		if step <= 0 {
			step = time.Minute
		}
		s, err := c.client.Range(ctx, a.Expr, start, end, step)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	default:
		return nil, fmt.Errorf("promql: unknown capability %q", id)
	}
}

func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/promql/ -race -v`
Expected: all PASS. Add a compile-time assertion to `promql.go`: `var _ connector.Connector = (*Connector)(nil)`.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/promql/promql.go internal/connector/promql/promql_test.go
git commit -m "feat: promql connector — probe, capabilities, query dispatch"
```

---

## Task 8: Metrics query pack + collector

**Files:**
- Create: `internal/metrics/pack.go`
- Create: `internal/metrics/collect.go`
- Test: `internal/metrics/collect_test.go`

**Interfaces:**
- Consumes: `internal/connector` (the `Connector` interface only), `internal/snapshot`, `encoding/json`, `context`, `time`.
- Produces (package `metrics`):
  - `type Kind int` with `KindInstant Kind = iota; KindRange`.
  - `type Entry struct { Name, Expr string; Kind Kind }`.
  - `func DefaultPack() []Entry` — exactly these five, in this order:
    1. `cpu_saturation` / instant / `max by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]) / (container_spec_cpu_quota{container!="",container!="POD"} / container_spec_cpu_period{container!="",container!="POD"}))`
    2. `mem_saturation` / instant / `max by (namespace, pod, container) (container_memory_working_set_bytes{container!="",container!="POD"} / container_spec_memory_limit_bytes{container!="",container!="POD"} > 0)`
    3. `restart_rate` / instant / `max by (namespace, pod) (rate(kube_pod_container_status_restarts_total[1h]) * 3600)`
    4. `pod_not_ready` / instant / `max by (namespace, pod) (kube_pod_status_ready{condition="true"} == 0)`
    5. `targets_down` / instant / `up == 0`
  - `func Collect(ctx context.Context, c connector.Connector, pack []Entry, at time.Time, backend string) *snapshot.MetricSet` — for each entry: build the args JSON (`{"expr":<expr>,"time":<RFC3339 at>}` for instant; `{"expr":<expr>,"stepSeconds":60}` for range), call `c.Query(ctx, "promql.instant"|"promql.range", args)`, unmarshal the result into `[]snapshot.MetricSample`. On any error (Query error OR unmarshal error), set `MetricResult.Error` to the error string and leave `Samples` empty — never abort. Returns a `*MetricSet` with `Backend: backend`, `CollectedAt: at`, and one `MetricResult` per entry in pack order. Never returns nil (M2: no whole-pack failure mode; the signature has no error).
  - Sort each result's `Samples` deterministically before returning: by the sorted-key-joined label string, then value. (Guards `report.json` byte-stability — Prometheus result order is not guaranteed.)

- [ ] **Step 1: Write the failing test**

`internal/metrics/collect_test.go`:

```go
package metrics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type fakeConn struct {
	byExpr map[string][]snapshot.MetricSample
	fail   map[string]bool
}

func (fakeConn) Name() string { return "promql" }
func (fakeConn) Probe(context.Context) (_ struct{}) { return } // unused; see note
func (f fakeConn) Query(_ context.Context, _ string, args json.RawMessage) (json.RawMessage, error) {
	var a struct{ Expr string `json:"expr"` }
	_ = json.Unmarshal(args, &a)
	if f.fail[a.Expr] {
		return nil, errors.New("boom")
	}
	return json.Marshal(f.byExpr[a.Expr])
}
```

> **Note for the implementer:** `fakeConn` must satisfy `connector.Connector`, which is `Name/Probe/Capabilities/Query`. Implement all four properly (Probe returns `connector.Availability{State: connector.StateAvailable}`, `Capabilities` returns nil). The stub above is abbreviated — write the real 4-method fake.

```go
func TestCollectRunsPackAndToleratesPerQueryFailure(t *testing.T) {
	pack := DefaultPack()
	f := fakeConn{
		byExpr: map[string][]snapshot.MetricSample{
			pack[0].Expr: {{Labels: map[string]string{"pod": "b"}, Value: 0.9}, {Labels: map[string]string{"pod": "a"}, Value: 0.5}},
		},
		fail: map[string]bool{pack[4].Expr: true},
	}
	at := time.Unix(1735732800, 0)
	set := Collect(context.Background(), f, pack, at, "test")

	require.Equal(t, "test", set.Backend)
	require.Equal(t, at, set.CollectedAt)
	require.Len(t, set.Results, 5)

	cpu := set.Result("cpu_saturation")
	require.NoError(t, errFromResult(cpu))
	require.Equal(t, "a", cpu.Samples[0].Labels["pod"]) // sorted by label string

	td := set.Result("targets_down")
	require.NotEmpty(t, td.Error) // per-query failure captured, not fatal
	require.Empty(t, td.Samples)

	nr := set.Result("pod_not_ready")
	require.Empty(t, nr.Error)
	require.Empty(t, nr.Samples) // fakeConn returned nil for this expr → empty, no error
}

func errFromResult(r *snapshot.MetricResult) error {
	if r != nil && r.Error != "" {
		return errors.New(r.Error)
	}
	return nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/metrics/ -v`
Expected: FAIL — `undefined: DefaultPack`.

- [ ] **Step 3: Write the implementation**

`internal/metrics/pack.go`:

```go
package metrics

type Kind int

const (
	KindInstant Kind = iota
	KindRange
)

type Entry struct {
	Name string
	Expr string
	Kind Kind
}

func DefaultPack() []Entry {
	return []Entry{
		{
			Name: "cpu_saturation", Kind: KindInstant,
			Expr: `max by (namespace, pod, container) (rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[5m]) / (container_spec_cpu_quota{container!="",container!="POD"} / container_spec_cpu_period{container!="",container!="POD"}))`,
		},
		{
			Name: "mem_saturation", Kind: KindInstant,
			Expr: `max by (namespace, pod, container) (container_memory_working_set_bytes{container!="",container!="POD"} / container_spec_memory_limit_bytes{container!="",container!="POD"} > 0)`,
		},
		{
			Name: "restart_rate", Kind: KindInstant,
			Expr: `max by (namespace, pod) (rate(kube_pod_container_status_restarts_total[1h]) * 3600)`,
		},
		{
			Name: "pod_not_ready", Kind: KindInstant,
			Expr: `max by (namespace, pod) (kube_pod_status_ready{condition="true"} == 0)`,
		},
		{
			Name: "targets_down", Kind: KindInstant,
			Expr: `up == 0`,
		},
	}
}
```

`internal/metrics/collect.go`:

```go
package metrics

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func Collect(ctx context.Context, c connector.Connector, pack []Entry, at time.Time, backend string) *snapshot.MetricSet {
	set := &snapshot.MetricSet{Backend: backend, CollectedAt: at, Results: make([]snapshot.MetricResult, 0, len(pack))}
	for _, e := range pack {
		res := snapshot.MetricResult{Name: e.Name, Expr: e.Expr}
		capID, args := "promql.instant", mustJSON(map[string]any{"expr": e.Expr, "time": at.Format(time.RFC3339)})
		if e.Kind == KindRange {
			capID, args = "promql.range", mustJSON(map[string]any{"expr": e.Expr, "stepSeconds": 60})
		}
		raw, err := c.Query(ctx, capID, args)
		if err != nil {
			res.Error = err.Error()
			set.Results = append(set.Results, res)
			continue
		}
		var samples []snapshot.MetricSample
		if err := json.Unmarshal(raw, &samples); err != nil {
			res.Error = "decode result: " + err.Error()
			set.Results = append(set.Results, res)
			continue
		}
		sortSamples(samples)
		res.Samples = samples
		set.Results = append(set.Results, res)
	}
	return set
}

func sortSamples(s []snapshot.MetricSample) {
	sort.SliceStable(s, func(i, j int) bool {
		ki, kj := labelKey(s[i].Labels), labelKey(s[j].Labels)
		if ki != kj {
			return ki < kj
		}
		return s[i].Value < s[j].Value
	})
}

func labelKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(m[k])
		b.WriteByte(',')
	}
	return b.String()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/metrics/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/metrics/
git commit -m "feat: metrics — fixed PromQL query pack and tolerant collector"
```

---

## Task 9: The `slo` analyzer

**Files:**
- Create: `internal/analyzer/slo/slo.go`
- Create: `internal/analyzer/slo/rules.go`
- Test: `internal/analyzer/slo/slo_test.go`

**Interfaces:**
- Consumes: `internal/analyzer` (`Finding`, `Severity*`, `ObjectRef`, `Evidence`), `internal/snapshot` (`Snapshot`, `MetricSet`, `MetricSample`), `context`, `fmt`, `sort`.
- Produces (package `slo`):
  - `type Analyzer struct{}`; `func New() *Analyzer`; `var _ analyzer.Analyzer = (*Analyzer)(nil)`.
  - `func (*Analyzer) ID() string` → `"slo"`.
  - `func (*Analyzer) Requires() []string` → `[]string{"promql"}`.
  - `func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error)` — if `snap.Metrics == nil` return `nil, nil` (belt-and-braces; the Requires gate normally prevents this). Otherwise run the rule funcs and concatenate.
  - Rule funcs, `func(m *snapshot.MetricSet) []analyzer.Finding`:
    - `checkCPUSaturation` — result `cpu_saturation`. For each sample with `Value > 0.90`: `slo/cpu-saturation`, **warning**. Object `{Kind:"Pod", Namespace: labels["namespace"], Name: labels["pod"]}`. Summary `container %q in Pod/%s/%s is at %.0f%% of its CPU limit` (container from `labels["container"]`). Evidence `{Source:"promql", Query: <the expr>, Value: {labels, ratio}, At: m.CollectedAt}`.
    - `checkMemSaturation` — result `mem_saturation`. `Value > 0.90` → **warning**; `Value >= 0.98` → **critical**. Same object/evidence shape; summary mentions `% of its memory limit`.
    - `checkRestartRate` — result `restart_rate`. `Value > 1.0` (restarts/hour) → **warning**. Object `{Kind:"Pod", Namespace, Name}` (no container label in this query). Summary `Pod/%s/%s is restarting ~%.1f times/hour`.
    - `checkPodNotReady` — result `pod_not_ready`. `Value >= 1` → **warning**. Summary `Pod/%s/%s has not been Ready`. (This complements `reliability/not-ready`, which is a point-in-time k8s check; note that in the summary: `(sustained, from metrics)`.)
    - `checkTargetsDown` — result `targets_down`. Each sample (`up == 0` only returns down targets) → **warning**, `slo/target-down`. Object `{Kind:"ScrapeTarget", Name: labels["job"] + "/" + labels["instance"]}` (synthetic kind — fine, it has no k8s object). Summary `scrape target %s (job %s) is down`.
    - `checkQueryErrors` — for each `MetricResult` with a non-empty `Error`: one `slo/query-error`, **info**, Object `{Kind:"MetricsBackend", Name: m.Backend}`, Summary `query %q failed: %s`, Evidence `{Source:"promql", Query: result.Expr, Value: result.Error, At: m.CollectedAt}`.
  - Helper `finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, ev ...analyzer.Evidence) analyzer.Finding` — sets `Domain: "slo"`.
  - Helper `ev(query string, value any, at time.Time) analyzer.Evidence` — `Source: "promql"`.
  - Within each rule, iterate `result.Samples` in the order they already are (the collector sorted them) — do NOT re-sort, and do NOT range a map for anything that reaches a Finding.

- [ ] **Step 1: Write the failing test**

`internal/analyzer/slo/slo_test.go`:

```go
package slo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func ids(fs []analyzer.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RuleID
	}
	return out
}

func mset(results ...snapshot.MetricResult) *snapshot.MetricSet {
	return &snapshot.MetricSet{Backend: "test", CollectedAt: time.Unix(1735732800, 0), Results: results}
}

func TestCPUSaturationWarns(t *testing.T) {
	m := mset(snapshot.MetricResult{
		Name: "cpu_saturation", Expr: "rate(...)",
		Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "p", "pod": "api-1", "container": "api"}, Value: 0.95},
			{Labels: map[string]string{"namespace": "p", "pod": "api-2", "container": "api"}, Value: 0.20},
		},
	})
	fs := checkCPUSaturation(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/cpu-saturation", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
	require.Equal(t, "Pod/p/api-1", fs[0].Object.String())
	require.Equal(t, "promql", fs[0].Evidence[0].Source)
}

func TestMemSaturationCriticalAt98(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "mem_saturation", Samples: []snapshot.MetricSample{
		{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.99},
	}})
	fs := checkMemSaturation(m)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
}

func TestTargetsDown(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "targets_down", Samples: []snapshot.MetricSample{
		{Labels: map[string]string{"job": "kubelet", "instance": "10.0.0.1:10250"}, Value: 0},
	}})
	fs := checkTargetsDown(m)
	require.Len(t, fs, 1)
	require.Equal(t, "slo/target-down", fs[0].RuleID)
}

func TestQueryErrorProducesInfoFinding(t *testing.T) {
	m := mset(snapshot.MetricResult{Name: "targets_down", Expr: "up == 0", Error: "boom"})
	fs := checkQueryErrors(m)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityInfo, fs[0].Severity)
}

func TestAnalyzeNilMetricsIsEmpty(t *testing.T) {
	fs, err := New().Analyze(context.Background(), &snapshot.Snapshot{})
	require.NoError(t, err)
	require.Empty(t, fs)
}

func TestAnalyzeRunsAllRules(t *testing.T) {
	snap := &snapshot.Snapshot{Metrics: mset(
		snapshot.MetricResult{Name: "mem_saturation", Samples: []snapshot.MetricSample{
			{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.95},
		}},
	)}
	fs, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	require.Contains(t, ids(fs), "slo/mem-saturation")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/slo/ -v`
Expected: FAIL — `undefined: checkCPUSaturation`.

- [ ] **Step 3: Write the implementation**

`internal/analyzer/slo/slo.go`:

```go
package slo

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

var _ analyzer.Analyzer = (*Analyzer)(nil)

func (*Analyzer) ID() string         { return "slo" }
func (*Analyzer) Requires() []string { return []string{"promql"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	if snap.Metrics == nil {
		return nil, nil
	}
	m := snap.Metrics
	var out []analyzer.Finding
	for _, rule := range []func(*snapshot.MetricSet) []analyzer.Finding{
		checkCPUSaturation,
		checkMemSaturation,
		checkRestartRate,
		checkPodNotReady,
		checkTargetsDown,
		checkQueryErrors,
	} {
		out = append(out, rule(m)...)
	}
	return out, nil
}
```

`internal/analyzer/slo/rules.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/analyzer/slo/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/slo/
git commit -m "feat: slo analyzer — saturation, restart rate, readiness, scrape health"
```

---

## Task 10: The `change` analyzer

**Files:**
- Create: `internal/analyzer/change/change.go`
- Create: `internal/analyzer/change/rules.go`
- Test: `internal/analyzer/change/change_test.go`

**Interfaces:**
- Consumes: `internal/analyzer`, `internal/snapshot`, `k8s.io/api/apps/v1`, `k8s.io/api/core/v1`, `context`, `fmt`, `sort`, `time`.
- Produces (package `change`):
  - `type Analyzer struct{}`; `func New() *Analyzer`; `var _ analyzer.Analyzer = (*Analyzer)(nil)`.
  - `func (*Analyzer) ID() string` → `"change"`.
  - `func (*Analyzer) Requires() []string` → `[]string{"k8s"}`.
  - `func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error)` — run the three rule funcs, concatenate.
  - Rule funcs, `func(snap *snapshot.Snapshot) []analyzer.Finding`:
    - `checkRecentRollout` — `change/recent-rollout`, **info**. For each Deployment/StatefulSet: find its owned ReplicaSets (Deployment) via `ownerReferences` and take the newest by `CreationTimestamp`; if that newest RS was created within `snap.Meta.Lookback` of `snap.Meta.CollectedAt` AND there is more than one RS for the workload (i.e. it is a real rollout, not the first deploy), emit a finding. Summary: `Deployment %s/%s rolled out %s ago (revision %s); %d/%d replicas ready`. Evidence: `{Source:"k8s", Query:"replicasets[owner=<wl>] newest creationTimestamp", Value: {newestRS, ageSeconds, readyReplicas, replicas}, At: CollectedAt}`. For StatefulSets (no RS): use `.Status.CurrentRevision != .Status.UpdateRevision` OR `.Status.ObservedGeneration` change is not observable → for M2, StatefulSet recent-rollout keys off `currentRevision != updateRevision` (an update in progress).
    - `checkRolloutStuck` — `change/rollout-stuck`, **warning**. Deployment/StatefulSet with `.Generation != .Status.ObservedGeneration` (controller has not caught up) — only flag when the workload's `CreationTimestamp` is older than 2×lookback (avoid flagging a just-created workload) OR when a Deployment has a `Progressing` condition with `Status == "False"` and `Reason == "ProgressDeadlineExceeded"` (flag regardless of age). Summary names which condition tripped. Evidence carries generation/observedGeneration or the condition.
    - `checkReplicaSetChurn` — `change/replicaset-churn`, **info**. For a Deployment, count its owned ReplicaSets whose `CreationTimestamp` is within `snap.Meta.Lookback`; if `>= 3`, emit (repeated rollouts / rollbacks in a short window). Summary: `Deployment %s/%s has had %d ReplicaSet revisions in the last %s`.
  - Helpers: `finding(...)` (Domain `"change"`), `ev(...)` (Source `"k8s"`), `deployRef`/`stsRef` (ObjectRef with `APIVersion:"apps/v1"`), `ownedReplicaSets(dep, allRS) []appsv1.ReplicaSet` (match on `ownerReferences[].uid == dep.uid` OR name-prefix + `pod-template-hash` label when uid is absent in fixtures — prefer uid).
  - Determinism: sort owned ReplicaSets by `(CreationTimestamp, Name)` before taking "newest"; iterate workloads in Snapshot slice order.

- [ ] **Step 1: Write the failing test**

`internal/analyzer/change/change_test.go`:

```go
package change

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func i32(v int32) *int32 { return &v }

func TestRecentRolloutFires(t *testing.T) {
	now := time.Now()
	depUID := types.UID("dep-uid")
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", UID: depUID, Generation: 3,
			CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Spec:   appsv1.DeploymentSpec{Replicas: i32(3)},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3, Replicas: 3, ReadyReplicas: 2},
	}
	rsOld := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-old", Namespace: "n", CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour)),
		OwnerReferences: []metav1.OwnerReference{{UID: depUID}},
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "1"}}}
	rsNew := appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "api-new", Namespace: "n", CreationTimestamp: metav1.NewTime(now.Add(-30 * time.Minute)),
		OwnerReferences: []metav1.OwnerReference{{UID: depUID}},
		Annotations:     map[string]string{"deployment.kubernetes.io/revision": "2"}}}

	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Deployments: []appsv1.Deployment{dep},
		ReplicaSets: []appsv1.ReplicaSet{rsOld, rsNew},
	}
	fs := checkRecentRollout(snap)
	require.Len(t, fs, 1)
	require.Equal(t, "change/recent-rollout", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityInfo, fs[0].Severity)
	require.Contains(t, fs[0].Summary, "revision 2")
}

func TestRolloutStuckOnGenerationLag(t *testing.T) {
	now := time.Now()
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", Generation: 5,
			CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 4},
	}
	snap := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour}, Deployments: []appsv1.Deployment{dep}}
	fs := checkRolloutStuck(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
}

func TestReplicaSetChurn(t *testing.T) {
	now := time.Now()
	uid := types.UID("d")
	dep := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n", UID: uid,
		CreationTimestamp: metav1.NewTime(now.Add(-72 * time.Hour))}}
	var rs []appsv1.ReplicaSet
	for i := 0; i < 4; i++ {
		rs = append(rs, appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "api-" + string(rune('a'+i)), Namespace: "n",
			CreationTimestamp: metav1.NewTime(now.Add(-time.Duration(i*10) * time.Minute)),
			OwnerReferences:   []metav1.OwnerReference{{UID: uid}}}})
	}
	snap := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Deployments: []appsv1.Deployment{dep}, ReplicaSets: rs}
	fs := checkReplicaSetChurn(snap)
	require.Len(t, fs, 1)
	require.Contains(t, fs[0].Summary, "4 ReplicaSet revisions")
}

func TestAnalyzeRunsAllRules(t *testing.T) {
	fs, err := New().Analyze(context.Background(), &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: time.Hour}})
	require.NoError(t, err)
	require.Empty(t, fs) // nothing to report on an empty snapshot
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/change/ -v`
Expected: FAIL — `undefined: checkRecentRollout`.

- [ ] **Step 3: Write the implementation**

`internal/analyzer/change/change.go`:

```go
package change

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

var _ analyzer.Analyzer = (*Analyzer)(nil)

func (*Analyzer) ID() string         { return "change" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	var out []analyzer.Finding
	for _, rule := range []func(*snapshot.Snapshot) []analyzer.Finding{
		checkRecentRollout,
		checkRolloutStuck,
		checkReplicaSetChurn,
	} {
		out = append(out, rule(snap)...)
	}
	return out, nil
}
```

`internal/analyzer/change/rules.go`:

```go
package change

import (
	"fmt"
	"sort"
	"time"

	appsv1 "k8s.io/api/apps/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

const churnThreshold = 3

func ev(query string, value any, at time.Time) analyzer.Evidence {
	return analyzer.Evidence{Source: "k8s", Query: query, Value: value, At: at}
}

func finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, e ...analyzer.Evidence) analyzer.Finding {
	return analyzer.Finding{RuleID: ruleID, Domain: "change", Severity: sev, Title: title, Object: obj, Summary: summary, Evidence: e}
}

func deployRef(d appsv1.Deployment) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: d.Namespace, Name: d.Name}
}

func stsRef(s appsv1.StatefulSet) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "apps/v1", Kind: "StatefulSet", Namespace: s.Namespace, Name: s.Name}
}

func ownedReplicaSets(d appsv1.Deployment, all []appsv1.ReplicaSet) []appsv1.ReplicaSet {
	var out []appsv1.ReplicaSet
	for _, rs := range all {
		if rs.Namespace != d.Namespace {
			continue
		}
		for _, o := range rs.OwnerReferences {
			if o.UID == d.UID && d.UID != "" {
				out = append(out, rs)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].CreationTimestamp.Time, out[j].CreationTimestamp.Time
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func checkRecentRollout(snap *snapshot.Snapshot) []analyzer.Finding {
	cutoff := snap.Meta.CollectedAt.Add(-snap.Meta.Lookback)
	var out []analyzer.Finding

	for _, d := range snap.Deployments {
		rss := ownedReplicaSets(d, snap.ReplicaSets)
		if len(rss) < 2 {
			continue
		}
		newest := rss[len(rss)-1]
		if newest.CreationTimestamp.Time.Before(cutoff) {
			continue
		}
		age := snap.Meta.CollectedAt.Sub(newest.CreationTimestamp.Time)
		rev := newest.Annotations["deployment.kubernetes.io/revision"]
		out = append(out, finding("change/recent-rollout", analyzer.SeverityInfo, deployRef(d),
			"Workload rolled out recently",
			fmt.Sprintf("Deployment %s/%s rolled out %s ago (revision %s); %d/%d replicas ready",
				d.Namespace, d.Name, age.Round(time.Minute), rev, d.Status.ReadyReplicas, d.Status.Replicas),
			ev("replicasets[owner=Deployment] newest creationTimestamp",
				map[string]any{"replicaSet": newest.Name, "revision": rev, "ageSeconds": int(age.Seconds()),
					"readyReplicas": d.Status.ReadyReplicas, "replicas": d.Status.Replicas},
				snap.Meta.CollectedAt),
		))
	}

	for _, s := range snap.StatefulSets {
		if s.Status.CurrentRevision == "" || s.Status.UpdateRevision == "" || s.Status.CurrentRevision == s.Status.UpdateRevision {
			continue
		}
		out = append(out, finding("change/recent-rollout", analyzer.SeverityInfo, stsRef(s),
			"Workload rolled out recently",
			fmt.Sprintf("StatefulSet %s/%s is mid-update (current %s → update %s); %d/%d replicas ready",
				s.Namespace, s.Name, short(s.Status.CurrentRevision), short(s.Status.UpdateRevision),
				s.Status.ReadyReplicas, s.Status.Replicas),
			ev("statefulset.status.{currentRevision,updateRevision}",
				map[string]any{"currentRevision": s.Status.CurrentRevision, "updateRevision": s.Status.UpdateRevision},
				snap.Meta.CollectedAt),
		))
	}
	return out
}

func deploymentStuck(d appsv1.Deployment, collectedAt time.Time, stale time.Duration) (analyzer.Finding, bool) {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentProgressing && string(c.Status) == "False" && c.Reason == "ProgressDeadlineExceeded" {
			return finding("change/rollout-stuck", analyzer.SeverityWarning, deployRef(d),
				"Rollout stuck",
				fmt.Sprintf("Deployment %s/%s: %s", d.Namespace, d.Name, c.Message),
				ev("deployment.status.conditions[type=Progressing]",
					map[string]any{"reason": c.Reason, "message": c.Message}, collectedAt)), true
		}
	}
	aged := collectedAt.Sub(d.CreationTimestamp.Time) > stale
	if aged && d.Generation != d.Status.ObservedGeneration {
		return finding("change/rollout-stuck", analyzer.SeverityWarning, deployRef(d),
			"Rollout stuck",
			fmt.Sprintf("Deployment %s/%s: spec generation %d not yet observed (controller at %d)",
				d.Namespace, d.Name, d.Generation, d.Status.ObservedGeneration),
			ev("deployment.{generation,status.observedGeneration}",
				map[string]any{"generation": d.Generation, "observedGeneration": d.Status.ObservedGeneration},
				collectedAt)), true
	}
	return analyzer.Finding{}, false
}

func checkRolloutStuck(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	stale := 2 * snap.Meta.Lookback

	for _, d := range snap.Deployments {
		if f, ok := deploymentStuck(d, snap.Meta.CollectedAt, stale); ok {
			out = append(out, f)
		}
	}

	for _, s := range snap.StatefulSets {
		if snap.Meta.CollectedAt.Sub(s.CreationTimestamp.Time) > stale && s.Generation != s.Status.ObservedGeneration {
			out = append(out, finding("change/rollout-stuck", analyzer.SeverityWarning, stsRef(s),
				"Rollout stuck",
				fmt.Sprintf("StatefulSet %s/%s: spec generation %d not yet observed (controller at %d)",
					s.Namespace, s.Name, s.Generation, s.Status.ObservedGeneration),
				ev("statefulset.{generation,status.observedGeneration}",
					map[string]any{"generation": s.Generation, "observedGeneration": s.Status.ObservedGeneration},
					snap.Meta.CollectedAt)))
		}
	}
	return out
}

func checkReplicaSetChurn(snap *snapshot.Snapshot) []analyzer.Finding {
	cutoff := snap.Meta.CollectedAt.Add(-snap.Meta.Lookback)
	var out []analyzer.Finding
	for _, d := range snap.Deployments {
		n := 0
		for _, rs := range ownedReplicaSets(d, snap.ReplicaSets) {
			if !rs.CreationTimestamp.Time.Before(cutoff) {
				n++
			}
		}
		if n >= churnThreshold {
			out = append(out, finding("change/replicaset-churn", analyzer.SeverityInfo, deployRef(d),
				"Repeated rollouts",
				fmt.Sprintf("Deployment %s/%s has had %d ReplicaSet revisions in the last %s",
					d.Namespace, d.Name, n, snap.Meta.Lookback),
				ev("replicasets[owner=Deployment] within lookback", map[string]any{"count": n}, snap.Meta.CollectedAt)))
		}
	}
	return out
}

func short(s string) string {
	if len(s) > 12 {
		return s[len(s)-12:]
	}
	return s
}
```

> **Behaviour:** `deploymentStuck` returns at most one finding per Deployment — the `ProgressDeadlineExceeded` condition wins over the generation-lag check. The generation-lag check only fires for workloads older than `2 × lookback` (so a just-created Deployment mid-first-rollout is not flagged).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/analyzer/change/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/change/
git commit -m "feat: change analyzer — recent rollout, stuck rollout, replicaset churn"
```

---

## Task 11: Orchestrator wiring

**Files:**
- Modify: `internal/orchestrator/orchestrator.go`
- Test: `internal/orchestrator/orchestrator_test.go` (add cases)

**Interfaces:**
- Consumes: `internal/connector/promql`, `internal/metrics`, `internal/analyzer/change`, `internal/analyzer/slo`.
- Behaviour changes in `Run`:
  1. **Register promql** after registering the k8s source, unless `cfg.Connectors.PromQL.URL == "disabled"`:
     ```go
     if cfg.Connectors.PromQL.URL != "disabled" {
         reg.Register(promql.New(promql.Options{
             URL:        cfg.Connectors.PromQL.URL,
             Clientset:  src.Clientset(),
             Namespaces: cfg.Scope.Namespaces,
         }))
     }
     ```
     (When `URL` is empty — shouldn't happen, config defaults it to `"auto"` — treat as `"auto"`.)
  2. `reg.Probe(ctx)` already probes every registered connector; the k8s-fatal check is unchanged. promql being `absent`/`degraded` is non-fatal.
  3. **After `src.Collect(ctx)`**, if promql is available, run the pack into the Snapshot:
     ```go
     if pc, ok := reg.Get("promql"); ok && reg.Satisfied([]string{"promql"}) {
         backend := "prometheus"
         if b, ok := pc.(interface{ Backend() string }); ok && b.Backend() != "" {
             backend = b.Backend()
         }
         snap.Metrics = metrics.Collect(ctx, pc, metrics.DefaultPack(), snap.Meta.CollectedAt, backend)
     }
     ```
     `snap.Meta.CollectedAt` is the k8s-collection timestamp; reuse it so JSON is stable and the two data sets share a "collected at".
  4. **Analyzer list** becomes `[]analyzer.Analyzer{reliability.New(), change.New(), slo.New()}` — fixed order (reliability, change, slo). The existing skipped-finding loop is unchanged and now genuinely fires for `slo` when promql is not available.
  5. Everything else (`report.Meta`, `Build`, exit code) unchanged.

- [ ] **Step 1: Write the failing tests**

Add to `internal/orchestrator/orchestrator_test.go`. You need a fake promql connector usable as `connector.Connector`. Put this in the test file:

```go
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
	var a struct{ Expr string `json:"expr"` }
	_ = json.Unmarshal(args, &a)
	return json.Marshal(f.samples[a.Expr])
}
func (fakePromql) Backend() string { return "fake" }
```

But `Run` builds the promql connector itself from config — it does not accept an injected one. Two options, pick the smaller:
- **(a)** Add `Promql connector.Connector` to `orchestrator.Options` (nil ⇒ build from config), mirroring the existing `K8s` seam. Wire `Run` to use `opts.Promql` when non-nil.
- **(b)** Drive the test through config: set `cfg.Connectors.PromQL.URL` to an `httptest` server URL serving canned Prometheus JSON, and let `Run` build the real promql connector.

**Choose (a)** — it matches the `K8s` seam already in `Options`, keeps the test hermetic (no httptest), and the injection point is one `if`. Update `Options`:

```go
type Options struct {
	Config  *config.Config
	Version string
	K8s     K8sSource
	Promql  connector.Connector // nil => built from Config.Connectors.PromQL
}
```

Tests:

```go
func TestRunCollectsMetricsAndRunsSLO(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "p"}},
	)
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})

	pack := metrics.DefaultPack()
	fp := fakePromql{state: connector.StateAvailable, samples: map[string][]snapshot.MetricSample{
		pack[1].Expr: {{Labels: map[string]string{"namespace": "p", "pod": "x", "container": "c"}, Value: 0.99}},
	}}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: src, Promql: fp})
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

func TestRunSkipsSLOWhenPromqlAbsent(t *testing.T) {
	cs := fake.NewSimpleClientset()
	src := k8s.NewWithClient(cs, "ctx", k8s.Scope{Lookback: time.Hour})
	fp := fakePromql{state: connector.StateAbsent}

	res, err := Run(context.Background(), Options{Config: testConfig(), Version: "t", K8s: src, Promql: fp})
	require.NoError(t, err)

	var skipped bool
	for _, f := range res.Report.Findings {
		if f.RuleID == "slo/skipped" {
			skipped = true
		}
	}
	require.True(t, skipped)
}
```

(`testConfig()` already exists in the M1 orchestrator test; it returns `config.Default()` with `LLM.Provider = "none"`. `config.Default()` sets `Connectors.PromQL.URL = "auto"` — fine, the injected `Promql` overrides the build path.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrator/ -run 'TestRunCollectsMetrics|TestRunSkipsSLO' -v`
Expected: FAIL — `Options` has no `Promql` field / `slo` not wired.

- [ ] **Step 3: Write the implementation**

Apply the `Options.Promql` field and, in `Run`:

```go
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
			pc = promql.New(promql.Options{URL: url, Clientset: src.Clientset(), Namespaces: cfg.Scope.Namespaces})
		}
		reg.Register(pc)
	}
	statuses := reg.Probe(ctx)
	// ...k8s-fatal check unchanged...

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
	// ...loop unchanged...
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -race -count=1`
Expected: all PASS, including the M1 orchestrator tests (unchanged behaviour when `Promql` is `absent`/not registered).

- [ ] **Step 5: Commit**

```bash
git add internal/orchestrator/
git commit -m "feat: orchestrator — register promql, collect metrics, run change + slo analyzers"
```

---

## Task 12: Report fixes, byte-stability, docs, demo

**Files:**
- Modify: `internal/report/markdown.go`, `internal/report/report.go`, `internal/report/determinism_test.go`
- Modify (regenerate if needed): `internal/report/testdata/golden_basic.md`
- Modify: `poirot.example.yaml`, `README.md`, `hack/demo-broken.yaml`
- Test: `internal/report/report_test.go` (add `meta.Namespaces` case), `internal/report/markdown_test.go` (add empty-Evidence + all-skipped cases)

**Interfaces:** no new exported surface. Behaviour fixes only.

- [ ] **Step 1: Write the failing tests**

Add to `internal/report/report_test.go`:

```go
func TestBuildNormalisesNilNamespaces(t *testing.T) {
	b, err := Build(Meta{Version: "v"}, nil, nil).JSON()
	require.NoError(t, err)
	require.Contains(t, string(b), `"namespaces": []`)
	require.NotContains(t, string(b), `"namespaces": null`)
}
```

Add to `internal/report/markdown_test.go`:

```go
func TestMarkdownEmptyEvidenceNoDanglingHeader(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"}, nil, []analyzer.Finding{{
		RuleID: "x/y", Domain: "x", Severity: analyzer.SeverityWarning, Title: "T",
		Object: analyzer.ObjectRef{Kind: "Pod", Name: "p"}, Summary: "s", // no Evidence
	}})
	md, err := r.Markdown()
	require.NoError(t, err)
	require.NotContains(t, string(md), "Evidence:\n\n") // no header with nothing under it
}

func TestMarkdownAllSkippedIsNotACleanBill(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"}, nil, []analyzer.Finding{{
		RuleID: "slo/skipped", Domain: "slo", Severity: analyzer.SeverityInfo,
		Title: "Analyzer skipped", Summary: "slo analysis skipped — missing connector(s): promql",
	}})
	md, err := r.Markdown()
	require.NoError(t, err)
	require.NotContains(t, string(md), "No findings. ✅")
	require.Contains(t, string(md), "slo analysis skipped")
}

func TestMarkdownEscapesPipeInConnectorDetail(t *testing.T) {
	r := Build(Meta{Version: "v", Context: "c", Lookback: "24h"},
		[]connector.Status{{Name: "promql", Availability: connector.Availability{
			State: connector.StateDegraded, Reason: "unreachable", Detail: "dial a|b failed"}}}, nil)
	md, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(md), `dial a\|b failed`)
}
```


Strengthen `internal/report/determinism_test.go`: change the existing fixture so at least one finding's `Evidence[0].Value` is a `map[string]any` built by ranging a Go map with ≥4 keys, and one finding is emitted per iteration by ranging a fresh `map[string]bool` into a slice **without sorting** — then assert 50× byte-identical JSON+MD. If `Build`/`Markdown` are correctly deterministic (they marshal maps with sorted keys, and every finding-bound slice is sorted upstream), this passes; if a regression reintroduces map-order in report rendering, it fails. Document in a comment that the *upstream* slice sorting (metrics collector, change analyzer) is what this leans on.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/report/ -run 'NilNamespaces|EmptyEvidence|AllSkipped|EscapesPipe' -v`
Expected: FAIL on all four.

- [ ] **Step 3: Apply the fixes**

`internal/report/report.go` — in `Build`, after the `meta.Tool` default:

```go
	if meta.Namespaces == nil {
		meta.Namespaces = []string{}
	}
```

`internal/report/markdown.go`:

- **Escape pipes** in the connector-row prep, when building `detail`:
  ```go
	detail := strings.ReplaceAll(strings.Join(parts, " — "), "|", `\|`)
	if detail == "" {
		detail = "-"
	}
  ```
- **Empty-Evidence guard** — wrap the `Evidence:` header and its range in `{{if .Evidence}}`:
  ```
	{{.Summary}}
	{{if .Evidence}}
	Evidence:
	{{- range .Evidence}}
	- {{evline .}}
	{{- end}}
	{{end}}
  ```
- **All-skipped message** — add a field to `mdData` and adjust:
  ```go
	data := mdData{
		Meta:       r.Meta,
		Connectors: rows,
		Domains:    domains,
		HasContent: len(domains) > 0,
		Skipped:    skipped,
	}
  ```
  In the template, replace the `{{if not .HasContent}}\nNo findings. ✅\n{{else}}...` head with:
  ```
	{{if not .HasContent}}{{if .Skipped}}
	No findings from the checks that ran.
	{{else}}
	No findings. ✅
	{{end}}{{else}}{{range .Domains}}
  ```
  Keep the rest of the block and the `## Checks skipped` section as-is.

Regenerate the golden if template whitespace shifted: `UPDATE_GOLDEN=1 go test ./internal/report/ -run TestMarkdownGolden`, then eyeball `internal/report/testdata/golden_basic.md` (the M1 golden has one finding *with* evidence and no skipped checks, so it should be unchanged — if the diff is non-empty, inspect it line by line before committing).

- [ ] **Step 4: Update docs + demo**

`poirot.example.yaml` — uncomment the `connectors:` block, and make `promql` real:

```yaml
connectors:
  # Metrics backend for the slo analyzer (M2). One of:
  #   auto      — discover a Prometheus/VictoriaMetrics/Thanos/Mimir Service and
  #               reach it through the Kubernetes API-server proxy (needs RBAC:
  #               get on services/proxy in the backend's namespace)
  #   http://…  — an explicit URL you can already reach (e.g. a kubectl port-forward)
  #   disabled  — skip metrics entirely; slo analyzer reports as skipped
  promql: { url: auto }
  # opencost / alertmanager / gitops arrive in later milestones (parsed, unused now).
  # opencost:     { url: auto, estimateFallback: true }
  # alertmanager: { url: auto }
  # gitops:       { mode: auto }
```

`README.md` — bump the status line and add one sentence:

```markdown
Status: M2 — reliability + slo (metrics golden signals, scrape health) + change (rollout risk). No LLM.

The `slo` analyzer needs a PromQL-compatible backend (Prometheus, VictoriaMetrics,
Thanos, Mimir). Set `connectors.promql.url` to `auto` (discovered + reached via the
API-server proxy), an explicit URL, or `disabled`.
```

`hack/demo-broken.yaml` — add one workload that saturates CPU against a low limit, so the `slo` rules fire when a metrics backend is present:

```yaml
---
# CPU saturation — busy-loops against a 50m limit → slo/cpu-saturation
apiVersion: apps/v1
kind: Deployment
metadata: { name: cpuhog, namespace: poirot-demo }
spec:
  replicas: 1
  selector: { matchLabels: { app: cpuhog } }
  template:
    metadata: { labels: { app: cpuhog } }
    spec:
      containers:
        - name: hog
          image: busybox:1.36
          command: ["sh", "-c", "while true; do :; done"]
          resources:
            requests: { cpu: 25m, memory: 16Mi }
            limits:   { cpu: 50m, memory: 32Mi }
```

- [ ] **Step 5: Run the full suite + manual e2e**

Run: `go test ./... -race -count=1 && go vet ./... && gofmt -l . && go build -tags tools ./...`
Expected: all green, `gofmt -l` empty.

Manual e2e (needs a local cluster with a metrics backend — the M1 README's colima path plus `kube-prometheus-stack` or k3s's bundled metrics is enough for `container_*` series; full pack needs kube-state-metrics):

```bash
# with kube-prometheus-stack (has prometheus + kube-state-metrics):
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm install kps prometheus-community/kube-prometheus-stack -n monitoring --create-namespace --wait
kubectl apply -f hack/demo-broken.yaml && sleep 120
cp poirot.example.yaml poirot.yaml    # url: auto
./bin/poirot run -c poirot.yaml
grep -E 'slo/|change/' poirot-out/report.md   # expect slo/cpu-saturation, maybe change/* if you kubectl rollout restart something
./bin/poirot run -c poirot.yaml && cp poirot-out/report.json /tmp/a.json
./bin/poirot run -c poirot.yaml && diff /tmp/a.json poirot-out/report.json && echo "byte-stable ✓"
```

Also confirm the degrade path: set `connectors.promql.url: disabled` in `poirot.yaml`, re-run, and check `report.md` shows `slo analysis skipped` under "Checks skipped" and still lists reliability/change findings.

- [ ] **Step 6: Commit**

```bash
git add internal/report/ poirot.example.yaml README.md hack/demo-broken.yaml
git commit -m "fix: report empty-evidence/all-skipped/pipe-escape + namespaces normalisation; docs+demo for M2"
```

---

## Self-Review

**1. Spec coverage (M2 slice):**

| Spec item | Task |
|---|---|
| PromQL connector: `auto` (Prom/VM/Thanos/Mimir) + explicit URL + `disabled`; degrades to kubeconfig-only | Tasks 3–7, 11 |
| `auto` works out-of-cluster via API-server proxy (the design decision confirmed for M2) | Tasks 5, 6, 7 |
| First real use of `Connector.Capabilities()` / `Query()` with JSON-Schema args | Task 7 |
| `slo` analyzer: golden signals (CPU/mem saturation), restart rate, readiness, `up == 0` scrape health; degrades gracefully when a series/KSM is absent | Tasks 8, 9 (a missing series ⇒ empty `Samples` ⇒ no finding; a query error ⇒ `slo/query-error` info) |
| `change` analyzer: workloads changed in lookback + health; stuck (`generation != observedGeneration`, `Progressing` stalled); ReplicaSet churn | Task 10 |
| Analyzers stay pure over an immutable Snapshot (metrics collected into `Snapshot.Metrics` in the collect phase) | Tasks 1, 8, 11 |
| `slo.Requires() == ["promql"]` ⇒ M1's `<id>/skipped` info-finding path finally exercised for real | Tasks 9, 11 (`TestRunSkipsSLOWhenPromqlAbsent`) |
| Config `connectors.promql.url` (already parsed in M1) now consumed | Task 11 |
| Byte-stable `report.json` with metric label sets / reason lists in play | Task 8 (`sortSamples`), Task 12 (determinism test) |

**Carry-forward from the M1 final review (recorded in memory `poirot-project`) addressed here:**
- Markdown `Evidence:` dangles on empty evidence → Task 12.
- `No findings. ✅` prints when every finding is `/skipped` → Task 12 (this now genuinely happens: healthy cluster + `promql: disabled` ⇒ only `slo/skipped`).
- Unescaped `|` in connector Detail cell → Task 12 (promql can now be `degraded` with an error string in Detail).
- `report.Build` leaves `Meta.Namespaces` nil → `"namespaces": null` → Task 12.
- `TestBuildOutputIsByteStable` fixture too weak → Task 12.
- Orchestrator skipped-analyzer branch untested → Task 11.

**Explicitly NOT in M2** (deferred, tracked in memory): ArgoCD/Flux readers for `change` (M5); OpenCost/cost (M4); LLM/agentic investigation (M3); app-level RED metrics; full range-series retention; the M1 `reliability` rule-quality nits (matchExpressions PDBs, `no-limits` `&&` vs `||`, no-Ready-condition nodes, per-event probe findings).

**2. Placeholder scan:** No `TBD`/`TODO`/"handle edge cases". Two spots hand the implementer a real code block plus an explicit judgement call (the `goto`/label in `checkRolloutStuck` with a named refactor alternative; the fake-clientset ProxyGet caveat in Task 6 with exactly what to test instead) — concrete instructions, not placeholders. One deliberate test-name typo is flagged with its correction inline.

**3. Type consistency:** `snapshot.MetricSet`/`MetricResult`/`MetricSample` defined in Task 1, consumed unchanged in Tasks 8 (collector fills them), 9 (`slo` reads them), 11 (orchestrator sets `snap.Metrics`). `promql.doer` defined in Task 4, consumed in Tasks 6 (`proxyDoer` returns it) and 7 (`newClient(d)`). `promql.Target` defined in Task 5, consumed in Task 6 (`proxyDoer(cs, t)`) and Task 7 (`Discover` → `proxyDoer`). `promql.Options` (Task 7) consumed by orchestrator (Task 11) with fields `URL`/`Clientset`/`Namespaces` — matches. `orchestrator.K8sSource` gains `Clientset()` in Task 2, used in Task 11 (`src.Clientset()`). `orchestrator.Options` gains `Promql connector.Connector` in Task 11. `analyzer.Analyzer` satisfied by `*slo.Analyzer` (Task 9) and `*change.Analyzer` (Task 10) — both have `ID() string`, `Requires() []string`, `Analyze(ctx, *snapshot.Snapshot) ([]analyzer.Finding, error)`, matching `internal/analyzer/finding.go` on `main`. `metrics.Collect` signature (Task 8) called identically in Task 11. `connector.Connector` (Name/Probe/Capabilities/Query) satisfied by `*promql.Connector` (Task 7, with the `var _` assertion) and by the test fakes.

**4. Determinism audit:** new map→slice conversions that could reach a `Finding` or the report — (a) `metrics.Collect` sorts every result's `Samples` by label-key string then value (Task 8); (b) `slo` rules iterate the already-sorted `Samples` and never range a map into a slice (Task 9); (c) `change` sorts `ownedReplicaSets` by `(CreationTimestamp, Name)` (Task 10); (d) evidence `Value` maps are marshalled by `encoding/json`, which sorts keys; (e) `report.Build`'s finding sort and `markdown.go`'s domain sort are unchanged from M1. Task 12's strengthened determinism test covers the pipeline.

---

## After M2

Write the M3 plan: **LLM abstraction + Anthropic provider + bounded agentic investigation loop + cross-finding correlation + synthesis** (`docs/superpowers/plans/<date>-poirot-m3-agent.md`). M3 is the first task to use `Connector.Capabilities()` as *tools* (the `promql` connector built here is the first tool provider) via an in-process `ToolProvider`, and the first to add the `investigate` phase between `analyze` and `synthesize`.
