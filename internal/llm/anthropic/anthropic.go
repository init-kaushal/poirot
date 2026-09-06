// Package anthropic is a hand-rolled, non-streaming client for the Anthropic
// /v1/messages API that implements llm.LLM.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/init-kaushal/poirot/internal/llm"
)

// anthropicVersion is the value sent in the anthropic-version header. Source:
// the claude-api skill (curl/examples.md "Required Headers" table and every
// curl example in the Anthropic docs use "2023-06-01").
const anthropicVersion = "2023-06-01"

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultMaxTokens = 4096
	maxAttempts      = 3
	bodySnippetMax   = 512
)

// Options configures a Client.
type Options struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

// Client talks to the Anthropic Messages API.
type Client struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

var _ llm.LLM = (*Client)(nil)

// New validates o and returns a ready Client. APIKey and Model are required.
func New(o Options) (*Client, error) {
	if o.APIKey == "" {
		return nil, errors.New("anthropic: APIKey is required")
	}
	if o.Model == "" {
		return nil, errors.New("anthropic: Model is required")
	}
	baseURL := o.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{
		apiKey:     o.APIKey,
		model:      o.Model,
		baseURL:    baseURL,
		httpClient: hc,
	}, nil
}

// Model returns the model id this client was configured with.
func (c *Client) Model() string { return c.model }

// Complete performs a single non-streaming completion, retrying transient
// failures (HTTP 429/5xx and 200-body overloaded_error) up to maxAttempts.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	payload, err := buildBody(c.model, req)
	if err != nil {
		return llm.Response{}, fmt.Errorf("anthropic: encoding request: %w", err)
	}
	url := c.baseURL + "/v1/messages"

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		resp, retryable, doErr := c.do(ctx, url, payload)
		if doErr == nil {
			return resp, nil
		}
		lastErr = doErr
		if !retryable || attempt == maxAttempts {
			return llm.Response{}, doErr
		}
		if err := sleep(ctx, backoff(attempt)); err != nil {
			return llm.Response{}, err
		}
	}
	return llm.Response{}, lastErr
}

// backoff returns min(2^attempt * 500ms, 8s).
func backoff(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// do performs one HTTP attempt. The bool reports whether a returned error is
// retryable.
func (c *Client) do(ctx context.Context, url string, payload []byte) (llm.Response, bool, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return llm.Response{}, false, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return llm.Response{}, false, ctxErr
		}
		return llm.Response{}, false, fmt.Errorf("anthropic: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return llm.Response{}, false, fmt.Errorf("anthropic: reading response body: %w", err)
	}

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests, httpResp.StatusCode >= 500:
		return llm.Response{}, true, fmt.Errorf("anthropic: http %d: %s", httpResp.StatusCode, snippet(body))
	case httpResp.StatusCode < 200 || httpResp.StatusCode >= 300:
		return llm.Response{}, false, fmt.Errorf("anthropic: http %d: %s", httpResp.StatusCode, snippet(body))
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return llm.Response{}, false, fmt.Errorf("anthropic: decoding response: %w", err)
	}
	if parsed.Type == "error" {
		errType, errMsg := "", ""
		if parsed.Error != nil {
			errType, errMsg = parsed.Error.Type, parsed.Error.Message
		}
		retryable := errType == "overloaded_error"
		return llm.Response{}, retryable, fmt.Errorf("anthropic: api error %q: %s", errType, errMsg)
	}

	out := llm.Response{
		StopReason: parsed.StopReason,
		Usage: llm.Usage{
			InputTokens:  parsed.Usage.InputTokens,
			OutputTokens: parsed.Usage.OutputTokens,
		},
	}
	for _, b := range parsed.Content {
		switch b.Type {
		case "text":
			out.Blocks = append(out.Blocks, llm.Block{Type: "text", Text: b.Text})
		case "tool_use":
			out.Blocks = append(out.Blocks, llm.Block{
				Type:     "tool_use",
				ToolID:   b.ID,
				ToolName: b.Name,
				Input:    b.Input,
			})
		}
	}
	return out, false, nil
}

func snippet(b []byte) string {
	if len(b) > bodySnippetMax {
		return string(b[:bodySnippetMax])
	}
	return string(b)
}

// --- wire types -------------------------------------------------------------

type apiRequest struct {
	Model       string       `json:"model"`
	System      string       `json:"system,omitempty"`
	MaxTokens   int          `json:"max_tokens"`
	Temperature float64      `json:"temperature"`
	Messages    []apiMessage `json:"messages"`
	Tools       []apiTool    `json:"tools,omitempty"`
}

type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

type apiBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiResponse struct {
	Type       string         `json:"type"`
	Content    []apiRespBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiRespBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func buildBody(model string, req llm.Request) ([]byte, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = defaultMaxTokens
	}
	ar := apiRequest{
		Model:       model,
		System:      req.System,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
	}
	for _, m := range req.Messages {
		am := apiMessage{Role: string(m.Role)}
		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
				am.Content = append(am.Content, apiBlock{Type: "text", Text: b.Text})
			case "tool_use":
				am.Content = append(am.Content, apiBlock{
					Type:  "tool_use",
					ID:    b.ToolID,
					Name:  b.ToolName,
					Input: b.Input,
				})
			case "tool_result":
				am.Content = append(am.Content, apiBlock{
					Type:      "tool_result",
					ToolUseID: b.ToolID,
					Content:   b.Content,
					IsError:   b.IsError,
				})
			}
		}
		ar.Messages = append(ar.Messages, am)
	}
	for _, t := range req.Tools {
		ar.Tools = append(ar.Tools, apiTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		})
	}
	return json.Marshal(ar)
}
