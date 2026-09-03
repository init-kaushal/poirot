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
