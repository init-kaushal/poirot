# poirot

Point-in-time reliability, cost, and change-risk assessment report for a Kubernetes cluster.
Give it a kubeconfig and a small `poirot.yaml`; run `poirot run`; read `report.md`.

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

Status: M1 — reliability report only, no LLM.
