# poirot

Point-in-time reliability, cost, and change-risk assessment report for a Kubernetes cluster.
Give it a kubeconfig and a small `poirot.yaml`; run `poirot run`; read `report.md`.

## Quickstart

```bash
make build
./bin/poirot run -c poirot.yaml
```

Status: M1 — reliability report only, no LLM.
