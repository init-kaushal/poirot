package promql

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/snapshot"
)

type doer func(ctx context.Context, path string, params url.Values) ([]byte, error)

type Client struct{ do doer }

func newClient(do doer) *Client { return &Client{do: do} }

func (c *Client) Instant(ctx context.Context, expr string, at time.Time) ([]snapshot.MetricSample, error) {
	p := url.Values{}
	p.Set("query", expr)
	p.Set("time", strconv.FormatInt(at.Unix(), 10))
	raw, err := c.do(ctx, "api/v1/query", p)
	if err != nil {
		return nil, err
	}
	return parseInstant(raw)
}

func (c *Client) Range(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]snapshot.MetricSample, error) {
	p := url.Values{}
	p.Set("query", expr)
	p.Set("start", strconv.FormatInt(start.Unix(), 10))
	p.Set("end", strconv.FormatInt(end.Unix(), 10))
	p.Set("step", strconv.Itoa(int(step.Seconds())))
	raw, err := c.do(ctx, "api/v1/query_range", p)
	if err != nil {
		return nil, err
	}
	return parseRange(raw)
}

func httpDoer(base string, hc *http.Client) (doer, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("promql: %q is not an absolute http(s) URL", base)
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 != 2 {
			snip := body
			if len(snip) > 512 {
				snip = snip[:512]
			}
			return nil, fmt.Errorf("promql: GET %s → %d: %s", path, resp.StatusCode, strings.TrimSpace(string(snip)))
		}
		return body, nil
	}, nil
}
