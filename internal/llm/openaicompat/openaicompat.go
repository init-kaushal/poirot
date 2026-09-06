// Package openaicompat is a hand-rolled, non-streaming client for the
// OpenAI-compatible POST {BaseURL}/chat/completions API that implements
// llm.LLM. It targets OpenAI itself as well as drop-in servers such as
// Ollama, vLLM and LiteLLM.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/init-kaushal/poirot/internal/llm"
)

// chatCompletionsPath is appended to the configured BaseURL. OpenAI and every
// compatible server (Ollama, vLLM, LiteLLM) expose the endpoint at this path.
const chatCompletionsPath = "/chat/completions"

const (
	maxAttempts    = 3
	bodySnippetMax = 512
)

// Options configures a Client.
type Options struct {
	APIKey     string
	Model      string
	BaseURL    string
	HTTPClient *http.Client
}

// Client talks to an OpenAI-compatible /chat/completions endpoint.
type Client struct {
	apiKey     string
	model      string
	baseURL    string
	httpClient *http.Client
}

var _ llm.LLM = (*Client)(nil)

// New validates o and returns a ready Client. BaseURL and Model are required;
// APIKey is optional (local servers such as Ollama need no auth).
func New(o Options) (*Client, error) {
	if o.BaseURL == "" {
		return nil, errors.New("openaicompat: BaseURL is required")
	}
	if o.Model == "" {
		return nil, errors.New("openaicompat: Model is required")
	}
	hc := o.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	return &Client{
		apiKey:     o.APIKey,
		model:      o.Model,
		baseURL:    strings.TrimRight(o.BaseURL, "/"),
		httpClient: hc,
	}, nil
}

// Model returns the model id this client was configured with.
func (c *Client) Model() string { return c.model }

// Complete performs a single non-streaming completion, retrying transient
// failures (HTTP 429/5xx) up to maxAttempts.
func (c *Client) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	payload, err := buildBody(c.model, req)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openaicompat: encoding request: %w", err)
	}
	url := c.baseURL + chatCompletionsPath

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
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("content-type", "application/json")

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return llm.Response{}, false, ctxErr
		}
		return llm.Response{}, false, fmt.Errorf("openaicompat: request failed: %w", err)
	}
	defer httpResp.Body.Close()

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return llm.Response{}, false, fmt.Errorf("openaicompat: reading response body: %w", err)
	}

	switch {
	case httpResp.StatusCode == http.StatusTooManyRequests, httpResp.StatusCode >= 500:
		return llm.Response{}, true, fmt.Errorf("openaicompat: http %d: %s", httpResp.StatusCode, snippet(body))
	case httpResp.StatusCode < 200 || httpResp.StatusCode >= 300:
		return llm.Response{}, false, fmt.Errorf("openaicompat: http %d: %s", httpResp.StatusCode, snippet(body))
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return llm.Response{}, false, fmt.Errorf("openaicompat: decoding response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return llm.Response{}, false, errors.New("openaicompat: response has no choices")
	}
	choice := parsed.Choices[0]

	out := llm.Response{
		StopReason: mapFinishReason(choice.FinishReason),
		Usage: llm.Usage{
			InputTokens:  parsed.Usage.PromptTokens,
			OutputTokens: parsed.Usage.CompletionTokens,
		},
	}
	if choice.Message.Content != "" {
		out.Blocks = append(out.Blocks, llm.Block{Type: "text", Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		out.Blocks = append(out.Blocks, llm.Block{
			Type:     "tool_use",
			ToolID:   tc.ID,
			ToolName: tc.Function.Name,
			Input:    json.RawMessage(tc.Function.Arguments),
		})
	}
	return out, false, nil
}

// mapFinishReason translates an OpenAI finish_reason to an llm.Response
// StopReason. Unknown values pass through unchanged.
func mapFinishReason(r string) string {
	switch r {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return r
	}
}

func snippet(b []byte) string {
	if len(b) > bodySnippetMax {
		return string(b[:bodySnippetMax])
	}
	return string(b)
}

// --- wire types -----------------------------------------------------------

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []chatTool    `json:"tools,omitempty"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatToolCallFunc `json:"function"`
}

type chatToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatToolFunc `json:"function"`
}

type chatToolFunc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// buildBody encodes req into an OpenAI /chat/completions request body.
func buildBody(model string, req llm.Request) ([]byte, error) {
	cr := chatRequest{
		Model:       model,
		Temperature: req.Temperature,
	}
	if req.MaxTokens > 0 {
		cr.MaxTokens = req.MaxTokens
	}

	if req.System != "" {
		cr.Messages = append(cr.Messages, chatMessage{Role: "system", Content: req.System})
	}

	for _, m := range req.Messages {
		var texts []string
		var toolCalls []chatToolCall
		var toolResults []chatMessage

		for _, b := range m.Blocks {
			switch b.Type {
			case "text":
				if b.Text != "" {
					texts = append(texts, b.Text)
				}
			case "tool_use":
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				toolCalls = append(toolCalls, chatToolCall{
					ID:   b.ToolID,
					Type: "function",
					Function: chatToolCallFunc{
						Name:      b.ToolName,
						Arguments: args,
					},
				})
			case "tool_result":
				toolResults = append(toolResults, chatMessage{
					Role:       "tool",
					ToolCallID: b.ToolID,
					Content:    b.Content,
				})
			}
		}

		if len(texts) > 0 {
			cr.Messages = append(cr.Messages, chatMessage{
				Role:    string(m.Role),
				Content: strings.Join(texts, "\n"),
			})
		}
		if len(toolCalls) > 0 {
			cr.Messages = append(cr.Messages, chatMessage{
				Role:      "assistant",
				ToolCalls: toolCalls,
			})
		}
		cr.Messages = append(cr.Messages, toolResults...)
	}

	for _, t := range req.Tools {
		cr.Tools = append(cr.Tools, chatTool{
			Type: "function",
			Function: chatToolFunc{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}

	return json.Marshal(cr)
}
