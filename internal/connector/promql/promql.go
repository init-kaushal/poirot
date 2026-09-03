package promql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

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

func (c *Connector) Name() string { return "promql" }

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
			return connector.Availability{State: connector.StateAbsent, Reason: "no metrics Service found"}
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

	if _, err := c.client.Instant(ctx, "vector(1)", time.Now()); err != nil {
		return connector.Availability{State: connector.StateDegraded, Reason: "backend unreachable", Detail: trunc(err.Error(), 200)}
	}
	return connector.Availability{State: connector.StateAvailable}
}

func (c *Connector) Backend() string { return c.backend }

func (c *Connector) Capabilities() []connector.Capability {
	return []connector.Capability{
		{
			ID:          "promql.instant",
			Description: "Run an instant PromQL query. Returns the current value of each matching series.",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["expr"],` +
				`"properties":{"expr":{"type":"string"},"time":{"type":"string","description":"RFC3339; defaults to now"}},` +
				`"additionalProperties":false}`),
		},
		{
			ID:          "promql.range",
			Description: "Run a PromQL range query. Returns the latest value of each matching series over the window.",
			ArgsSchema: json.RawMessage(`{"type":"object","required":["expr"],` +
				`"properties":{"expr":{"type":"string"},"start":{"type":"string"},"end":{"type":"string"},` +
				`"stepSeconds":{"type":"integer","minimum":1}},"additionalProperties":false}`),
		},
	}
}

func (c *Connector) Query(ctx context.Context, id string, args json.RawMessage) (json.RawMessage, error) {
	if c.client == nil {
		return nil, errors.New("promql: connector is not available (probe first)")
	}
	switch id {
	case "promql.instant":
		var a struct {
			Expr string `json:"expr"`
			Time string `json:"time"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		at := time.Now()
		if a.Time != "" {
			t, err := time.Parse(time.RFC3339, a.Time)
			if err != nil {
				return nil, fmt.Errorf("promql: bad time %q: %w", a.Time, err)
			}
			at = t
		}
		s, err := c.client.Instant(ctx, a.Expr, at)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	case "promql.range":
		var a struct {
			Expr        string `json:"expr"`
			Start       string `json:"start"`
			End         string `json:"end"`
			StepSeconds int    `json:"stepSeconds"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		end := time.Now()
		if a.End != "" {
			t, err := time.Parse(time.RFC3339, a.End)
			if err != nil {
				return nil, err
			}
			end = t
		}
		start := end.Add(-15 * time.Minute)
		if a.Start != "" {
			t, err := time.Parse(time.RFC3339, a.Start)
			if err != nil {
				return nil, err
			}
			start = t
		}
		step := time.Duration(a.StepSeconds) * time.Second
		if step <= 0 {
			step = time.Minute
		}
		s, err := c.client.Range(ctx, a.Expr, start, end, step)
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	default:
		return nil, fmt.Errorf("promql: unknown capability %q", id)
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
