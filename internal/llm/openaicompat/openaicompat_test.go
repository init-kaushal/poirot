package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/init-kaushal/poirot/internal/llm"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	require.NoError(t, err)
	return b
}

func TestCompleteTextTurn(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("content-type"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, err := New(Options{APIKey: "sk-test", Model: "gpt-4o-mini", BaseURL: srv.URL})
	require.NoError(t, err)
	resp, err := c.Complete(context.Background(), llm.Request{
		System:      "you are helpful",
		Temperature: 0.7,
		Messages: []llm.Message{
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: "hi"}}},
		},
	})
	require.NoError(t, err)

	// request shape
	require.Equal(t, "gpt-4o-mini", body["model"])
	require.Equal(t, 0.7, body["temperature"])
	_, hasTools := body["tools"]
	require.False(t, hasTools, "tools key must be omitted when no tools")
	_, hasMaxTokens := body["max_tokens"]
	require.False(t, hasMaxTokens, "max_tokens must be omitted when MaxTokens == 0")
	msgs := body["messages"].([]any)
	first := msgs[0].(map[string]any)
	require.Equal(t, "system", first["role"])
	require.Equal(t, "you are helpful", first["content"])

	// response mapping
	require.Equal(t, "end_turn", resp.StopReason)
	require.Len(t, resp.Blocks, 1)
	require.Equal(t, "text", resp.Blocks[0].Type)
	require.Equal(t, "Hello! How can I help you today?", resp.Blocks[0].Text)
	require.Equal(t, 12, resp.Usage.InputTokens)
	require.Equal(t, 19, resp.Usage.OutputTokens)
}

func TestCompleteNoAuthHeaderWhenKeyEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := r.Header["Authorization"]
		require.False(t, ok, "Authorization header must be absent when APIKey is empty")
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, err := New(Options{Model: "llama3", BaseURL: srv.URL})
	require.NoError(t, err)
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
}

func TestCompleteToolCallTurn(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Write(fixture(t, "tool_call_turn"))
	}))
	defer srv.Close()

	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: "list pods"}}},
			{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:     "tool_use",
				ToolID:   "call_1",
				ToolName: "kubectl_get",
				Input:    json.RawMessage(`{"resource":"pods"}`),
			}}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{
				Type:    "tool_result",
				ToolID:  "call_1",
				Content: "no pods found",
			}}},
		},
		Tools: []llm.ToolSpec{{
			Name:        "kubectl_get",
			Description: "get resources",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"resource":{"type":"string"}}}`),
		}},
	})
	require.NoError(t, err)

	// request shape: assistant tool_calls arguments must be a JSON *string*
	msgs := body["messages"].([]any)
	var toolCallMsg, toolResultMsg map[string]any
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] == "assistant" && mm["tool_calls"] != nil {
			toolCallMsg = mm
		}
		if mm["role"] == "tool" {
			toolResultMsg = mm
		}
	}
	require.NotNil(t, toolCallMsg)
	tc := toolCallMsg["tool_calls"].([]any)[0].(map[string]any)
	require.Equal(t, "call_1", tc["id"])
	require.Equal(t, "function", tc["type"])
	fn := tc["function"].(map[string]any)
	require.Equal(t, "kubectl_get", fn["name"])
	require.Equal(t, `{"resource":"pods"}`, fn["arguments"], "arguments must be a stringified JSON object")

	require.NotNil(t, toolResultMsg)
	require.Equal(t, "call_1", toolResultMsg["tool_call_id"])
	require.Equal(t, "no pods found", toolResultMsg["content"])

	tools := body["tools"].([]any)
	tool0 := tools[0].(map[string]any)
	require.Equal(t, "function", tool0["type"])
	tfn := tool0["function"].(map[string]any)
	require.Equal(t, "kubectl_get", tfn["name"])
	require.Equal(t, "get resources", tfn["description"])
	params := tfn["parameters"].(map[string]any)
	require.Equal(t, "object", params["type"])

	// response mapping
	require.Equal(t, "tool_use", resp.StopReason)
	var tu *llm.Block
	for i := range resp.Blocks {
		if resp.Blocks[i].Type == "tool_use" {
			tu = &resp.Blocks[i]
		}
	}
	require.NotNil(t, tu)
	require.Equal(t, "call_kJ3nR8sd7QpZ01", tu.ToolID)
	require.Equal(t, "kubectl_get", tu.ToolName)
	require.JSONEq(t, `{"resource":"pods","namespace":"default"}`, string(tu.Input))
}

func TestCompleteLengthStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "length_stop"))
	}))
	defer srv.Close()

	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "max_tokens", resp.StopReason)
}

func TestCompleteRetriesOn500ThenSucceeds(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write(fixture(t, "server_error"))
			return
		}
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
	require.Equal(t, int32(2), atomic.LoadInt32(&n), "expected exactly one retry (2 server hits)")
}

func TestCompleteMaxTokensSentWhenPositive(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), llm.Request{MaxTokens: 256})
	require.NoError(t, err)
	require.Equal(t, float64(256), body["max_tokens"])
}

func TestNewValidation(t *testing.T) {
	_, err := New(Options{Model: "m"})
	require.Error(t, err, "empty BaseURL must be rejected")

	_, err = New(Options{BaseURL: "http://x"})
	require.Error(t, err, "empty Model must be rejected")

	c, err := New(Options{Model: "m", BaseURL: "http://x"})
	require.NoError(t, err, "empty APIKey is allowed")
	require.Equal(t, "m", c.Model())
}
