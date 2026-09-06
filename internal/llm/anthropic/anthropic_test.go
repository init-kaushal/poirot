package anthropic

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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "k", r.Header.Get("x-api-key"))
		require.NotEmpty(t, r.Header.Get("anthropic-version"))
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, float64(0), body["temperature"])
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()

	c, err := New(Options{APIKey: "k", Model: "claude-sonnet-5", BaseURL: srv.URL})
	require.NoError(t, err)
	resp, err := c.Complete(context.Background(), llm.Request{
		System:   "s",
		Messages: []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{{Type: "text", Text: "hi"}}}},
	})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
	require.NotEmpty(t, resp.Blocks)
	require.Equal(t, "text", resp.Blocks[0].Type)
	require.Positive(t, resp.Usage.OutputTokens)
}

func TestCompleteToolUseTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "tool_use_turn"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "tool_use", resp.StopReason)
	var tu *llm.Block
	for i := range resp.Blocks {
		if resp.Blocks[i].Type == "tool_use" {
			tu = &resp.Blocks[i]
		}
	}
	require.NotNil(t, tu)
	require.NotEmpty(t, tu.ToolName)
	require.NotEmpty(t, tu.ToolID)
	require.NotEmpty(t, tu.Input)
}

func TestCompleteRetriesOn429ThenSucceeds(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&n, 1) == 1 {
			w.WriteHeader(429)
			w.Write(fixture(t, "rate_limited"))
			return
		}
		w.Write(fixture(t, "text_turn"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	resp, err := c.Complete(context.Background(), llm.Request{})
	require.NoError(t, err)
	require.Equal(t, "end_turn", resp.StopReason)
	require.Equal(t, int32(2), atomic.LoadInt32(&n))
}

func TestCompleteAuthErrorIsHard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		w.Write(fixture(t, "auth_error"))
	}))
	defer srv.Close()
	c, _ := New(Options{APIKey: "k", Model: "m", BaseURL: srv.URL})
	_, err := c.Complete(context.Background(), llm.Request{})
	require.ErrorContains(t, err, "401")
}

func TestNewRejectsEmpty(t *testing.T) {
	_, err := New(Options{Model: "m"})
	require.Error(t, err)
	_, err = New(Options{APIKey: "k"})
	require.Error(t, err)
}
