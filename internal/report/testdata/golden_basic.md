# poirot report

**prod** · lookback 24h · generated 2026-09-01T10:00:00Z · poirot 1.0.0

**1 critical · 0 warning · 0 info**

## Connector coverage

| Connector | State | Detail |
| --- | --- | --- |
| k8s | available | - |
| promql | absent | not configured |

## Findings

### reliability

#### [CRITICAL] Container in CrashLoopBackOff — Pod/payments/api-1

container "api" in Pod/payments/api-1 is in CrashLoopBackOff (7 restarts)

Evidence:
- `pod.status.containerStatuses[].state.waiting.reason` → "CrashLoopBackOff" (k8s)

## Checks skipped

All configured checks ran.
