package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/init-kaushal/poirot/internal/connector"
)

var _ connector.Connector = (*Connector)(nil)

const (
	logTailDefault = 100
	logTailMax     = 200
	logByteCap     = 256 * 1024
)

// Capabilities returns the connector's read-only queryable capabilities, in a
// fixed order so callers (and the M3 agent's tool list) are stable.
func (c *Connector) Capabilities() []connector.Capability {
	return []connector.Capability{
		{
			ID:          "k8s.get",
			Description: "Fetch one Kubernetes object (spec + status) by kind/namespace/name.",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["kind","name"],` +
				`"properties":{"kind":{"type":"string"},"namespace":{"type":"string"},"name":{"type":"string"}},` +
				`"additionalProperties":false}`),
		},
		{
			ID:          "k8s.logs",
			Description: "Read recent log lines from a pod container (optionally the previously-terminated container).",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["namespace","pod"],` +
				`"properties":{"namespace":{"type":"string"},"pod":{"type":"string"},"container":{"type":"string"},` +
				`"previous":{"type":"boolean"},"tailLines":{"type":"integer","default":100,"maximum":200}},` +
				`"additionalProperties":false}`),
		},
	}
}

// Query executes a read-only capability by ID.
func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	switch id {
	case "k8s.get":
		var a struct {
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return c.k8sGet(ctx, a.Kind, a.Namespace, a.Name)

	case "k8s.logs":
		var a struct {
			Namespace string `json:"namespace"`
			Pod       string `json:"pod"`
			Container string `json:"container"`
			Previous  bool   `json:"previous"`
			TailLines int    `json:"tailLines"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		text, err := c.podLogs(ctx, a.Namespace, a.Pod, a.Container, a.Previous, a.TailLines)
		if err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{
			"pod":       a.Pod,
			"container": a.Container,
			"previous":  a.Previous,
			"lines":     text,
		})

	default:
		return nil, fmt.Errorf("k8s: unknown capability %q", id)
	}
}

// k8sGet fetches a single typed object, strips server-side apply bookkeeping
// (managedFields), and returns it as JSON. Every branch uses a read verb (Get).
func (c *Connector) k8sGet(ctx context.Context, kind, ns, name string) (json.RawMessage, error) {
	opts := metav1.GetOptions{}
	switch strings.ToLower(kind) {
	case "pod":
		obj, err := c.cs.CoreV1().Pods(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "deployment":
		obj, err := c.cs.AppsV1().Deployments(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "replicaset":
		obj, err := c.cs.AppsV1().ReplicaSets(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "statefulset":
		obj, err := c.cs.AppsV1().StatefulSets(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "daemonset":
		obj, err := c.cs.AppsV1().DaemonSets(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "node":
		obj, err := c.cs.CoreV1().Nodes().Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "event":
		obj, err := c.cs.CoreV1().Events(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "poddisruptionbudget", "pdb":
		obj, err := c.cs.PolicyV1().PodDisruptionBudgets(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "horizontalpodautoscaler", "hpa":
		obj, err := c.cs.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "service", "svc":
		obj, err := c.cs.CoreV1().Services(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	case "persistentvolumeclaim", "pvc":
		obj, err := c.cs.CoreV1().PersistentVolumeClaims(ns).Get(ctx, name, opts)
		if err != nil {
			return nil, err
		}
		obj.ManagedFields = nil
		return json.Marshal(obj)
	default:
		return nil, fmt.Errorf("k8s.get: unsupported kind %q", kind)
	}
}

// podLogs reads recent log lines from a pod container and returns them
// truncated to the last tail lines and a 256 KiB cap. It is the shared helper
// used by the k8s.logs capability and Task 5's collect phase. tail is clamped
// to [1, 200] with a default of 100 when non-positive.
func (c *Connector) podLogs(ctx context.Context, ns, pod, container string, previous bool, tail int) (string, error) {
	if tail <= 0 {
		tail = logTailDefault
	}
	if tail > logTailMax {
		tail = logTailMax
	}
	raw, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, &corev1.PodLogOptions{
		Container: container,
		Previous:  previous,
		TailLines: int64Ptr(int64(tail)),
	}).DoRaw(ctx)
	if err != nil {
		return "", fmt.Errorf("k8s.logs: %w", err)
	}
	text, _ := truncateLog(string(raw), tail)
	return text, nil
}

// truncateLog keeps only the last tail lines of text and caps the result at
// 256 KiB (keeping the tail of the byte stream). It returns the possibly
// truncated text and a bool that is true when either limit fired; when true the
// text is prefixed with "…(truncated)\n".
func truncateLog(text string, tail int) (string, bool) {
	truncated := false

	trailingNewline := strings.HasSuffix(text, "\n")
	body := strings.TrimSuffix(text, "\n")
	lines := strings.Split(body, "\n")
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
		truncated = true
	}
	out := strings.Join(lines, "\n")
	if trailingNewline {
		out += "\n"
	}

	if len(out) > logByteCap {
		out = out[len(out)-logByteCap:]
		truncated = true
	}

	if truncated {
		out = "…(truncated)\n" + out
	}
	return out, truncated
}

func int64Ptr(i int64) *int64 { return &i }
