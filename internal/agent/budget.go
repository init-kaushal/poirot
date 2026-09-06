package agent

import (
	"fmt"

	"github.com/init-kaushal/poirot/internal/llm"
)

// Budget tracks tool-call and token usage per group, enforcing caps.
type Budget struct {
	MaxToolCalls int
	MaxTokens    int

	// Unexported running counters.
	calls  int
	tokens int
}

// Add accumulates the input and output tokens from a usage record.
func (b *Budget) Add(u llm.Usage) {
	b.tokens += u.InputTokens + u.OutputTokens
}

// ToolCall increments the tool-call counter.
func (b *Budget) ToolCall() {
	b.calls++
}

// Exceeded reports whether either cap has been exceeded.
// Tool-call limit is checked first. Zero cap means "no limit".
func (b *Budget) Exceeded() (bool, string) {
	// Check tool-call limit first (if non-zero).
	if b.MaxToolCalls > 0 && b.calls >= b.MaxToolCalls {
		return true, fmt.Sprintf("tool-call limit exceeded: %d >= %d", b.calls, b.MaxToolCalls)
	}

	// Check token limit (if non-zero).
	if b.MaxTokens > 0 && b.tokens >= b.MaxTokens {
		return true, fmt.Sprintf("token limit exceeded: %d >= %d", b.tokens, b.MaxTokens)
	}

	return false, ""
}

// Used returns the current running counters for tool calls and accumulated tokens.
func (b *Budget) Used() (toolCalls, tokens int) {
	return b.calls, b.tokens
}
