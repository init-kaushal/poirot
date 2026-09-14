# poirot

Point-in-time reliability, cost, and change-risk assessment report for a Kubernetes cluster.
Give it a kubeconfig and a small `poirot.yaml`; run `poirot run`; read `report.md`.

**[→ init-kaushal.github.io/poirot](https://init-kaushal.github.io/poirot/)** — the case file,
with real findings pulled from an actual run.

## Quickstart

```bash
make build
cp poirot.example.yaml poirot.yaml    # then edit cluster.context if needed
./bin/poirot run -c poirot.yaml
cat poirot-out/report.md
```

`poirot run` exits `0` (clean) / `1` (warnings) / `2` (critical), gated by `output.failOn`.

## Try it against a local cluster

```bash
# any local cluster works: kind, k3s, minikube, or `colima start --kubernetes`
kubectl apply -f hack/demo-broken.yaml   # deploys deliberately broken workloads
sleep 60
./bin/poirot run -c poirot.yaml
kubectl delete namespace poirot-demo     # teardown
```

Status: M4 — reliability + slo + change + cost (rightsizing, idle, orphaned resources, spend trend), plus the M3 LLM investigation layer. Cost is measured from OpenCost when reachable, otherwise estimated from resource requests.

The `slo` analyzer needs a PromQL-compatible backend (Prometheus, VictoriaMetrics,
Thanos, Mimir). Set `connectors.promql.url` to `auto` (discovered + reached via the
API-server proxy), an explicit URL, or `disabled`.

## LLM analysis

After the deterministic analyzers run, poirot can enrich warning-or-worse
findings with a probable cause, cross-finding correlations, and an executive
summary. This layer is additive: the deterministic core of `report.json` /
`report.md` is byte-identical whether or not it runs.

- Set the API key: `export POIROT_LLM_API_KEY=...` (the env var name is
  `llm.apiKeyEnv` in `poirot.yaml`).
- Configure the provider in `poirot.yaml`: `llm.provider` is `anthropic`,
  `openai-compatible`, or `none`; set `llm.model` (required unless the provider
  is `none`), and `llm.baseURL` for `openai-compatible`.
- `poirot run --no-llm` forces the deterministic-only report regardless of config.
- With no API key the LLM phase is skipped, and the report notes it
  (`meta.llm.status` and a banner in `report.md`).

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
