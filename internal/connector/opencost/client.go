package opencost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type doer func(ctx context.Context, path string, params url.Values) ([]byte, error)

type Client struct{ do doer }

func newClient(do doer) *Client { return &Client{do: do} }

// Allocation issues one /allocation/compute call. accumulate=true collapses the
// window to a single step; step != "" (e.g. "7d") buckets it instead.
func (c *Client) Allocation(ctx context.Context, window, aggregate string, accumulate bool, step string) ([][]Allocation, error) {
	p := url.Values{}
	p.Set("window", window)
	p.Set("aggregate", aggregate)
	p.Set("accumulate", strconv.FormatBool(accumulate))
	if step != "" {
		p.Set("step", step)
	}
	raw, err := c.do(ctx, "allocation/compute", p)
	if err != nil {
		return nil, err
	}
	return ParseAllocation(raw)
}

func httpDoer(base string, hc *http.Client) (doer, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("opencost: %q is not an absolute http(s) URL", base)
	}
	root := strings.TrimRight(base, "/")
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	return func(ctx context.Context, path string, params url.Values) ([]byte, error) {
		full := root + "/" + path + "?" + params.Encode()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
		if err != nil {
			return nil, err
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if rerr != nil {
			return nil, fmt.Errorf("opencost: read %s body: %w", path, rerr)
		}
		if resp.StatusCode/100 != 2 {
			snip := body
			if len(snip) > 512 {
				snip = snip[:512]
			}
			return nil, fmt.Errorf("opencost: GET %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snip)))
		}
		return body, nil
	}, nil
}
