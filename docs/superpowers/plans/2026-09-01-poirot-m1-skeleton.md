# poirot M1 (Skeleton) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship `poirot run` — a single Go binary that reads a kubeconfig + small YAML config, collects Kubernetes state, runs deterministic reliability checks, and writes `report.json` + `report.md`. No LLM in M1.

**Architecture:** One binary, layered: `cmd/poirot` (Cobra CLI) → `orchestrator` (discover → collect → analyze → synthesize) → `connector` (interface + registry, with a `k8s` implementation) + `analyzer` (pure `Snapshot → []Finding` rules) + `report` (JSON canonical, Markdown rendered from it). The immutable `snapshot.Snapshot` is the hand-off between collection and analysis. Interfaces are shaped now so later milestones add connectors, an LLM agent, and an MCP server without reshaping them.

**Tech Stack:** Go 1.23, `github.com/spf13/cobra` (CLI), `k8s.io/client-go` + `k8s.io/api` + `k8s.io/apimachinery` (cluster access, `fake` clientset for tests), `sigs.k8s.io/yaml` (config), `github.com/stretchr/testify` (assertions).

**Spec:** `docs/superpowers/specs/2026-09-01-poirot-sre-assessment-agent-design.md`

## Global Constraints

- Module path: `github.com/init-kaushal/poirot`.
- Go version floor: `1.23` (set `go 1.23` in `go.mod`).
- Binary name: `poirot`. Built to `bin/poirot`.
- Read-only: no code path may create, update, patch, or delete a cluster resource. Only `list`/`get`/`watch`-style calls.
- Every connector query is `(capabilityID string, args json.RawMessage) → (json.RawMessage, error)`. Connector packages import only their own client libraries and the `connector` + `snapshot` packages — never `analyzer`, `orchestrator`, `agent`, or `report`.
- Domain values are lowercase strings: severity `critical|warning|info`; connector state `available|degraded|absent`; `failOn` `none|warning|critical`.
- Every `Finding` carries at least one `Evidence` whose `Query` field is the exact selector/expression used, for reproducibility.
- Exit-code disposition: `worst = 2` if any critical finding, `1` if any warning, else `0`. `failOn=none` → always exit `0`. `failOn=warning` → exit `worst`. `failOn=critical` → exit `2` if `worst==2`, else `0`.
- All tests run with `go test ./... -race -count=1` and must pass before every commit.
- Commit messages: `<type>: <summary>` (`feat`, `test`, `chore`, `refactor`, `docs`). End the body with:
  `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`

---

## File Structure

| Path | Responsibility |
|---|---|
| `go.mod`, `go.sum` | Module + pinned deps |
| `Makefile` | `build`, `test`, `lint`, `tidy` targets |
| `.github/workflows/ci.yml` | build + vet + `go test -race` on push/PR |
| `.gitignore` | `/bin/`, `/poirot-out/`, `*.test` |
| `README.md` | One-paragraph description + quickstart stub |
| `cmd/poirot/main.go` | `main()` — build root cmd, map `ExitError` → process exit code |
| `cmd/poirot/root.go` | `newRootCmd(version string) *cobra.Command` — wires subcommands |
| `cmd/poirot/version.go` | `version` subcommand |
| `cmd/poirot/run.go` | `run` subcommand — load config, call orchestrator, write outputs |
| `cmd/poirot/exit.go` | `ExitError{Code int}` type |
| `internal/config/config.go` | `Config` struct tree, `Duration`, `Default()`, `Load(path)`, `Validate()` |
| `internal/snapshot/snapshot.go` | `Snapshot` (raw k8s objects) + `Meta` |
| `internal/analyzer/finding.go` | `Severity`, `ObjectRef`, `Evidence`, `Analysis`, `Finding`, `Analyzer` interface |
| `internal/connector/registry.go` | `Availability`, `Capability`, `Connector` iface, `Status`, `Registry` |
| `internal/connector/k8s/k8s.go` | REST-config build, client, `Name()`, `Probe()`, stub `Capabilities()`/`Query()` |
| `internal/connector/k8s/collect.go` | `Collect(ctx) (*snapshot.Snapshot, error)` |
| `internal/analyzer/reliability/reliability.go` | `Analyzer` impl: `ID()`, `Requires()`, `Analyze()` calling each rule |
| `internal/analyzer/reliability/pods.go` | pod-level rule functions |
| `internal/analyzer/reliability/workloads.go` | workload/node/PVC/HPA/event rule functions |
| `internal/report/report.go` | `Report`, `Meta`, `Counts`, `Build()`, `JSON()`, `ExitCode()` |
| `internal/report/markdown.go` | `Markdown()` + embedded `text/template` |
| `internal/orchestrator/orchestrator.go` | `Options`, `Result`, `Run(ctx, Options)` — the 4 active phases |

Test files sit beside their targets (`*_test.go`). Shared test fixtures for analyzer rules live in `internal/analyzer/reliability/testdata/`.

---

## Task 1: Project scaffold

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `README.md`, `.github/workflows/ci.yml`
- Create: `cmd/poirot/main.go`, `cmd/poirot/root.go`, `cmd/poirot/version.go`, `cmd/poirot/exit.go`
- Test: `cmd/poirot/root_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `newRootCmd(version string) *cobra.Command` (package `main`) — root command with `version` subcommand attached, `SilenceErrors=true`, `SilenceUsage=true`.
  - `ExitError struct { Code int }` with `func (e *ExitError) Error() string` returning `fmt.Sprintf("exit code %d", e.Code)` (package `main`).

- [ ] **Step 1: Initialize the module and dependencies**

```bash
cd /Users/kaushal/Projects/poirot
go mod init github.com/init-kaushal/poirot
go get github.com/spf13/cobra@latest
go get github.com/stretchr/testify@latest
go get sigs.k8s.io/yaml@latest
go get k8s.io/client-go@latest k8s.io/api@latest k8s.io/apimachinery@latest
```

Then set the Go floor: ensure `go.mod` line reads `go 1.23` (edit if `go mod init` wrote a newer patch form like `go 1.23.0`; keep it as `go 1.23`).

- [ ] **Step 2: Write the failing test**

`cmd/poirot/root_test.go`:

```go
package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersionCommandPrintsVersion(t *testing.T) {
	cmd := newRootCmd("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version"})

	require.NoError(t, cmd.Execute())
	require.Equal(t, "1.2.3\n", out.String())
}

func TestExitErrorMessage(t *testing.T) {
	err := &ExitError{Code: 2}
	require.EqualError(t, err, "exit code 2")
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./cmd/poirot/ -run 'TestVersionCommandPrintsVersion|TestExitErrorMessage' -v`
Expected: FAIL — `undefined: newRootCmd`, `undefined: ExitError`.

- [ ] **Step 4: Write the scaffold files**

`cmd/poirot/exit.go`:

```go
package main

import "fmt"

// ExitError carries a desired process exit code up to main().
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit code %d", e.Code) }
```

`cmd/poirot/root.go`:

```go
package main

import "github.com/spf13/cobra"

func newRootCmd(version string) *cobra.Command {
	root := &cobra.Command{
		Use:           "poirot",
		Short:         "Kubernetes SRE assessment agent — point-in-time reliability/cost/change report",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(newVersionCmd(version))
	return root
}
```

`cmd/poirot/version.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newVersionCmd(version string) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the poirot version",
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	}
}
```

`cmd/poirot/main.go`:

```go
package main

