// Package agent provides the ToolProvider seam the M3 investigation loop calls
// instead of touching connectors directly, plus its in-process implementation.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/init-kaushal/poirot/internal/analyzer"
	"github.com/init-kaushal/poirot/internal/connector"
	"github.com/init-kaushal/poirot/internal/llm"
	"github.com/init-kaushal/poirot/internal/snapshot"
)

// ToolProvider is the set of tools the investigation loop may call.
//
// Invoke has two failure channels:
//   - isErr == true (err == nil): the tool ran but returned an error the model
//     should see; the loop feeds it back as a tool_result with IsError: true.
//   - err != nil: an infrastructure failure (context cancelled, marshalling
//     bug); the loop aborts.
type ToolProvider interface {
	Tools() []llm.ToolSpec
	Invoke(ctx context.Context, name string, args json.RawMessage) (result json.RawMessage, isErr bool, err error)
}

// InProcessToolProvider serves the snapshot.* reads locally from the collected
// snapshot and analyzer findings, and bridges every other tool call to a
// registered, available connector capability.
type InProcessToolProvider struct {
	reg      *connector.Registry
	snap     *snapshot.Snapshot
	findings []analyzer.Finding
	// allowed is the exact set of tool names Tools() advertises. Invoke rejects
	// anything outside it before any connector dispatch, so the read-only
	// guarantee rests on structure (the advertised capability set) rather than
	// on every connector Query switch staying read-only by convention.
	allowed map[string]bool
}

var _ ToolProvider = (*InProcessToolProvider)(nil)

// NewInProcessToolProvider builds the provider from the connector registry, the
// collected snapshot, and the deterministic analyzer findings for this run.
func NewInProcessToolProvider(reg *connector.Registry, snap *snapshot.Snapshot, findings []analyzer.Finding) *InProcessToolProvider {
	if snap == nil {
		// A nil snapshot must not panic out of Invoke (and thus out of Run):
		// the snapshot.* tools then correctly report "not found" instead.
		snap = &snapshot.Snapshot{}
	}
	p := &InProcessToolProvider{reg: reg, snap: snap, findings: findings}
	p.allowed = make(map[string]bool)
	for _, t := range p.Tools() {
		p.allowed[t.Name] = true
	}
	return p
}

// Tools lists the four snapshot.* tools in fixed order, then one ToolSpec per
// capability of every available connector (registration order, Capabilities()
// order). The result is deterministic.
func (p *InProcessToolProvider) Tools() []llm.ToolSpec {
	out := append([]llm.ToolSpec(nil), snapshotToolSpecs...)
	if p.reg != nil {
		for _, c := range p.reg.Available() {
			for _, capa := range c.Capabilities() {
				out = append(out, llm.ToolSpec{
					Name:        capa.ID,
					Description: capa.Description,
					InputSchema: capa.ArgsSchema,
				})
			}
		}
	}
	return out
}

// Invoke runs one tool call. The context is checked first; snapshot.* tools are
// handled in-package; a dotted name whose prefix is a registered available
// connector is routed to conn.Query; anything else is an unknown tool.
func (p *InProcessToolProvider) Invoke(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}

	// Allow-list gate: only names Tools() advertised are dispatchable. An
	// unadvertised connector capability (e.g. a non-read verb someone adds to a
	// Query switch without exposing it in Capabilities()) is unreachable here.
	if !p.allowed[name] {
		return json.RawMessage("unknown tool: " + name), true, nil
	}

	switch name {
	case "snapshot.findings":
		return p.snapshotFindings(args)
	case "snapshot.object":
		return p.snapshotObject(args)
	case "snapshot.events":
		return p.snapshotEvents(args)
	case "snapshot.logs":
		return p.snapshotLogs(args)
	}

	if prefix, _, ok := strings.Cut(name, "."); ok && p.reg != nil {
		for _, c := range p.reg.Available() {
			if c.Name() == prefix {
				raw, err := c.Query(ctx, name, args)
				if err != nil {
					return json.RawMessage(fmt.Sprintf("tool error: %v", err)), true, nil
				}
				return raw, false, nil
			}
		}
	}

	return json.RawMessage("unknown tool: " + name), true, nil
}
