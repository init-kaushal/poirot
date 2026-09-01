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
