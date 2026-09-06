// Package llm is a minimal provider-neutral chat-with-tools interface.
package llm

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

type Block struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ToolName string          `json:"toolName,omitempty"`
	ToolID   string          `json:"toolId,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
	Content  string          `json:"content,omitempty"`
	IsError  bool            `json:"isError,omitempty"`
}

type Message struct {
	Role   Role    `json:"role"`
	Blocks []Block `json:"blocks"`
}

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Request struct {
	System      string
	Messages    []Message
	Tools       []ToolSpec
	MaxTokens   int
	Temperature float64
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Blocks     []Block
	StopReason string
	Usage      Usage
}

// LLM is a single non-streaming completion with optional tool use.
type LLM interface {
	Complete(ctx context.Context, req Request) (Response, error)
	Model() string
}
