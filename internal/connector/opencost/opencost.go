package opencost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"k8s.io/client-go/kubernetes"

	"github.com/init-kaushal/poirot/internal/connector"
)

var _ connector.Connector = (*Connector)(nil)

type Options struct {
	URL        string
	Clientset  kubernetes.Interface
	Namespaces []string
}

type Connector struct {
	opts    Options
	client  *Client
	backend string
}

func New(o Options) *Connector { return &Connector{opts: o} }

func (c *Connector) Name() string { return "opencost" }

func (c *Connector) Probe(ctx context.Context) connector.Availability {
	switch c.opts.URL {
	case "disabled":
		return connector.Availability{State: connector.StateAbsent, Reason: "disabled in config"}
	case "auto":
		tgt, err := Discover(ctx, c.opts.Clientset, c.opts.Namespaces)
		if err != nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "service discovery failed", Detail: trunc(err.Error(), 200)}
		}
		if tgt == nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "no OpenCost service found"}
		}
		c.client = newClient(proxyDoer(c.opts.Clientset, *tgt))
		c.backend = "svc:" + tgt.Namespace + "/" + tgt.Name
	default:
		d, err := httpDoer(c.opts.URL, nil)
		if err != nil {
			return connector.Availability{State: connector.StateAbsent, Reason: "bad url", Detail: trunc(err.Error(), 200)}
		}
		c.client = newClient(d)
		c.backend = sanitizeURL(c.opts.URL)
	}

	if _, err := c.client.Allocation(ctx, "1d", "namespace", true, ""); err != nil {
		return connector.Availability{State: connector.StateAbsent, Reason: "backend unreachable", Detail: trunc(err.Error(), 200)}
	}
	return connector.Availability{State: connector.StateAvailable}
}

func (c *Connector) Backend() string { return c.backend }

func (c *Connector) Capabilities() []connector.Capability {
	return []connector.Capability{
		{
			ID:          "opencost.allocation",
			Description: "Fetch OpenCost cost allocations for a window, aggregated by the given key.",
			ArgsSchema:  json.RawMessage(`{"type":"object","required":["window","aggregate"],"properties":{"window":{"type":"string"},"aggregate":{"type":"string"},"accumulate":{"type":"boolean"},"step":{"type":"string"}},"additionalProperties":false}`),
		},
	}
}

func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("opencost: connector is not available (probe first)")
	}
	switch id {
	case "opencost.allocation":
		var a struct {
			Window     string `json:"window"`
			Aggregate  string `json:"aggregate"`
			Accumulate bool   `json:"accumulate"`
			Step       string `json:"step"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		steps, err := c.client.Allocation(ctx, a.Window, a.Aggregate, a.Accumulate, a.Step)
		if err != nil {
			return nil, err
		}
		return json.Marshal(steps)
	default:
		return nil, fmt.Errorf("opencost: unknown capability %q", id)
	}
}

func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