import (
	"errors"
	"fmt"
	"os"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	root := newRootCmd(version)
	if err := root.Execute(); err != nil {
		var ee *ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.Code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Add Makefile, CI, gitignore, README**

`Makefile`:

```makefile
BINARY  := poirot
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint tidy
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/poirot
test:
	go test ./... -race -count=1
lint:
	go vet ./...
tidy:
	go mod tidy
```

`.github/workflows/ci.yml`:

```yaml
name: ci
on: [push, pull_request]
jobs:
  build-test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.23'
      - run: go build ./...
      - run: go vet ./...
      - run: go test ./... -race -count=1
```

`.gitignore`:

```
/bin/
/poirot-out/
*.test
```

`README.md`:

```markdown
# poirot

Point-in-time reliability, cost, and change-risk assessment report for a Kubernetes cluster.
Give it a kubeconfig and a small `poirot.yaml`; run `poirot run`; read `report.md`.

## Quickstart

```bash
make build
./bin/poirot run -c poirot.yaml
```

Status: M1 — reliability report only, no LLM.
```

- [ ] **Step 6: Run tests and build to verify they pass**

Run: `go test ./cmd/poirot/ -v && make build && ./bin/poirot version`
Expected: tests PASS; `./bin/poirot version` prints a version string.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "chore: project scaffold — cobra CLI, version command, Makefile, CI"
```

---

## Task 2: Config package

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`, `internal/config/testdata/minimal.yaml`, `internal/config/testdata/full.yaml`, `internal/config/testdata/bad-provider.yaml`

**Interfaces:**
- Consumes: nothing.
- Produces (package `config`):
  - `type Duration time.Duration` with `UnmarshalJSON`/`MarshalJSON` handling Go duration strings (`"24h"`).
  - `type Config struct { Cluster Cluster; Scope Scope; Connectors Connectors; Focus []string; LLM LLM; Output Output }`
  - `type Cluster struct { Kubeconfig string; Context string }`
  - `type Scope struct { Namespaces []string; Exclude []string; Lookback Duration }`
  - `type ConnectorSpec struct { URL string; Mode string; EstimateFallback bool }`
  - `type Connectors struct { PromQL, OpenCost, Alertmanager, GitOps ConnectorSpec }`
  - `type LLM struct { Provider string; Model string; MaxToolCallsPerFinding int; MaxFindingsInvestigated int }`
  - `type Output struct { Dir string; FailOn string }`
  - `func Default() *Config`
  - `func Load(path string) (*Config, error)` — read file, unmarshal onto `Default()`, apply defaults for any still-zero fields, `Validate()`.
  - `func (c *Config) Validate() error`

- [ ] **Step 1: Write the failing test**

`internal/config/config_test.go`:

```go
package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadMinimalAppliesDefaults(t *testing.T) {
	cfg, err := Load("testdata/minimal.yaml")
	require.NoError(t, err)

	require.Equal(t, "prod", cfg.Cluster.Context)
	require.Equal(t, 24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, []string{"kube-system", "kube-node-lease"}, cfg.Scope.Exclude)
	require.Equal(t, "auto", cfg.Connectors.PromQL.URL)
	require.True(t, cfg.Connectors.OpenCost.EstimateFallback)
	require.Equal(t, "auto", cfg.Connectors.GitOps.Mode)
	require.Equal(t, "anthropic", cfg.LLM.Provider)
	require.Equal(t, "claude-sonnet-5", cfg.LLM.Model)
	require.Equal(t, 6, cfg.LLM.MaxToolCallsPerFinding)
	require.Equal(t, 15, cfg.LLM.MaxFindingsInvestigated)
	require.Equal(t, "./poirot-out", cfg.Output.Dir)
	require.Equal(t, "critical", cfg.Output.FailOn)
}

func TestLoadFullOverridesDefaults(t *testing.T) {
	cfg, err := Load("testdata/full.yaml")
	require.NoError(t, err)

	require.Equal(t, []string{"payments", "checkout"}, cfg.Scope.Namespaces)
	require.Equal(t, 7*24*time.Hour, time.Duration(cfg.Scope.Lookback))
	require.Equal(t, "http://prom.mon:9090", cfg.Connectors.PromQL.URL)
	require.Equal(t, "none", cfg.LLM.Provider)
	require.Equal(t, "warning", cfg.Output.FailOn)
	require.Equal(t, []string{"cost"}, cfg.Focus)
}

func TestLoadRejectsUnknownProvider(t *testing.T) {
	_, err := Load("testdata/bad-provider.yaml")
	require.ErrorContains(t, err, "llm.provider")
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("testdata/does-not-exist.yaml")
	require.Error(t, err)
}
```

`internal/config/testdata/minimal.yaml`:

```yaml
cluster:
  context: prod
```

`internal/config/testdata/full.yaml`:

```yaml
cluster:
  kubeconfig: ~/.kube/config
  context: prod
scope:
  namespaces: [payments, checkout]
  lookback: 168h
connectors:
  promql: { url: "http://prom.mon:9090" }
focus: [cost]
llm:
  provider: none
output:
  failOn: warning
```

`internal/config/testdata/bad-provider.yaml`:

```yaml
cluster:
  context: prod
llm:
  provider: gemini
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Write the implementation**

`internal/config/config.go`:

```go
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"sigs.k8s.io/yaml"
)

// Duration parses Go duration strings ("24h", "90m") from YAML/JSON.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

type Cluster struct {
	Kubeconfig string `json:"kubeconfig"`
	Context    string `json:"context"`
}

type Scope struct {
	Namespaces []string `json:"namespaces"`
	Exclude    []string `json:"exclude"`
	Lookback   Duration `json:"lookback"`
}

type ConnectorSpec struct {
	URL              string `json:"url"`
	Mode             string `json:"mode"`
	EstimateFallback bool   `json:"estimateFallback"`
}

type Connectors struct {
	PromQL       ConnectorSpec `json:"promql"`
	OpenCost     ConnectorSpec `json:"opencost"`
	Alertmanager ConnectorSpec `json:"alertmanager"`
	GitOps       ConnectorSpec `json:"gitops"`
}

type LLM struct {
	Provider                string `json:"provider"`
	Model                   string `json:"model"`
	MaxToolCallsPerFinding  int    `json:"maxToolCallsPerFinding"`
	MaxFindingsInvestigated int    `json:"maxFindingsInvestigated"`
}

type Output struct {
	Dir    string `json:"dir"`
	FailOn string `json:"failOn"`
}

type Config struct {
	Cluster    Cluster    `json:"cluster"`
	Scope      Scope      `json:"scope"`
	Connectors Connectors `json:"connectors"`
	Focus      []string   `json:"focus"`
	LLM        LLM        `json:"llm"`
	Output     Output     `json:"output"`
}

func Default() *Config {
	return &Config{
		Scope: Scope{
			Exclude:  []string{"kube-system", "kube-node-lease"},
			Lookback: Duration(24 * time.Hour),
		},
		Connectors: Connectors{
			PromQL:       ConnectorSpec{URL: "auto"},
			OpenCost:     ConnectorSpec{URL: "auto", EstimateFallback: true},
			Alertmanager: ConnectorSpec{URL: "auto"},
			GitOps:       ConnectorSpec{Mode: "auto"},
		},
		LLM: LLM{
			Provider:                "anthropic",
			Model:                   "claude-sonnet-5",
			MaxToolCallsPerFinding:  6,
			MaxFindingsInvestigated: 15,
		},
		Output: Output{Dir: "./poirot-out", FailOn: "critical"},
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyDefaults fills fields still at their zero value after unmarshal.
func (c *Config) applyDefaults() {
	d := Default()
	if c.Scope.Lookback == 0 {
		c.Scope.Lookback = d.Scope.Lookback
	}
	if c.Scope.Exclude == nil {
		c.Scope.Exclude = d.Scope.Exclude
	}
	if c.Connectors.PromQL.URL == "" {
		c.Connectors.PromQL.URL = d.Connectors.PromQL.URL
	}
	if c.Connectors.OpenCost.URL == "" {
		c.Connectors.OpenCost.URL = d.Connectors.OpenCost.URL
	}
	if c.Connectors.Alertmanager.URL == "" {
		c.Connectors.Alertmanager.URL = d.Connectors.Alertmanager.URL
	}
	if c.Connectors.GitOps.Mode == "" {
		c.Connectors.GitOps.Mode = d.Connectors.GitOps.Mode
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = d.LLM.Provider
	}
	if c.LLM.Model == "" {
		c.LLM.Model = d.LLM.Model
	}
	if c.LLM.MaxToolCallsPerFinding == 0 {
		c.LLM.MaxToolCallsPerFinding = d.LLM.MaxToolCallsPerFinding
	}
	if c.LLM.MaxFindingsInvestigated == 0 {
		c.LLM.MaxFindingsInvestigated = d.LLM.MaxFindingsInvestigated
	}
	if c.Output.Dir == "" {
		c.Output.Dir = d.Output.Dir
	}
	if c.Output.FailOn == "" {
		c.Output.FailOn = d.Output.FailOn
	}
	// OpenCost.EstimateFallback defaults to true only when the whole opencost
	// block was omitted; if the user wrote an opencost block they opt in explicitly.
	// For M1 we always want the default-on behavior, so force it when unset via URL check above.
	if d.Connectors.OpenCost.EstimateFallback && c.Connectors.OpenCost.URL == "auto" {
		c.Connectors.OpenCost.EstimateFallback = true
	}
}

func (c *Config) Validate() error {
	switch c.LLM.Provider {
	case "anthropic", "openai-compatible", "none":
	default:
		return fmt.Errorf("llm.provider must be anthropic|openai-compatible|none, got %q", c.LLM.Provider)
	}
	switch c.Output.FailOn {
	case "none", "warning", "critical":
	default:
		return fmt.Errorf("output.failOn must be none|warning|critical, got %q", c.Output.FailOn)
	}
	if time.Duration(c.Scope.Lookback) <= 0 {
		return fmt.Errorf("scope.lookback must be positive, got %s", time.Duration(c.Scope.Lookback))
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/config/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat: config loading with defaults and validation"
```

---

## Task 3: Domain types (snapshot + finding)

**Files:**
- Create: `internal/snapshot/snapshot.go`
- Create: `internal/analyzer/finding.go`
- Test: `internal/analyzer/finding_test.go`

**Interfaces:**
- Consumes: `k8s.io/api/*` types.
- Produces (package `snapshot`):
  - `type Meta struct { CollectedAt time.Time; Lookback time.Duration; Context string; Namespaces []string }`
  - `type Snapshot struct { Meta Meta; Deployments []appsv1.Deployment; StatefulSets []appsv1.StatefulSet; DaemonSets []appsv1.DaemonSet; ReplicaSets []appsv1.ReplicaSet; Pods []corev1.Pod; Events []corev1.Event; Nodes []corev1.Node; PDBs []policyv1.PodDisruptionBudget; HPAs []autoscalingv2.HorizontalPodAutoscaler; PVCs []corev1.PersistentVolumeClaim; Services []corev1.Service }`
- Produces (package `analyzer`):
  - `type Severity string` with consts `SeverityCritical="critical"`, `SeverityWarning="warning"`, `SeverityInfo="info"`, and `func (s Severity) Rank() int` (critical=3, warning=2, info=1, else 0).
  - `type ObjectRef struct { APIVersion string; Kind string; Namespace string; Name string }` with `func (o ObjectRef) String() string` (`Kind/Name` or `Kind/Namespace/Name`).
  - `type Evidence struct { Source string; Query string; Value any; At time.Time }`
  - `type Analysis struct { ProbableCause string; CorrelatedFindings []string; Confidence string; Remediation string }`
  - `type Finding struct { RuleID string; Domain string; Severity Severity; Title string; Object ObjectRef; Evidence []Evidence; Summary string; Analysis *Analysis }`
  - `type Analyzer interface { ID() string; Requires() []string; Analyze(ctx context.Context, snap *snapshot.Snapshot) ([]Finding, error) }`

- [ ] **Step 1: Write the failing test**

`internal/analyzer/finding_test.go`:

```go
package analyzer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSeverityRankOrders(t *testing.T) {
	require.Greater(t, SeverityCritical.Rank(), SeverityWarning.Rank())
	require.Greater(t, SeverityWarning.Rank(), SeverityInfo.Rank())
	require.Equal(t, 0, Severity("bogus").Rank())
}

func TestObjectRefString(t *testing.T) {
	require.Equal(t, "Node/worker-1", ObjectRef{Kind: "Node", Name: "worker-1"}.String())
	require.Equal(t, "Pod/payments/api-abc", ObjectRef{Kind: "Pod", Namespace: "payments", Name: "api-abc"}.String())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/ -v`
Expected: FAIL — `undefined: SeverityCritical`.

- [ ] **Step 3: Write the implementation**

`internal/snapshot/snapshot.go`:

```go
package snapshot

import (
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// Meta describes what was collected and over what window.
type Meta struct {
	CollectedAt time.Time
	Lookback    time.Duration
	Context     string
	Namespaces  []string // effective scope; empty means all
}

// Snapshot is the immutable set of raw cluster objects handed from
// collection to analysis. Analyzers must not mutate it.
type Snapshot struct {
	Meta         Meta
	Deployments  []appsv1.Deployment
	StatefulSets []appsv1.StatefulSet
	DaemonSets   []appsv1.DaemonSet
	ReplicaSets  []appsv1.ReplicaSet
	Pods         []corev1.Pod
	Events       []corev1.Event
	Nodes        []corev1.Node
	PDBs         []policyv1.PodDisruptionBudget
	HPAs         []autoscalingv2.HorizontalPodAutoscaler
	PVCs         []corev1.PersistentVolumeClaim
	Services     []corev1.Service
}
```

`internal/analyzer/finding.go`:

```go
package analyzer

import (
	"context"
	"fmt"
	"time"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Rank returns a sortable severity rank; higher is more severe.
func (s Severity) Rank() int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	case SeverityInfo:
		return 1
	default:
		return 0
	}
}

type ObjectRef struct {
	APIVersion string `json:"apiVersion,omitempty"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
}

func (o ObjectRef) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s/%s", o.Kind, o.Name)
	}
	return fmt.Sprintf("%s/%s/%s", o.Kind, o.Namespace, o.Name)
}

type Evidence struct {
	Source string    `json:"source"`
	Query  string    `json:"query"`
	Value  any       `json:"value"`
	At     time.Time `json:"at"`
}

type Analysis struct {
	ProbableCause      string   `json:"probableCause"`
	CorrelatedFindings []string `json:"correlatedFindings,omitempty"`
	Confidence         string   `json:"confidence"`
	Remediation        string   `json:"remediation"`
}

type Finding struct {
	RuleID   string     `json:"ruleId"`
	Domain   string     `json:"domain"`
	Severity Severity   `json:"severity"`
	Title    string     `json:"title"`
	Object   ObjectRef  `json:"object"`
	Evidence []Evidence `json:"evidence"`
	Summary  string     `json:"summary"`
	Analysis *Analysis  `json:"analysis,omitempty"`
}

// Analyzer is a deterministic, pure check over a Snapshot.
type Analyzer interface {
	ID() string
	Requires() []string // connector names that must be available
	Analyze(ctx context.Context, snap *snapshot.Snapshot) ([]Finding, error)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/analyzer/ ./internal/snapshot/ -race -v`
Expected: PASS (snapshot has no tests yet; it must compile).

- [ ] **Step 5: Commit**

```bash
git add internal/snapshot/ internal/analyzer/finding.go internal/analyzer/finding_test.go
git commit -m "feat: core domain types — Snapshot, Finding, Analyzer interface"
```

---

## Task 4: Connector interface + registry

**Files:**
- Create: `internal/connector/registry.go`
- Test: `internal/connector/registry_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces (package `connector`):
  - `type State string` with consts `StateAvailable="available"`, `StateDegraded="degraded"`, `StateAbsent="absent"`.
  - `type Availability struct { State State; Reason string; Detail string }`
  - `type Capability struct { ID string; Description string; ArgsSchema json.RawMessage }`
  - `type Connector interface { Name() string; Probe(ctx context.Context) Availability; Capabilities() []Capability; Query(ctx context.Context, capabilityID string, args json.RawMessage) (json.RawMessage, error) }`
  - `type Status struct { Name string; Availability Availability }`
  - `type Registry struct { ... }` with:
    - `func NewRegistry() *Registry`
    - `func (r *Registry) Register(c Connector)`
    - `func (r *Registry) Probe(ctx context.Context) []Status` — probes every registered connector in registration order, caches results, returns statuses.
    - `func (r *Registry) Available() []Connector` — connectors whose last probe was `StateAvailable`.
    - `func (r *Registry) Get(name string) (Connector, bool)`
    - `func (r *Registry) Satisfied(names []string) bool` — true iff every name is a currently-available connector.

- [ ] **Step 1: Write the failing test**

`internal/connector/registry_test.go`:

```go
package connector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

type fakeConnector struct {
	name  string
	avail Availability
}

func (f fakeConnector) Name() string                       { return f.name }
func (f fakeConnector) Probe(context.Context) Availability  { return f.avail }
func (f fakeConnector) Capabilities() []Capability          { return nil }
func (f fakeConnector) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func TestRegistryProbeAndAvailable(t *testing.T) {
	r := NewRegistry()
	r.Register(fakeConnector{name: "k8s", avail: Availability{State: StateAvailable}})
	r.Register(fakeConnector{name: "promql", avail: Availability{State: StateAbsent, Reason: "not found"}})

	statuses := r.Probe(context.Background())
	require.Len(t, statuses, 2)
	require.Equal(t, "k8s", statuses[0].Name)
	require.Equal(t, StateAbsent, statuses[1].Availability.State)

	avail := r.Available()
	require.Len(t, avail, 1)
	require.Equal(t, "k8s", avail[0].Name())

	require.True(t, r.Satisfied([]string{"k8s"}))
	require.False(t, r.Satisfied([]string{"k8s", "promql"}))

	c, ok := r.Get("promql")
	require.True(t, ok)
	require.Equal(t, "promql", c.Name())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/ -v`
Expected: FAIL — `undefined: NewRegistry`.

- [ ] **Step 3: Write the implementation**

`internal/connector/registry.go`:

```go
package connector

import (
	"context"
	"encoding/json"
)

type State string

const (
	StateAvailable State = "available"
	StateDegraded  State = "degraded"
	StateAbsent    State = "absent"
)

type Availability struct {
	State  State  `json:"state"`
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// Capability is declared as data so it can be served verbatim over MCP later.
type Capability struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	ArgsSchema  json.RawMessage `json:"argsSchema"`
}

// Connector is a read-only provider of cluster/observability data.
type Connector interface {
	Name() string
	Probe(ctx context.Context) Availability
	Capabilities() []Capability
	Query(ctx context.Context, capabilityID string, args json.RawMessage) (json.RawMessage, error)
}

type Status struct {
	Name         string       `json:"name"`
	Availability Availability `json:"availability"`
}

type Registry struct {
	connectors []Connector
	statuses   map[string]Availability
}

func NewRegistry() *Registry {
	return &Registry{statuses: map[string]Availability{}}
}

func (r *Registry) Register(c Connector) {
	r.connectors = append(r.connectors, c)
}

func (r *Registry) Probe(ctx context.Context) []Status {
	out := make([]Status, 0, len(r.connectors))
	for _, c := range r.connectors {
		av := c.Probe(ctx)
		r.statuses[c.Name()] = av
		out = append(out, Status{Name: c.Name(), Availability: av})
	}
	return out
}

func (r *Registry) Available() []Connector {
	var out []Connector
	for _, c := range r.connectors {
		if r.statuses[c.Name()].State == StateAvailable {
			out = append(out, c)
		}
	}
	return out
}

func (r *Registry) Get(name string) (Connector, bool) {
	for _, c := range r.connectors {
		if c.Name() == name {
			return c, true
		}
	}
	return nil, false
}

func (r *Registry) Satisfied(names []string) bool {
	for _, n := range names {
		if r.statuses[n].State != StateAvailable {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/
git commit -m "feat: connector interface and registry with probe/availability"
```

---

## Task 5: k8s connector — REST config, client, Probe

**Files:**
- Create: `internal/connector/k8s/k8s.go`
- Test: `internal/connector/k8s/k8s_test.go`

**Interfaces:**
- Consumes: `connector.Availability`, `connector.Capability`, `connector.State*`.
- Produces (package `k8s`):
  - `type Scope struct { Namespaces []string; Exclude []string; Lookback time.Duration }`
  - `type Connector struct { ... }` (unexported fields: `cs kubernetes.Interface`, `ctxName string`, `scope Scope`).
  - `func New(kubeconfigPath, contextName string, scope Scope) (*Connector, error)` — in-cluster config when `kubeconfigPath==""` and running in a pod; else loads kubeconfig (expanding a leading `~/`), applying `contextName` override; records the resolved context name.
  - `func NewWithClient(cs kubernetes.Interface, contextName string, scope Scope) *Connector` — test seam.
  - `func (c *Connector) Name() string` → `"k8s"`
  - `func (c *Connector) Probe(ctx context.Context) connector.Availability` — calls `Discovery().ServerVersion()`; `available` on success, `absent` with `Detail=err.Error()` on failure.
  - `func (c *Connector) Capabilities() []connector.Capability` → `nil` (wired in M3).
  - `func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error)` → returns error `"k8s connector exposes no queryable capabilities in M1"`.
  - `func (c *Connector) ContextName() string`

- [ ] **Step 1: Write the failing test**

`internal/connector/k8s/k8s_test.go`:

```go
package k8s

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/connector"
)

func TestProbeAvailableWithFakeClient(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := NewWithClient(cs, "test-ctx", Scope{})

	require.Equal(t, "k8s", c.Name())
	require.Equal(t, "test-ctx", c.ContextName())

	av := c.Probe(context.Background())
	require.Equal(t, connector.StateAvailable, av.State)
}

func TestQueryUnsupportedInM1(t *testing.T) {
	c := NewWithClient(fake.NewSimpleClientset(), "test-ctx", Scope{})
	_, err := c.Query(context.Background(), "anything", nil)
	require.ErrorContains(t, err, "no queryable capabilities")
}
```

> Note: `fake.NewSimpleClientset()`'s discovery returns a non-nil (empty) `ServerVersion` without error, so `Probe` reports `available`. If the installed client-go version returns an error for fake discovery, adjust `Probe` to treat a nil error OR a fake discovery client as available; verify against the pinned version.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/k8s/ -v`
Expected: FAIL — `undefined: NewWithClient`.

- [ ] **Step 3: Write the implementation**

`internal/connector/k8s/k8s.go`:

```go
package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/init-kaushal/poirot/internal/connector"
)

type Scope struct {
	Namespaces []string
	Exclude    []string
	Lookback   time.Duration
}

type Connector struct {
	cs      kubernetes.Interface
	ctxName string
	scope   Scope
}

func New(kubeconfigPath, contextName string, scope Scope) (*Connector, error) {
	cfg, resolvedCtx, err := buildRESTConfig(kubeconfigPath, contextName)
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("build kubernetes client: %w", err)
	}
	return &Connector{cs: cs, ctxName: resolvedCtx, scope: scope}, nil
}

// NewWithClient injects a client (tests).
func NewWithClient(cs kubernetes.Interface, contextName string, scope Scope) *Connector {
	return &Connector{cs: cs, ctxName: contextName, scope: scope}
}

func buildRESTConfig(path, ctxName string) (*rest.Config, string, error) {
	if path == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, "in-cluster", nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if path != "" {
		rules.ExplicitPath = expandHome(path)
	}
	overrides := &clientcmd.ConfigOverrides{}
	if ctxName != "" {
		overrides.CurrentContext = ctxName
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	resolved := ctxName
	if resolved == "" {
		if raw, err := cc.RawConfig(); err == nil {
			resolved = raw.CurrentContext
		}
	}
	return restCfg, resolved, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func (c *Connector) Name() string        { return "k8s" }
func (c *Connector) ContextName() string { return c.ctxName }

func (c *Connector) Probe(ctx context.Context) connector.Availability {
	if _, err := c.cs.Discovery().ServerVersion(); err != nil {
		return connector.Availability{
			State:  connector.StateAbsent,
			Reason: "cannot reach Kubernetes API server",
			Detail: err.Error(),
		}
	}
	return connector.Availability{State: connector.StateAvailable}
}

func (c *Connector) Capabilities() []connector.Capability { return nil }

func (c *Connector) Query(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, fmt.Errorf("k8s connector exposes no queryable capabilities in M1")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/k8s/ -race -v`
Expected: PASS. If `Probe` fails against fake discovery, apply the note from Step 1.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/k8s/
git commit -m "feat: k8s connector — kubeconfig loading, client, probe"
```

---

## Task 6: k8s connector — Collect into Snapshot

**Files:**
- Create: `internal/connector/k8s/collect.go`
- Test: `internal/connector/k8s/collect_test.go`

**Interfaces:**
- Consumes: `snapshot.Snapshot`, `snapshot.Meta`, the `k8s.Connector` from Task 5.
- Produces (package `k8s`):
  - `func (c *Connector) Collect(ctx context.Context) (*snapshot.Snapshot, error)` — lists cluster-scoped `Nodes`; resolves target namespaces (`scope.Namespaces` if non-empty, else all namespaces minus `scope.Exclude`); for each namespace lists Deployments, StatefulSets, DaemonSets, ReplicaSets, Pods, Events, PodDisruptionBudgets, HorizontalPodAutoscalers (autoscaling/v2), PersistentVolumeClaims, Services; fills `Snapshot.Meta` (`CollectedAt=now`, `Lookback=scope.Lookback`, `Context=ctxName`, `Namespaces=resolved`). Any list error is wrapped and returned (fail the collect; the orchestrator decides posture).
  - `func (c *Connector) targetNamespaces(ctx context.Context) ([]string, error)` (unexported).

- [ ] **Step 1: Write the failing test**

`internal/connector/k8s/collect_test.go`:

```go
package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestCollectGathersScopedObjects(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "payments"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "payments"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "sys", Namespace: "kube-system"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"}},
	)
	c := NewWithClient(cs, "test-ctx", Scope{
		Exclude:  []string{"kube-system"},
		Lookback: 24 * time.Hour,
	})

	snap, err := c.Collect(context.Background())
	require.NoError(t, err)

	require.Equal(t, []string{"payments"}, snap.Meta.Namespaces)
	require.Equal(t, "test-ctx", snap.Meta.Context)
	require.Equal(t, 24*time.Hour, snap.Meta.Lookback)
	require.WithinDuration(t, time.Now(), snap.Meta.CollectedAt, time.Minute)

	require.Len(t, snap.Nodes, 1)
	require.Len(t, snap.Deployments, 1)
	require.Equal(t, "api", snap.Deployments[0].Name)
	require.Len(t, snap.Pods, 1)
}

func TestCollectExplicitNamespaces(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "b"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "da", Namespace: "a"}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "b"}},
	)
	c := NewWithClient(cs, "x", Scope{Namespaces: []string{"a"}})

	snap, err := c.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"a"}, snap.Meta.Namespaces)
	require.Len(t, snap.Deployments, 1)
	require.Equal(t, "da", snap.Deployments[0].Name)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/connector/k8s/ -run TestCollect -v`
Expected: FAIL — `c.Collect undefined`.

- [ ] **Step 3: Write the implementation**

`internal/connector/k8s/collect.go`:

```go
package k8s

import (
	"context"
	"fmt"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

func (c *Connector) Collect(ctx context.Context) (*snapshot.Snapshot, error) {
	snap := &snapshot.Snapshot{}

	nodes, err := c.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	snap.Nodes = nodes.Items

	namespaces, err := c.targetNamespaces(ctx)
	if err != nil {
		return nil, err
	}

	for _, ns := range namespaces {
		deploys, err := c.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list deployments in %s: %w", ns, err)
		}
		snap.Deployments = append(snap.Deployments, deploys.Items...)

		sts, err := c.cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list statefulsets in %s: %w", ns, err)
		}
		snap.StatefulSets = append(snap.StatefulSets, sts.Items...)

		ds, err := c.cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list daemonsets in %s: %w", ns, err)
		}
		snap.DaemonSets = append(snap.DaemonSets, ds.Items...)

		rs, err := c.cs.AppsV1().ReplicaSets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list replicasets in %s: %w", ns, err)
		}
		snap.ReplicaSets = append(snap.ReplicaSets, rs.Items...)

		pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pods in %s: %w", ns, err)
		}
		snap.Pods = append(snap.Pods, pods.Items...)

		events, err := c.cs.CoreV1().Events(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list events in %s: %w", ns, err)
		}
		snap.Events = append(snap.Events, events.Items...)

		pdbs, err := c.cs.PolicyV1().PodDisruptionBudgets(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pdbs in %s: %w", ns, err)
		}
		snap.PDBs = append(snap.PDBs, pdbs.Items...)

		hpas, err := c.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list hpas in %s: %w", ns, err)
		}
		snap.HPAs = append(snap.HPAs, hpas.Items...)

		pvcs, err := c.cs.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list pvcs in %s: %w", ns, err)
		}
		snap.PVCs = append(snap.PVCs, pvcs.Items...)

		svcs, err := c.cs.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("list services in %s: %w", ns, err)
		}
		snap.Services = append(snap.Services, svcs.Items...)
	}

	snap.Meta = snapshot.Meta{
		CollectedAt: time.Now(),
		Lookback:    c.scope.Lookback,
		Context:     c.ctxName,
		Namespaces:  namespaces,
	}
	return snap, nil
}

func (c *Connector) targetNamespaces(ctx context.Context) ([]string, error) {
	if len(c.scope.Namespaces) > 0 {
		out := append([]string(nil), c.scope.Namespaces...)
		sort.Strings(out)
		return out, nil
	}
	nsList, err := c.cs.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list namespaces: %w", err)
	}
	excl := make(map[string]bool, len(c.scope.Exclude))
	for _, e := range c.scope.Exclude {
		excl[e] = true
	}
	var out []string
	for _, ns := range nsList.Items {
		if !excl[ns.Name] {
			out = append(out, ns.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/connector/k8s/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/connector/k8s/collect.go internal/connector/k8s/collect_test.go
git commit -m "feat: k8s Collect — gather scoped cluster objects into Snapshot"
```

---

## Task 7: reliability analyzer — pod-level rules

**Files:**
- Create: `internal/analyzer/reliability/reliability.go`
- Create: `internal/analyzer/reliability/pods.go`
- Test: `internal/analyzer/reliability/pods_test.go`

**Interfaces:**
- Consumes: `analyzer.Finding`, `analyzer.Severity*`, `analyzer.ObjectRef`, `analyzer.Evidence`, `snapshot.Snapshot`.
- Produces (package `reliability`):
  - `type Analyzer struct{}` and `func New() *Analyzer`.
  - `func (*Analyzer) ID() string` → `"reliability"`.
  - `func (*Analyzer) Requires() []string` → `[]string{"k8s"}`.
  - `func (a *Analyzer) Analyze(ctx context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error)` — for M1 Task 7 it calls the pod-level rule functions and concatenates; Task 8 appends the rest.
  - Unexported rule funcs, each `func(snap *snapshot.Snapshot) []analyzer.Finding`:
    - `checkCrashLoop` — `reliability/crashloop`, **critical**. Any container status with `State.Waiting.Reason == "CrashLoopBackOff"`. Evidence: `{Source:"k8s", Query:"pod.status.containerStatuses[].state.waiting.reason", Value: reason, At: now}`. Summary: `"container %s in %s is in CrashLoopBackOff (%d restarts)"`.
    - `checkImagePull` — `reliability/image-pull`, **critical**. `State.Waiting.Reason` in `{"ImagePullBackOff","ErrImagePull"}`. Include the waiting `Message` as evidence value.
    - `checkOOMKilled` — `reliability/oomkilled`, **warning**. Any container `LastTerminationState.Terminated.Reason == "OOMKilled"`. Evidence value: the terminated `ExitCode` + `FinishedAt`.
    - `checkHighRestarts` — `reliability/restarts`, **warning**. Container `RestartCount >= 5` and not already CrashLoopBackOff. Evidence value: restart count. (M1 has no metrics; note in Summary that the count is cumulative.)
    - `checkPending` — `reliability/pending`, **warning** (escalate to **critical** if the pod is older than 15 min). Pod `Status.Phase == Pending` with a `PodScheduled` condition `Status==False`. Evidence value: the condition `Message` (scheduler reason).
    - `checkEvicted` — `reliability/evicted`, **warning**. Pod `Status.Phase == Failed` and `Status.Reason == "Evicted"`. Evidence value: `Status.Message`.
    - `checkProbeFailing` — `reliability/probe-failing`, **warning**. Pod has a `Warning` Event with `Reason == "Unhealthy"` whose `InvolvedObject` is the pod, within `snap.Meta.Lookback` of `snap.Meta.CollectedAt`. Evidence value: the event `Message` + `Count`.
    - `checkNotReady` — `reliability/not-ready`, **warning**. Pod `Status.Phase == Running`, a `Ready` condition `Status==False`, and that condition's `LastTransitionTime` older than 10 min relative to `snap.Meta.CollectedAt`. Evidence value: minutes not-ready.
  - Helper `podRef(pod) analyzer.ObjectRef` → `{APIVersion:"v1", Kind:"Pod", Namespace, Name}`.
  - Helper `finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, ev ...analyzer.Evidence) analyzer.Finding` — sets `Domain:"reliability"`.

- [ ] **Step 1: Write the failing test**

`internal/analyzer/reliability/pods_test.go`:

```go
package reliability

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func ruleIDs(fs []analyzer.Finding) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.RuleID
	}
	return out
}

func TestCrashLoopIsCritical(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: 24 * time.Hour},
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "payments"},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:         "api",
					RestartCount: 7,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"},
					},
				}},
			},
		}},
	}

	fs := checkCrashLoop(snap)
	require.Len(t, fs, 1)
	require.Equal(t, "reliability/crashloop", fs[0].RuleID)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
	require.Equal(t, "Pod/payments/api-1", fs[0].Object.String())
	require.NotEmpty(t, fs[0].Evidence)
	require.Equal(t, "k8s", fs[0].Evidence[0].Source)
}

func TestPendingEscalatesWhenOld(t *testing.T) {
	now := time.Now()
	mk := func(age time.Duration) corev1.Pod {
		return corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p", Namespace: "d",
				CreationTimestamp: metav1.NewTime(now.Add(-age)),
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodPending,
				Conditions: []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse,
					Message: "0/3 nodes are available: insufficient cpu",
				}},
			},
		}
	}
	young := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now}, Pods: []corev1.Pod{mk(2 * time.Minute)}}
	old := &snapshot.Snapshot{Meta: snapshot.Meta{CollectedAt: now}, Pods: []corev1.Pod{mk(30 * time.Minute)}}

	require.Equal(t, analyzer.SeverityWarning, checkPending(young)[0].Severity)
	require.Equal(t, analyzer.SeverityCritical, checkPending(old)[0].Severity)
}

func TestAnalyzeRunsPodRules(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now(), Lookback: time.Hour},
		Pods: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "n"},
			Status: corev1.PodStatus{
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "c",
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137},
					},
				}},
			},
		}},
	}
	fs, err := New().Analyze(context.Background(), snap)
	require.NoError(t, err)
	require.Contains(t, ruleIDs(fs), "reliability/oomkilled")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/reliability/ -v`
Expected: FAIL — `undefined: checkCrashLoop`, `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/analyzer/reliability/reliability.go`:

```go
package reliability

import (
	"context"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type Analyzer struct{}

func New() *Analyzer { return &Analyzer{} }

func (*Analyzer) ID() string         { return "reliability" }
func (*Analyzer) Requires() []string { return []string{"k8s"} }

func (a *Analyzer) Analyze(_ context.Context, snap *snapshot.Snapshot) ([]analyzer.Finding, error) {
	var out []analyzer.Finding
	podRules := []func(*snapshot.Snapshot) []analyzer.Finding{
		checkCrashLoop,
		checkImagePull,
		checkOOMKilled,
		checkHighRestarts,
		checkPending,
		checkEvicted,
		checkProbeFailing,
		checkNotReady,
	}
	for _, r := range podRules {
		out = append(out, r(snap)...)
	}
	// Task 8 appends workload/node/pvc/hpa/event rules here.
	return out, nil
}

func podRef(p corevPod) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: p.Namespace, Name: p.Name}
}

func finding(ruleID string, sev analyzer.Severity, obj analyzer.ObjectRef, title, summary string, ev ...analyzer.Evidence) analyzer.Finding {
	return analyzer.Finding{
		RuleID:   ruleID,
		Domain:   "reliability",
		Severity: sev,
		Title:    title,
		Object:   obj,
		Summary:  summary,
		Evidence: ev,
	}
}
```

> `corevPod` above is a alias to avoid a long import line in the helper; define it in `pods.go` as `type corevPod = corev1.Pod`. Alternatively inline `corev1.Pod` — pick one and keep it consistent.

`internal/analyzer/reliability/pods.go`:

```go
package reliability

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type corevPod = corev1.Pod

func ev(query string, value any, at time.Time) analyzer.Evidence {
	return analyzer.Evidence{Source: "k8s", Query: query, Value: value, At: at}
}

func checkCrashLoop(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				out = append(out, finding(
					"reliability/crashloop", analyzer.SeverityCritical, podRef(p),
					"Container in CrashLoopBackOff",
					fmt.Sprintf("container %q in %s is in CrashLoopBackOff (%d restarts)", cs.Name, podRef(p), cs.RestartCount),
					ev("pod.status.containerStatuses[].state.waiting.reason", cs.State.Waiting.Reason, snap.Meta.CollectedAt),
				))
			}
		}
	}
	return out
}

func checkImagePull(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		for _, cs := range p.Status.ContainerStatuses {
			w := cs.State.Waiting
			if w != nil && (w.Reason == "ImagePullBackOff" || w.Reason == "ErrImagePull") {
				out = append(out, finding(
					"reliability/image-pull", analyzer.SeverityCritical, podRef(p),
					"Image cannot be pulled",
					fmt.Sprintf("container %q in %s cannot pull its image (%s)", cs.Name, podRef(p), w.Reason),
					ev("pod.status.containerStatuses[].state.waiting.message", w.Message, snap.Meta.CollectedAt),
				))
			}
		}
	}
	return out
}

func checkOOMKilled(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		for _, cs := range p.Status.ContainerStatuses {
			t := cs.LastTerminationState.Terminated
			if t != nil && t.Reason == "OOMKilled" {
				out = append(out, finding(
					"reliability/oomkilled", analyzer.SeverityWarning, podRef(p),
					"Container was OOMKilled",
					fmt.Sprintf("container %q in %s was OOMKilled (exit %d)", cs.Name, podRef(p), t.ExitCode),
					ev("pod.status.containerStatuses[].lastState.terminated.reason",
						map[string]any{"reason": t.Reason, "exitCode": t.ExitCode, "finishedAt": t.FinishedAt.Time},
						snap.Meta.CollectedAt),
				))
			}
		}
	}
	return out
}

func checkHighRestarts(snap *snapshot.Snapshot) []analyzer.Finding {
	const threshold = 5
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		for _, cs := range p.Status.ContainerStatuses {
			crashing := cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff"
			if !crashing && cs.RestartCount >= threshold {
				out = append(out, finding(
					"reliability/restarts", analyzer.SeverityWarning, podRef(p),
					"Container has restarted many times",
					fmt.Sprintf("container %q in %s has %d cumulative restarts", cs.Name, podRef(p), cs.RestartCount),
					ev("pod.status.containerStatuses[].restartCount", cs.RestartCount, snap.Meta.CollectedAt),
				))
			}
		}
	}
	return out
}

func checkPending(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		if p.Status.Phase != corev1.PodPending {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse {
				sev := analyzer.SeverityWarning
				age := snap.Meta.CollectedAt.Sub(p.CreationTimestamp.Time)
				if age > 15*time.Minute {
					sev = analyzer.SeverityCritical
				}
				out = append(out, finding(
					"reliability/pending", sev, podRef(p),
					"Pod cannot be scheduled",
					fmt.Sprintf("%s has been Pending for %s: %s", podRef(p), age.Round(time.Minute), c.Message),
					ev("pod.status.conditions[type=PodScheduled].message", c.Message, snap.Meta.CollectedAt),
				))
			}
		}
	}
	return out
}

func checkEvicted(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		if p.Status.Phase == corev1.PodFailed && p.Status.Reason == "Evicted" {
			out = append(out, finding(
				"reliability/evicted", analyzer.SeverityWarning, podRef(p),
				"Pod was evicted",
				fmt.Sprintf("%s was evicted: %s", podRef(p), p.Status.Message),
				ev("pod.status.reason", map[string]any{"reason": p.Status.Reason, "message": p.Status.Message}, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkProbeFailing(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	cutoff := snap.Meta.CollectedAt.Add(-snap.Meta.Lookback)
	for _, e := range snap.Events {
		if e.Type != corev1.EventTypeWarning || e.Reason != "Unhealthy" || e.InvolvedObject.Kind != "Pod" {
			continue
		}
		if lastSeen(e).Before(cutoff) {
			continue
		}
		out = append(out, finding(
			"reliability/probe-failing", analyzer.SeverityWarning,
			analyzer.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: e.InvolvedObject.Namespace, Name: e.InvolvedObject.Name},
			"Probe is failing",
			fmt.Sprintf("Pod/%s/%s probe failing: %s (x%d)", e.InvolvedObject.Namespace, e.InvolvedObject.Name, e.Message, e.Count),
			ev("event[reason=Unhealthy].message", map[string]any{"message": e.Message, "count": e.Count}, lastSeen(e)),
		))
	}
	return out
}

func checkNotReady(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.Pods {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodReady && c.Status == corev1.ConditionFalse {
				notReady := snap.Meta.CollectedAt.Sub(c.LastTransitionTime.Time)
				if notReady > 10*time.Minute {
					out = append(out, finding(
						"reliability/not-ready", analyzer.SeverityWarning, podRef(p),
						"Pod running but not Ready",
						fmt.Sprintf("%s has been running but not Ready for %s", podRef(p), notReady.Round(time.Minute)),
						ev("pod.status.conditions[type=Ready]", map[string]any{"minutesNotReady": int(notReady.Minutes())}, snap.Meta.CollectedAt),
					))
				}
			}
		}
	}
	return out
}

// lastSeen returns the most recent timestamp on an event.
func lastSeen(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.FirstTimestamp.Time
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/analyzer/reliability/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/analyzer/reliability/
git commit -m "feat: reliability analyzer — pod-level rules"
```

---

## Task 8: reliability analyzer — workload, node, PVC, HPA, event rules

**Files:**
- Create: `internal/analyzer/reliability/workloads.go`
- Modify: `internal/analyzer/reliability/reliability.go` — append the new rule group in `Analyze`.
- Test: `internal/analyzer/reliability/workloads_test.go`

**Interfaces:**
- Consumes: same as Task 7, plus `appsv1`, `policyv1`, `autoscalingv2` types via the snapshot.
- Produces (package `reliability`) — unexported funcs, signature `func(snap *snapshot.Snapshot) []analyzer.Finding` unless noted:
  - `checkSingleReplica` — `reliability/single-replica`, **info**. Deployment or StatefulSet with `spec.replicas == 1`. `workloadRef` object.
  - `checkNoResourceLimits` — `reliability/no-limits`, **info**. Any container in a Deployment/StatefulSet/DaemonSet pod template with empty `Resources.Limits` (both cpu and memory missing). One finding per workload (list offending containers in Summary).
  - `checkNoProbes` — `reliability/no-probes`, **info**. Deployment/StatefulSet pod-template container with both `ReadinessProbe==nil` and `LivenessProbe==nil`.
  - `checkNoPDB` — `reliability/no-pdb`, **info**. Deployment/StatefulSet with `spec.replicas >= 2` and no PDB in the same namespace whose `Spec.Selector` matches the workload's `spec.selector.matchLabels` (compare as: every PDB selector key/value is present and equal in the workload selector). Evidence value: replica count.
  - `checkLatestTag` — `reliability/latest-tag`, **info**. Pod-template container image ending `:latest` or with no `:` tag segment. Object = `workloadRef`.
  - `checkHPAMaxed` — `reliability/hpa-maxed`, **warning**. HPA with `Status.CurrentReplicas == Spec.MaxReplicas` AND `Status.DesiredReplicas >= Spec.MaxReplicas`. Object: `{APIVersion:"autoscaling/v2", Kind:"HorizontalPodAutoscaler", Namespace, Name}`. Evidence value: `{current, desired, max}`.
  - `checkNodePressure` — `reliability/node-pressure`, **warning** (**critical** if the node's `Ready` condition is not `True`). Node with any of `MemoryPressure|DiskPressure|PIDPressure == True`, or `Ready != True`. Object: `{Kind:"Node", Name}`. One finding per node; list the tripped conditions in Summary + evidence.
  - `checkPVCUnbound` — `reliability/pvc-unbound`, **warning**. PVC `Status.Phase != Bound`. Object: `{APIVersion:"v1", Kind:"PersistentVolumeClaim", Namespace, Name}`. Evidence value: the phase.
  - `checkWarningEventClusters` — `reliability/warning-events`, **info**. Group `Warning` events within lookback by `InvolvedObject` (kind+ns+name); emit a finding when total `Count` across events for that object `>= 3` AND no more-specific reliability finding already exists for that object. Since this needs the other findings, its signature is `func(snap *snapshot.Snapshot, existing []analyzer.Finding) []analyzer.Finding`. Evidence value: `{eventCount, reasons: []string}`.
  - `workloadRef(kind, namespace, name string) analyzer.ObjectRef` helper — `APIVersion:"apps/v1"`.

- [ ] **Step 1: Write the failing test**

`internal/analyzer/reliability/workloads_test.go`:

```go
package reliability

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func i32(v int32) *int32 { return &v }

func TestSingleReplicaAndNoPDB(t *testing.T) {
	dep := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
		Spec: appsv1.DeploymentSpec{
			Replicas: i32(3),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
		},
	}
	snap := &snapshot.Snapshot{
		Meta:        snapshot.Meta{CollectedAt: time.Now()},
		Deployments: []appsv1.Deployment{dep},
	}
	require.Empty(t, checkSingleReplica(snap))
	nopdb := checkNoPDB(snap)
	require.Len(t, nopdb, 1)
	require.Equal(t, "reliability/no-pdb", nopdb[0].RuleID)

	snap.PDBs = []policyv1.PodDisruptionBudget{{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}},
	}}
	require.Empty(t, checkNoPDB(snap))
}

func TestHPAMaxed(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now()},
		HPAs: []autoscalingv2.HorizontalPodAutoscaler{{
			ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "n"},
			Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 10},
			Status:     autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 10, DesiredReplicas: 12},
		}},
	}
	fs := checkHPAMaxed(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityWarning, fs[0].Severity)
}

func TestNodePressureCriticalWhenNotReady(t *testing.T) {
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: time.Now()},
		Nodes: []corev1.Node{{
			ObjectMeta: metav1.ObjectMeta{Name: "w1"},
			Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			}},
		}},
	}
	fs := checkNodePressure(snap)
	require.Len(t, fs, 1)
	require.Equal(t, analyzer.SeverityCritical, fs[0].Severity)
	require.Equal(t, "Node/w1", fs[0].Object.String())
}

func TestWarningEventClustersSkipWhenSpecificExists(t *testing.T) {
	now := time.Now()
	snap := &snapshot.Snapshot{
		Meta: snapshot.Meta{CollectedAt: now, Lookback: time.Hour},
		Events: []corev1.Event{{
			Type:           corev1.EventTypeWarning,
			Reason:         "BackOff",
			Count:          5,
			LastTimestamp:  metav1.NewTime(now.Add(-5 * time.Minute)),
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "n", Name: "api-1"},
		}},
	}
	existing := []analyzer.Finding{{
		RuleID: "reliability/crashloop",
		Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: "api-1"},
	}}
	require.Empty(t, checkWarningEventClusters(snap, existing))
	require.Len(t, checkWarningEventClusters(snap, nil), 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/analyzer/reliability/ -run 'TestSingleReplica|TestHPAMaxed|TestNodePressure|TestWarningEvent' -v`
Expected: FAIL — `undefined: checkSingleReplica`.

- [ ] **Step 3: Write the implementation**

`internal/analyzer/reliability/workloads.go`:

```go
package reliability

import (
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func workloadRef(kind, namespace, name string) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "apps/v1", Kind: kind, Namespace: namespace, Name: name}
}

type workload struct {
	kind     string
	name     string
	ns       string
	replicas int32
	selector map[string]string
	template corev1.PodTemplateSpec
}

func workloads(snap *snapshot.Snapshot) []workload {
	var out []workload
	for _, d := range snap.Deployments {
		out = append(out, workload{
			kind: "Deployment", name: d.Name, ns: d.Namespace,
			replicas: derefI32(d.Spec.Replicas), selector: selLabels(d.Spec.Selector), template: d.Spec.Template,
		})
	}
	for _, s := range snap.StatefulSets {
		out = append(out, workload{
			kind: "StatefulSet", name: s.Name, ns: s.Namespace,
			replicas: derefI32(s.Spec.Replicas), selector: selLabels(s.Spec.Selector), template: s.Spec.Template,
		})
	}
	return out
}

func daemonsetTemplates(snap *snapshot.Snapshot) []workload {
	var out []workload
	for _, d := range snap.DaemonSets {
		out = append(out, workload{kind: "DaemonSet", name: d.Name, ns: d.Namespace, template: d.Spec.Template})
	}
	return out
}

func derefI32(p *int32) int32 {
	if p == nil {
		return 1 // k8s default for Deployment/StatefulSet replicas
	}
	return *p
}

func selLabels(s *metav1LabelSelector) map[string]string {
	if s == nil {
		return nil
	}
	return s.MatchLabels
}

// metav1LabelSelector aliased to keep imports tidy.
type metav1LabelSelector = struct {
	MatchLabels map[string]string
}
```

> **Note for the implementer:** the alias hack above will not compile. Replace `selLabels` with a direct `*metav1.LabelSelector` parameter and `import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"`. It is written out here only to keep this block focused; use the real type:
> ```go
> import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
> func selLabels(s *metav1.LabelSelector) map[string]string {
> 	if s == nil { return nil }
> 	return s.MatchLabels
> }
> ```
> Delete the `metav1LabelSelector` alias entirely.

```go
func checkSingleReplica(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		if w.replicas == 1 {
			out = append(out, finding(
				"reliability/single-replica", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Workload runs a single replica",
				fmt.Sprintf("%s %s/%s has replicas=1; no redundancy for node loss or rollout", w.kind, w.ns, w.name),
				ev("workload.spec.replicas", 1, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoResourceLimits(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	all := append(workloads(snap), daemonsetTemplates(snap)...)
	for _, w := range all {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			_, hasCPU := c.Resources.Limits[corev1.ResourceCPU]
			_, hasMem := c.Resources.Limits[corev1.ResourceMemory]
			if !hasCPU && !hasMem {
				bad = append(bad, c.Name)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/no-limits", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Containers have no resource limits",
				fmt.Sprintf("%s %s/%s: containers without CPU/memory limits: %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].resources.limits", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoProbes(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			if c.ReadinessProbe == nil && c.LivenessProbe == nil {
				bad = append(bad, c.Name)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/no-probes", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Containers have no health probes",
				fmt.Sprintf("%s %s/%s: containers without readiness or liveness probes: %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].{readinessProbe,livenessProbe}", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNoPDB(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, w := range workloads(snap) {
		if w.replicas < 2 {
			continue
		}
		if hasMatchingPDB(snap, w) {
			continue
		}
		out = append(out, finding(
			"reliability/no-pdb", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
			"Multi-replica workload has no PodDisruptionBudget",
			fmt.Sprintf("%s %s/%s has %d replicas but no PDB; voluntary disruptions can take it fully down", w.kind, w.ns, w.name, w.replicas),
			ev("poddisruptionbudgets (namespace match)", w.replicas, snap.Meta.CollectedAt),
		))
	}
	return out
}

func hasMatchingPDB(snap *snapshot.Snapshot, w workload) bool {
	for _, pdb := range snap.PDBs {
		if pdb.Namespace != w.ns || pdb.Spec.Selector == nil {
			continue
		}
		match := len(pdb.Spec.Selector.MatchLabels) > 0
		for k, v := range pdb.Spec.Selector.MatchLabels {
			if w.selector[k] != v {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func checkLatestTag(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	all := append(workloads(snap), daemonsetTemplates(snap)...)
	for _, w := range all {
		var bad []string
		for _, c := range w.template.Spec.Containers {
			if isFloatingTag(c.Image) {
				bad = append(bad, c.Image)
			}
		}
		if len(bad) > 0 {
			out = append(out, finding(
				"reliability/latest-tag", analyzer.SeverityInfo, workloadRef(w.kind, w.ns, w.name),
				"Container uses a floating image tag",
				fmt.Sprintf("%s %s/%s uses non-pinned image(s): %s", w.kind, w.ns, w.name, strings.Join(bad, ", ")),
				ev("workload.spec.template.spec.containers[].image", bad, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func isFloatingTag(image string) bool {
	ref := image
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return false // digest-pinned
	}
	slash := strings.LastIndex(ref, "/")
	lastPart := ref[slash+1:]
	colon := strings.LastIndex(lastPart, ":")
	if colon < 0 {
		return true // no tag => :latest
	}
	return lastPart[colon+1:] == "latest"
}

func checkHPAMaxed(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, h := range snap.HPAs {
		if h.Status.CurrentReplicas == h.Spec.MaxReplicas && h.Status.DesiredReplicas >= h.Spec.MaxReplicas {
			out = append(out, finding(
				"reliability/hpa-maxed", analyzer.SeverityWarning,
				analyzer.ObjectRef{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler", Namespace: h.Namespace, Name: h.Name},
				"HPA is pinned at maxReplicas",
				fmt.Sprintf("HPA %s/%s is at max (%d/%d, desired %d); it cannot scale out further", h.Namespace, h.Name, h.Status.CurrentReplicas, h.Spec.MaxReplicas, h.Status.DesiredReplicas),
				ev("hpa.status", map[string]any{"current": h.Status.CurrentReplicas, "desired": h.Status.DesiredReplicas, "max": h.Spec.MaxReplicas}, snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkNodePressure(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, n := range snap.Nodes {
		var tripped []string
		notReady := false
		for _, c := range n.Status.Conditions {
			switch c.Type {
			case corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure:
				if c.Status == corev1.ConditionTrue {
					tripped = append(tripped, string(c.Type))
				}
			case corev1.NodeReady:
				if c.Status != corev1.ConditionTrue {
					notReady = true
					tripped = append(tripped, "NotReady")
				}
			}
		}
		if len(tripped) == 0 {
			continue
		}
		sev := analyzer.SeverityWarning
		if notReady {
			sev = analyzer.SeverityCritical
		}
		out = append(out, finding(
			"reliability/node-pressure", sev,
			analyzer.ObjectRef{Kind: "Node", Name: n.Name},
			"Node under pressure or not Ready",
			fmt.Sprintf("Node %s: %s", n.Name, strings.Join(tripped, ", ")),
			ev("node.status.conditions", tripped, snap.Meta.CollectedAt),
		))
	}
	return out
}

func checkPVCUnbound(snap *snapshot.Snapshot) []analyzer.Finding {
	var out []analyzer.Finding
	for _, p := range snap.PVCs {
		if p.Status.Phase != corev1.ClaimBound {
			out = append(out, finding(
				"reliability/pvc-unbound", analyzer.SeverityWarning,
				analyzer.ObjectRef{APIVersion: "v1", Kind: "PersistentVolumeClaim", Namespace: p.Namespace, Name: p.Name},
				"PersistentVolumeClaim is not Bound",
				fmt.Sprintf("PVC %s/%s is %s", p.Namespace, p.Name, p.Status.Phase),
				ev("pvc.status.phase", string(p.Status.Phase), snap.Meta.CollectedAt),
			))
		}
	}
	return out
}

func checkWarningEventClusters(snap *snapshot.Snapshot, existing []analyzer.Finding) []analyzer.Finding {
	cutoff := snap.Meta.CollectedAt.Add(-snap.Meta.Lookback)
	type agg struct {
		ref     analyzer.ObjectRef
		count   int32
		reasons map[string]bool
	}
	groups := map[string]*agg{}
	for _, e := range snap.Events {
		if e.Type != corev1.EventTypeWarning || lastSeen(e).Before(cutoff) {
			continue
		}
		ref := analyzer.ObjectRef{
			Kind:      e.InvolvedObject.Kind,
			Namespace: e.InvolvedObject.Namespace,
			Name:      e.InvolvedObject.Name,
		}
		key := ref.String()
		g := groups[key]
		if g == nil {
			g = &agg{ref: ref, reasons: map[string]bool{}}
			groups[key] = g
		}
		c := e.Count
		if c == 0 {
			c = 1
		}
		g.count += c
		g.reasons[e.Reason] = true
	}

	covered := map[string]bool{}
	for _, f := range existing {
		covered[f.Object.String()] = true
	}

	var out []analyzer.Finding
	for key, g := range groups {
		if g.count < 3 || covered[key] {
			continue
		}
		reasons := make([]string, 0, len(g.reasons))
		for r := range g.reasons {
			reasons = append(reasons, r)
		}
		out = append(out, finding(
			"reliability/warning-events", analyzer.SeverityInfo, g.ref,
			"Object has repeated Warning events",
			fmt.Sprintf("%s has %d Warning events in the lookback window (%s)", key, g.count, strings.Join(reasons, ", ")),
			ev("events[type=Warning] grouped by involvedObject", map[string]any{"eventCount": g.count, "reasons": reasons}, snap.Meta.CollectedAt),
		))
	}
	return out
}
```

> Unused-import guard: `appsv1` and `autoscalingv2` are referenced only if you keep the `workloads`/`daemonsetTemplates` builders using them. They are used via `snap.Deployments` (type `[]appsv1.Deployment`) field access, which does **not** require importing `appsv1` in this file. Remove any import the compiler flags as unused; `go vet` in CI will catch leftovers.

- [ ] **Step 4: Wire the new rules into `Analyze`**

In `internal/analyzer/reliability/reliability.go`, replace the `// Task 8 appends...` comment:

```go
	simpleWorkloadRules := []func(*snapshot.Snapshot) []analyzer.Finding{
		checkSingleReplica,
		checkNoResourceLimits,
		checkNoProbes,
		checkNoPDB,
		checkLatestTag,
		checkHPAMaxed,
		checkNodePressure,
		checkPVCUnbound,
	}
	for _, r := range simpleWorkloadRules {
		out = append(out, r(snap)...)
	}
	out = append(out, checkWarningEventClusters(snap, out)...)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/analyzer/reliability/ -race -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/analyzer/reliability/
git commit -m "feat: reliability analyzer — workload, node, PVC, HPA, event rules"
```

---

## Task 9: Reporter — JSON + exit code

**Files:**
- Create: `internal/report/report.go`
- Test: `internal/report/report_test.go`

**Interfaces:**
- Consumes: `analyzer.Finding`, `analyzer.Severity*`, `connector.Status`.
- Produces (package `report`):
  - `type Counts struct { Critical int; Warning int; Info int }`
  - `type Meta struct { Tool string; Version string; GeneratedAt time.Time; Context string; Lookback string; Namespaces []string; Counts Counts }`
  - `type Summary struct { Headline string; Actions []string }` (unused in M1; always nil).
  - `type Report struct { Meta Meta; Connectors []connector.Status; Findings []analyzer.Finding; Summary *Summary }`
  - `func Build(meta Meta, statuses []connector.Status, findings []analyzer.Finding) Report` — sorts findings by: severity rank desc, then `Domain` asc, then `Object.String()` asc, then `RuleID` asc; fills `meta.Counts` from the findings; sets `Tool="poirot"` if empty.
  - `func (r Report) JSON() ([]byte, error)` — `json.MarshalIndent(r, "", "  ")` with a trailing newline.
  - `func (r Report) ExitCode(failOn string) int` — per the Global Constraints disposition table.

- [ ] **Step 1: Write the failing test**

`internal/report/report_test.go`:

```go
package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

func mkF(rule string, sev analyzer.Severity, obj string) analyzer.Finding {
	return analyzer.Finding{RuleID: rule, Domain: "reliability", Severity: sev,
		Object: analyzer.ObjectRef{Kind: "Pod", Namespace: "n", Name: obj}}
}

func TestBuildSortsAndCounts(t *testing.T) {
	findings := []analyzer.Finding{
		mkF("reliability/no-limits", analyzer.SeverityInfo, "b"),
		mkF("reliability/crashloop", analyzer.SeverityCritical, "z"),
		mkF("reliability/oomkilled", analyzer.SeverityWarning, "a"),
	}
	r := Build(Meta{Version: "1.0.0"}, nil, findings)

	require.Equal(t, "reliability/crashloop", r.Findings[0].RuleID)
	require.Equal(t, "reliability/oomkilled", r.Findings[1].RuleID)
	require.Equal(t, "reliability/no-limits", r.Findings[2].RuleID)
	require.Equal(t, Counts{Critical: 1, Warning: 1, Info: 1}, r.Meta.Counts)
	require.Equal(t, "poirot", r.Meta.Tool)
}

func TestExitCode(t *testing.T) {
	crit := Build(Meta{}, nil, []analyzer.Finding{mkF("x", analyzer.SeverityCritical, "a")})
	warn := Build(Meta{}, nil, []analyzer.Finding{mkF("x", analyzer.SeverityWarning, "a")})
	clean := Build(Meta{}, nil, nil)

	require.Equal(t, 2, crit.ExitCode("critical"))
	require.Equal(t, 0, warn.ExitCode("critical"))
	require.Equal(t, 1, warn.ExitCode("warning"))
	require.Equal(t, 2, crit.ExitCode("warning"))
	require.Equal(t, 0, crit.ExitCode("none"))
	require.Equal(t, 0, clean.ExitCode("warning"))
}

func TestJSONRoundTrips(t *testing.T) {
	r := Build(Meta{Version: "1", GeneratedAt: time.Now()},
		[]connector.Status{{Name: "k8s", Availability: connector.Availability{State: connector.StateAvailable}}},
		[]analyzer.Finding{mkF("x", analyzer.SeverityWarning, "a")})
	b, err := r.JSON()
	require.NoError(t, err)

	var back Report
	require.NoError(t, json.Unmarshal(b, &back))
	require.Equal(t, "k8s", back.Connectors[0].Name)
	require.Len(t, back.Findings, 1)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -v`
Expected: FAIL — `undefined: Build`.

- [ ] **Step 3: Write the implementation**

`internal/report/report.go`:

```go
package report

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

type Counts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

type Meta struct {
	Tool        string    `json:"tool"`
	Version     string    `json:"version"`
	GeneratedAt time.Time `json:"generatedAt"`
	Context     string    `json:"context"`
	Lookback    string    `json:"lookback"`
	Namespaces  []string  `json:"namespaces"`
	Counts      Counts    `json:"counts"`
}

type Summary struct {
	Headline string   `json:"headline"`
	Actions  []string `json:"actions"`
}

type Report struct {
	Meta       Meta               `json:"meta"`
	Connectors []connector.Status `json:"connectors"`
	Findings   []analyzer.Finding `json:"findings"`
	Summary    *Summary           `json:"summary,omitempty"`
}

func Build(meta Meta, statuses []connector.Status, findings []analyzer.Finding) Report {
	if meta.Tool == "" {
		meta.Tool = "poirot"
	}

	sorted := append([]analyzer.Finding(nil), findings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.Domain != b.Domain {
			return a.Domain < b.Domain
		}
		if a.Object.String() != b.Object.String() {
			return a.Object.String() < b.Object.String()
		}
		return a.RuleID < b.RuleID
	})

	for _, f := range sorted {
		switch f.Severity {
		case analyzer.SeverityCritical:
			meta.Counts.Critical++
		case analyzer.SeverityWarning:
			meta.Counts.Warning++
		case analyzer.SeverityInfo:
			meta.Counts.Info++
		}
	}

	return Report{Meta: meta, Connectors: statuses, Findings: sorted}
}

func (r Report) JSON() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func (r Report) worst() int {
	switch {
	case r.Meta.Counts.Critical > 0:
		return 2
	case r.Meta.Counts.Warning > 0:
		return 1
	default:
		return 0
	}
}

func (r Report) ExitCode(failOn string) int {
	switch failOn {
	case "none":
		return 0
	case "warning":
		return r.worst()
	default: // "critical"
		if r.worst() == 2 {
			return 2
		}
		return 0
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/report/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/report/report.go internal/report/report_test.go
git commit -m "feat: report JSON assembly, sorting, counts, exit codes"
```

---

## Task 10: Reporter — Markdown

**Files:**
- Create: `internal/report/markdown.go`
- Create: `internal/report/testdata/golden_basic.md`
- Test: `internal/report/markdown_test.go`

**Interfaces:**
- Consumes: `Report` from Task 9.
- Produces (package `report`):
  - `func (r Report) Markdown() ([]byte, error)` — renders a stable Markdown document via a package-level `text/template`. Sections, in order:
    1. `# poirot report` + a bold line: `**<context>** · lookback <lookback> · generated <RFC3339> · poirot <version>`.
    2. `**<n> critical · <n> warning · <n> info**`.
    3. `## Connector coverage` — a table `| Connector | State | Detail |`, one row per `Connectors` entry (Detail = `Reason`/`Detail` joined by `— `, or `-`).
    4. `## Findings` — grouped by `Domain` (alphabetical); within a domain, findings already come severity-sorted from `Build`. Each finding:
       - `### [CRITICAL] <Title> — <Object.String()>`
       - `<Summary>`
       - `Evidence:` then a bullet per evidence item: `` - `<Query>` → <Value as compact JSON> (<Source>) ``
       - if `Analysis != nil`: `Probable cause:` / `Remediation:` / `Confidence:` lines (won't trigger in M1).
       - If there are no findings at all: `No findings. ✅`.
    5. `## Checks skipped` — list any `Finding` with `RuleID` suffix `/skipped` (rendered as `- <Summary>`); if none: `All configured checks ran.`
  - Determinism: no map iteration in template output; pre-sort domains in Go before rendering.

- [ ] **Step 1: Write the failing test**

`internal/report/markdown_test.go`:

```go
package report

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
)

func TestMarkdownGolden(t *testing.T) {
	gen, _ := time.Parse(time.RFC3339, "2026-09-01T10:00:00Z")
	r := Build(
		Meta{Version: "1.0.0", GeneratedAt: gen, Context: "prod", Lookback: "24h"},
		[]connector.Status{
			{Name: "k8s", Availability: connector.Availability{State: connector.StateAvailable}},
			{Name: "promql", Availability: connector.Availability{State: connector.StateAbsent, Reason: "not configured"}},
		},
		[]analyzer.Finding{
			{
				RuleID: "reliability/crashloop", Domain: "reliability", Severity: analyzer.SeverityCritical,
				Title: "Container in CrashLoopBackOff", Summary: "container \"api\" in Pod/payments/api-1 is in CrashLoopBackOff (7 restarts)",
				Object:   analyzer.ObjectRef{Kind: "Pod", Namespace: "payments", Name: "api-1"},
				Evidence: []analyzer.Evidence{{Source: "k8s", Query: "pod.status.containerStatuses[].state.waiting.reason", Value: "CrashLoopBackOff", At: gen}},
			},
		},
	)

	got, err := r.Markdown()
	require.NoError(t, err)

	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.WriteFile("testdata/golden_basic.md", got, 0o644))
	}
	want, err := os.ReadFile("testdata/golden_basic.md")
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

func TestMarkdownNoFindings(t *testing.T) {
	r := Build(Meta{Version: "1.0.0", Context: "dev", Lookback: "24h"}, nil, nil)
	got, err := r.Markdown()
	require.NoError(t, err)
	require.Contains(t, string(got), "No findings. ✅")
	require.Contains(t, string(got), "All configured checks ran.")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/report/ -run TestMarkdown -v`
Expected: FAIL — `r.Markdown undefined`.

- [ ] **Step 3: Write the implementation**

`internal/report/markdown.go`:

```go
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/init-kaushal/poirot/internal/analyzer"
)

type mdDomain struct {
	Name     string
	Findings []analyzer.Finding
}

type mdData struct {
	Meta       Meta
	Connectors []connectorRow
	Domains    []mdDomain
	HasContent bool
	Skipped    []string
}

type connectorRow struct {
	Name   string
	State  string
	Detail string
}

var mdFuncs = template.FuncMap{
	"sevTag": func(s analyzer.Severity) string { return strings.ToUpper(string(s)) },
	"evline": func(e analyzer.Evidence) string {
		v, _ := json.Marshal(e.Value)
		return fmt.Sprintf("`%s` → %s (%s)", e.Query, string(v), e.Source)
	},
}

var mdTmpl = template.Must(template.New("report").Funcs(mdFuncs).Parse(`# poirot report

**{{.Meta.Context}}** · lookback {{.Meta.Lookback}} · generated {{.Meta.GeneratedAt.Format "2006-01-02T15:04:05Z07:00"}} · poirot {{.Meta.Version}}

**{{.Meta.Counts.Critical}} critical · {{.Meta.Counts.Warning}} warning · {{.Meta.Counts.Info}} info**

## Connector coverage

| Connector | State | Detail |
| --- | --- | --- |
{{- range .Connectors}}
| {{.Name}} | {{.State}} | {{.Detail}} |
{{- end}}

## Findings
{{if not .HasContent}}
No findings. ✅
{{else}}{{range .Domains}}
### {{.Name}}
{{range .Findings}}
#### [{{sevTag .Severity}}] {{.Title}} — {{.Object.String}}

{{.Summary}}

Evidence:
{{- range .Evidence}}
- {{evline .}}
{{- end}}
{{if .Analysis}}
Probable cause: {{.Analysis.ProbableCause}}
Remediation: {{.Analysis.Remediation}}
Confidence: {{.Analysis.Confidence}}
{{end}}
{{end}}{{end}}{{end}}
## Checks skipped
{{if .Skipped}}{{range .Skipped}}
- {{.}}
{{- end}}
{{else}}
All configured checks ran.
{{end}}`))

func (r Report) Markdown() ([]byte, error) {
	rows := make([]connectorRow, 0, len(r.Connectors))
	for _, s := range r.Connectors {
		detail := strings.TrimSpace(strings.TrimPrefix(
			strings.Join([]string{s.Availability.Reason, s.Availability.Detail}, " — "), " — "))
		detail = strings.TrimSuffix(strings.TrimPrefix(detail, "— "), " —")
		if detail == "" || detail == "—" {
			detail = "-"
		}
		rows = append(rows, connectorRow{Name: s.Name, State: string(s.Availability.State), Detail: detail})
	}

	byDomain := map[string][]analyzer.Finding{}
	var skipped []string
	for _, f := range r.Findings {
		if strings.HasSuffix(f.RuleID, "/skipped") {
			skipped = append(skipped, f.Summary)
			continue
		}
		byDomain[f.Domain] = append(byDomain[f.Domain], f)
	}
	names := make([]string, 0, len(byDomain))
	for n := range byDomain {
		names = append(names, n)
	}
	sort.Strings(names)
	domains := make([]mdDomain, 0, len(names))
	for _, n := range names {
		domains = append(domains, mdDomain{Name: n, Findings: byDomain[n]})
	}

	data := mdData{
		Meta:       r.Meta,
		Connectors: rows,
		Domains:    domains,
		HasContent: len(domains) > 0,
		Skipped:    skipped,
	}
	var buf bytes.Buffer
	if err := mdTmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
```

- [ ] **Step 4: Generate the golden file, then verify**

Run: `UPDATE_GOLDEN=1 go test ./internal/report/ -run TestMarkdownGolden`
Then inspect `internal/report/testdata/golden_basic.md` by eye — headings present, table well-formed, the CrashLoopBackOff finding rendered under `### reliability`, `## Checks skipped` says `All configured checks ran.`. Fix template whitespace if it looks wrong and regenerate.
Then run without the env var: `go test ./internal/report/ -race -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/report/markdown.go internal/report/markdown_test.go internal/report/testdata/golden_basic.md
git commit -m "feat: Markdown report rendering from canonical report data"
```

---

## Task 11: Orchestrator + `run` command + end-to-end test

**Files:**
- Create: `internal/orchestrator/orchestrator.go`
- Create: `cmd/poirot/run.go`
- Modify: `cmd/poirot/root.go` — attach `newRunCmd(version)`.
- Test: `internal/orchestrator/orchestrator_test.go`
- Test: `cmd/poirot/run_test.go`

**Interfaces:**
- Consumes: `config.Config`, `connector.Registry`, `connector.Status`, `k8s.Connector` + `k8s.Scope`, `analyzer.Analyzer`, `reliability.New()`, `report.Build`/`report.Meta`/`report.Report`.
- Produces (package `orchestrator`):
  - `type K8sSource interface { connector.Connector; Collect(ctx context.Context) (*snapshot.Snapshot, error); ContextName() string }`
  - `type Options struct { Config *config.Config; Version string; K8s K8sSource }` — if `K8s == nil`, build one via `k8s.New` from `Config.Cluster` + `Config.Scope`.
  - `type Result struct { Report report.Report; ExitCode int }`
  - `func Run(ctx context.Context, opts Options) (*Result, error)` — phases:
    1. **Discover:** build registry, register the k8s source, `reg.Probe(ctx)`. If the `k8s` status is not `available`, return an error `"kubernetes API not reachable: <detail>"`.
    2. **Collect:** `opts.K8s.Collect(ctx)`; wrap errors.
    3. **Analyze:** run `[]analyzer.Analyzer{reliability.New()}`. For each, if `!reg.Satisfied(a.Requires())` append a skipped `Finding` (`RuleID = a.ID()+"/skipped"`, `Domain = a.ID()`, `Severity = info`, `Summary = "<id> analysis skipped — missing connector(s): <list>"`) and continue; else call `Analyze` and wrap errors with the analyzer id.
    4. **Synthesize:** build `report.Meta` (`Version`, `GeneratedAt = time.Now()`, `Context = snap.Meta.Context`, `Lookback = Config.Scope.Lookback` string form, `Namespaces = snap.Meta.Namespaces`), `report.Build(...)`, compute `ExitCode` via `Report.ExitCode(Config.Output.FailOn)`.
- Produces (package `main`):
  - `func newRunCmd(version string) *cobra.Command` — flag `-c/--config` (default `poirot.yaml`); `RunE` loads config, calls `orchestrator.Run`, writes `<Output.Dir>/report.json` and `<Output.Dir>/report.md` (creating the dir), prints a one-line summary to stdout, and if `Result.ExitCode != 0` returns `&ExitError{Code: Result.ExitCode}`.
  - `func writeOutputs(dir string, r report.Report) error` (package `main`).

- [ ] **Step 1: Write the failing tests**

`internal/orchestrator/orchestrator_test.go`:

```go
package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
)

func testConfig() *config.Config {
	c := config.Default()
	c.LLM.Provider = "none"
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
```

> `unreachableK8s` is a tiny stub in the test file: implement `Name() string` → `"k8s"`, `Probe` → `connector.Availability{State: connector.StateAbsent, Detail: "dial tcp: refused"}`, `Capabilities` → nil, `Query` → `(nil, nil)`, `Collect` → `(nil, errors.New("unreachable"))`, `ContextName` → `""`. Import `connector` and `snapshot` for the signatures.

`cmd/poirot/run_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunCommandWritesReportsAndExits(t *testing.T) {
	dir := t.TempDir()
	cfg := "cluster:\n  context: does-not-matter\nllm:\n  provider: none\noutput:\n  dir: " + filepath.Join(dir, "out") + "\n"
	cfgPath := filepath.Join(dir, "poirot.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfg), 0o644))

	// Point KUBECONFIG at an empty file so k8s.New fails fast and the command
	// returns an error (there is no cluster in CI). This asserts wiring, not success.
	empty := filepath.Join(dir, "kubeconfig")
	require.NoError(t, os.WriteFile(empty, []byte("apiVersion: v1\nkind: Config\n"), 0o644))
	t.Setenv("KUBECONFIG", empty)

	cmd := newRootCmd("test")
	cmd.SetArgs([]string{"run", "-c", cfgPath})
	err := cmd.Execute()
	require.Error(t, err) // no reachable cluster
}
```

> This test asserts the command is wired and fails cleanly without a cluster. The real success path is covered by `orchestrator_test.go` with a fake clientset. A kind-based e2e is deferred to a follow-up (see note at end).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/orchestrator/ ./cmd/poirot/ -v`
Expected: FAIL — `undefined: Run`, `undefined: newRunCmd`.

- [ ] **Step 3: Write the orchestrator**

`internal/orchestrator/orchestrator.go`:

```go
package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/analyzer/reliability"
	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/connector/k8s"
	"github.com/init-kaushal/poirot/internal/report"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

type K8sSource interface {
	connector.Connector
	Collect(ctx context.Context) (*snapshot.Snapshot, error)
	ContextName() string
}

type Options struct {
	Config  *config.Config
	Version string
	K8s     K8sSource // nil => built from Config
}

type Result struct {
	Report   report.Report
	ExitCode int
}

func Run(ctx context.Context, opts Options) (*Result, error) {
	src := opts.K8s
	if src == nil {
		built, err := k8s.New(
			opts.Config.Cluster.Kubeconfig,
			opts.Config.Cluster.Context,
			k8s.Scope{
				Namespaces: opts.Config.Scope.Namespaces,
				Exclude:    opts.Config.Scope.Exclude,
				Lookback:   time.Duration(opts.Config.Scope.Lookback),
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

	// 3. Analyze
	analyzers := []analyzer.Analyzer{reliability.New()}
	var findings []analyzer.Finding
	for _, a := range analyzers {
		if !reg.Satisfied(a.Requires()) {
			findings = append(findings, analyzer.Finding{
				RuleID:   a.ID() + "/skipped",
				Domain:   a.ID(),
				Severity: analyzer.SeverityInfo,
				Title:    "Analyzer skipped",
				Summary:  fmt.Sprintf("%s analysis skipped — missing connector(s): %s", a.ID(), strings.Join(a.Requires(), ", ")),
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
		Lookback:    time.Duration(opts.Config.Scope.Lookback).String(),
		Namespaces:  snap.Meta.Namespaces,
	}
	rep := report.Build(meta, statuses, findings)
	return &Result{Report: rep, ExitCode: rep.ExitCode(opts.Config.Output.FailOn)}, nil
}
```

- [ ] **Step 4: Write the `run` command**

`cmd/poirot/run.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/init-kaushal/poirot/internal/config"
	"github.com/init-kaushal/poirot/internal/orchestrator"
	"github.com/init-kaushal/poirot/internal/report"
)

func newRunCmd(version string) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Assess a cluster and write report.json + report.md",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			res, err := orchestrator.Run(cmd.Context(), orchestrator.Options{Config: cfg, Version: version})
			if err != nil {
				return err
			}
			if err := writeOutputs(cfg.Output.Dir, res.Report); err != nil {
				return err
			}
			c := res.Report.Meta.Counts
			fmt.Fprintf(cmd.OutOrStdout(),
				"wrote %s/report.json and %s/report.md — %d critical, %d warning, %d info\n",
				cfg.Output.Dir, cfg.Output.Dir, c.Critical, c.Warning, c.Info)
			if res.ExitCode != 0 {
				return &ExitError{Code: res.ExitCode}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&cfgPath, "config", "c", "poirot.yaml", "path to the poirot config file")
	return cmd
}

func writeOutputs(dir string, r report.Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	j, err := r.JSON()
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), j, 0o644); err != nil {
		return err
	}
	m, err := r.Markdown()
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), m, 0o644)
}
```

Then in `cmd/poirot/root.go`, add `root.AddCommand(newRunCmd(version))` alongside the existing `newVersionCmd(version)` line.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./... -race -count=1`
Expected: all packages PASS.

- [ ] **Step 6: Manual smoke test against a real cluster (if one is reachable)**

```bash
make build
cat > /tmp/poirot.yaml <<'EOF'
cluster:
  context: ""     # current context
llm:
  provider: none
output:
  dir: ./poirot-out
EOF
./bin/poirot run -c /tmp/poirot.yaml
cat ./poirot-out/report.md
```

Expected: a report is written; exit code reflects the worst finding (`echo $?`).

- [ ] **Step 7: Commit**

```bash
git add internal/orchestrator/ cmd/poirot/run.go cmd/poirot/root.go cmd/poirot/run_test.go internal/orchestrator/orchestrator_test.go
git commit -m "feat: orchestrator lifecycle and run command — end-to-end M1"
```

---

## Self-Review

**1. Spec coverage (M1 slice):**

| Spec item (M1 milestone) | Task |
|---|---|
| CLI (`run`, `version`), `--cronjob` flag | Tasks 1, 11 — `version` + `run` done. `--cronjob` is a thin wrapper deferred to M6 per the spec table; noted here so the executor doesn't add it now. |
| Config `poirot.yaml`, zero-arg-ish run, `auto` defaults | Task 2 (`-c` defaults to `poirot.yaml`) |
| Connector interface + registry + capability-as-data + JSON-serializable I/O | Task 4 |
| `v1 → v2` disciplines (no connector imports agent, ToolProvider abstraction, schema-as-data) | Tasks 4, 5 — `ToolProvider` itself is an M3 concern; the constraints that enable it (capability data, no reverse imports) are enforced now. |
| Kubernetes connector: workloads, pods, events, nodes, PDBs, HPAs, PVCs, Services | Tasks 5, 6 |
| `reliability` analyzer (full rule list from the catalog) | Tasks 7, 8 — every catalog bullet mapped to a rule ID. PVC "near-full (needs metrics)" is partially deferred: M1 does `pvc-unbound` only; the fill-percentage check lands with the promql connector in M2. |
| Deterministic baseline, `--no-llm` behavior | Whole plan — no LLM code path exists in M1; `Analyze` is pure. |
| Reporter: `report.json` canonical + `report.md` from it | Tasks 9, 10 |
| Exit codes 0/1/2 gated by `failOn` | Task 9 (`ExitCode`), Task 11 (wired) |
| Orchestrator phases discover → collect → analyze → synthesize; partial-failure posture | Task 11 — investigate phase intentionally absent in M1. |
| Skipped-analyzer `info` finding | Task 11 |
| Testing: analyzer fixtures, connector fakes, golden reports, `-race` in CI | Tasks 2–11 + Task 1 CI |

Deferred-and-noted (correct per spec, not gaps): LLM/agent (M3), promql/slo (M2), opencost/cost (M4), alertmanager/gitops (M5), CronJob/RBAC/Dockerfile/openai-compatible (M6), kind e2e job (follow-up to M1).

**2. Placeholder scan:** No `TBD`/`TODO`/"handle edge cases". Two spots give the implementer a real code block plus an explicit correction note (the `metav1.LabelSelector` alias in Task 8, the fake-discovery behavior in Task 5) — these are concrete instructions with the exact replacement code, not placeholders.

**3. Type consistency:** `Finding`/`Evidence`/`Severity`/`ObjectRef`/`Analysis` defined in Task 3, used unchanged in 7–11. `connector.Status`/`Availability`/`State*` from Task 4 used in 9–11. `k8s.Scope` fields (`Namespaces`,`Exclude`,`Lookback time.Duration`) consistent across Tasks 5, 6, 11. `report.Meta`/`Counts`/`Report.ExitCode(string)` consistent between Tasks 9, 10, 11. `orchestrator.Options.K8s` is `K8sSource` (embeds `connector.Connector` + `Collect` + `ContextName`), satisfied by `*k8s.Connector` which has all three. `report.Build(meta, statuses, findings)` signature identical in Tasks 9, 10, 11. `New()` constructors: `reliability.New()` returns `*reliability.Analyzer` implementing `analyzer.Analyzer` — used in Task 11.

---

## After M1

Once M1 is merged, write the next plan: **M2 — `change` analyzer + `promql` connector + `slo` analyzer** (`docs/superpowers/plans/<date>-poirot-m2-metrics-change.md`), following this same structure. The `promql` connector is the first to exercise `Connector.Capabilities()` / `Query()` for real, and the first `auto`-discovery path.
