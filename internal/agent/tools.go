package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// snapshotToolSpecs is the fixed-order set of tools backed by the in-memory
// snapshot and analyzer findings. It is prepended to every Tools() result.
var snapshotToolSpecs = []llm.ToolSpec{
	{
		Name:        "snapshot.findings",
		Description: "List the deterministic analyzer findings from this run, optionally filtered by minimum severity, domain, or object.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"severityGte":{"type":"string"},"domain":{"type":"string"},"object":{"type":"string"}},"additionalProperties":false}`),
	},
	{
		Name:        "snapshot.object",
		Description: "Return the spec and status of a Kubernetes object captured in the snapshot, by kind/namespace/name.",
		InputSchema: json.RawMessage(`{"type":"object","required":["kind","name"],"properties":{"kind":{"type":"string"},"namespace":{"type":"string"},"name":{"type":"string"}},"additionalProperties":false}`),
	},
	{
		Name:        "snapshot.events",
		Description: "List the Kubernetes events captured in the snapshot for a given object (kind/namespace/name).",
		InputSchema: json.RawMessage(`{"type":"object","required":["kind","name"],"properties":{"kind":{"type":"string"},"namespace":{"type":"string"},"name":{"type":"string"}},"additionalProperties":false}`),
	},
	{
		Name:        "snapshot.logs",
		Description: "Read the pod logs captured during collection for a pod (current + previously-terminated container).",
		InputSchema: json.RawMessage(`{"type":"object","required":["namespace","pod"],"properties":{"namespace":{"type":"string"},"pod":{"type":"string"},"container":{"type":"string"}},"additionalProperties":false}`),
	},
}

// marshalResult marshals v as the successful (non-error) tool result. A
// marshalling failure is an infrastructure error: (nil, false, err).
func marshalResult(v any) (json.RawMessage, bool, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false, err
	}
	return raw, false, nil
}

// findingView is the shape returned by snapshot.findings.
type findingView struct {
	RuleID   string              `json:"ruleId"`
	Domain   string              `json:"domain"`
	Severity string              `json:"severity"`
	Object   string              `json:"object"`
	Summary  string              `json:"summary"`
	Evidence []analyzer.Evidence `json:"evidence"`
}

func (p *InProcessToolProvider) snapshotFindings(args json.RawMessage) (json.RawMessage, bool, error) {
	var a struct {
		SeverityGte string `json:"severityGte"`
		Domain      string `json:"domain"`
		Object      string `json:"object"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil
		}
	}

	minRank := 0
	if a.SeverityGte != "" {
		minRank = analyzer.Severity(a.SeverityGte).Rank()
	}

	out := make([]findingView, 0, len(p.findings))
	for _, f := range p.findings {
		if minRank > 0 && f.Severity.Rank() < minRank {
			continue
		}
		if a.Domain != "" && f.Domain != a.Domain {
			continue
		}
		if a.Object != "" && f.Object.String() != a.Object && f.Object.Name != a.Object {
			continue
		}
		out = append(out, findingView{
			RuleID:   f.RuleID,
			Domain:   f.Domain,
			Severity: string(f.Severity),
			Object:   f.Object.String(),
			Summary:  f.Summary,
			Evidence: f.Evidence,
		})
	}
	return marshalResult(out)
}

func (p *InProcessToolProvider) snapshotObject(args json.RawMessage) (json.RawMessage, bool, error) {
	var a struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil
	}

	match := func(name, ns string) bool {
		return name == a.Name && (a.Namespace == "" || ns == a.Namespace)
	}
	snap := p.snap

	switch strings.ToLower(a.Kind) {
	case "pod", "pods":
		for i := range snap.Pods {
			if o := snap.Pods[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "deployment", "deployments", "deploy":
		for i := range snap.Deployments {
			if o := snap.Deployments[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "statefulset", "statefulsets", "sts":
		for i := range snap.StatefulSets {
			if o := snap.StatefulSets[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "daemonset", "daemonsets", "ds":
		for i := range snap.DaemonSets {
			if o := snap.DaemonSets[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "replicaset", "replicasets", "rs":
		for i := range snap.ReplicaSets {
			if o := snap.ReplicaSets[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "node", "nodes":
		for i := range snap.Nodes {
			if o := snap.Nodes[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "event", "events":
		for i := range snap.Events {
			if o := snap.Events[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "poddisruptionbudget", "poddisruptionbudgets", "pdb":
		for i := range snap.PDBs {
			if o := snap.PDBs[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "horizontalpodautoscaler", "horizontalpodautoscalers", "hpa":
		for i := range snap.HPAs {
			if o := snap.HPAs[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "persistentvolumeclaim", "persistentvolumeclaims", "pvc":
		for i := range snap.PVCs {
			if o := snap.PVCs[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	case "service", "services", "svc":
		for i := range snap.Services {
			if o := snap.Services[i]; match(o.Name, o.Namespace) {
				o.ManagedFields = nil
				return marshalResult(o)
			}
		}
	}

	return json.RawMessage(fmt.Sprintf("object not found in snapshot: %s %s/%s", a.Kind, a.Namespace, a.Name)), true, nil
}

func (p *InProcessToolProvider) snapshotEvents(args json.RawMessage) (json.RawMessage, bool, error) {
	var a struct {
		Kind      string `json:"kind"`
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil
	}

	out := p.snap.Events[:0:0]
	for _, e := range p.snap.Events {
		io := e.InvolvedObject
		if !strings.EqualFold(io.Kind, a.Kind) || io.Name != a.Name {
			continue
		}
		if a.Namespace != "" && io.Namespace != a.Namespace {
			continue
		}
		out = append(out, e)
	}
	return marshalResult(out)
}

func (p *InProcessToolProvider) snapshotLogs(args json.RawMessage) (json.RawMessage, bool, error) {
	var a struct {
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Container string `json:"container"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil
	}

	key := "Pod/" + a.Namespace + "/" + a.Pod
	chunks, ok := p.snap.PodLogs[key]
	if !ok {
		return json.RawMessage(fmt.Sprintf("no captured logs for %s", key)), true, nil
	}

	if a.Container != "" {
		filtered := make([]snapshot.LogChunk, 0, len(chunks))
		for _, c := range chunks {
			if c.Container == a.Container {
				filtered = append(filtered, c)
			}
		}
		chunks = filtered
	}
	return marshalResult(chunks)
}
