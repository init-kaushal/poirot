package reliability

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

func ev(query string, value any, at time.Time) analyzer.Evidence {
	return analyzer.Evidence{Source: "k8s", Query: query, Value: value, At: at}
}

func podRef(p corev1.Pod) analyzer.ObjectRef {
	return analyzer.ObjectRef{APIVersion: "v1", Kind: "Pod", Namespace: p.Namespace, Name: p.Name}
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
