package agent

import (
	"strings"
	"testing"

	"github.com/init-kaushal/poirot/internal/llm"
)

func TestBudgetToolCallExceeded(t *testing.T) {
	b := &Budget{MaxToolCalls: 2, MaxTokens: 100}

	// First ToolCall should not exceed
	b.ToolCall()
	exceeded, reason := b.Exceeded()
	if exceeded {
		t.Errorf("after 1 ToolCall: expected not exceeded, got exceeded with reason: %s", reason)
	}

	// Second ToolCall should exceed with "tool" in reason
	b.ToolCall()
	exceeded, reason = b.Exceeded()
	if !exceeded {
		t.Errorf("after 2 ToolCalls (max 2): expected exceeded, got not exceeded")
	}
	if !strings.Contains(strings.ToLower(reason), "tool") {
		t.Errorf("expected reason to mention 'tool', got: %s", reason)
	}
}

func TestBudgetTokenExceeded(t *testing.T) {
	b := &Budget{MaxToolCalls: 0, MaxTokens: 100}

	// Add usage that totals 110 tokens (60 + 50)
	b.Add(llm.Usage{InputTokens: 60, OutputTokens: 50})

	exceeded, reason := b.Exceeded()
	if !exceeded {
		t.Errorf("expected exceeded with 110 tokens (max 100), got not exceeded")
	}
	if !strings.Contains(strings.ToLower(reason), "token") {
		t.Errorf("expected reason to mention 'token', got: %s", reason)
	}

	// Verify Used() returns correct counts
	calls, tokens := b.Used()
	if calls != 0 {
		t.Errorf("expected 0 tool calls, got %d", calls)
	}
	if tokens != 110 {
		t.Errorf("expected 110 tokens, got %d", tokens)
	}
}

func TestBudgetNoLimits(t *testing.T) {
	// Both zero caps mean "no limit"
	b := &Budget{}

	b.ToolCall()
	b.ToolCall()
	b.Add(llm.Usage{InputTokens: 999})

	exceeded, reason := b.Exceeded()
	if exceeded {
		t.Errorf("with zero limits: expected not exceeded, got exceeded with reason: %s", reason)
	}

	calls, tokens := b.Used()
	if calls != 2 {
		t.Errorf("expected 2 tool calls, got %d", calls)
	}
	if tokens != 999 {
		t.Errorf("expected 999 tokens, got %d", tokens)
	}
}

func TestBudgetToolCheckBeforeToken(t *testing.T) {
	// When both limits are exceeded, tool check should be reported first
	b := &Budget{MaxToolCalls: 1, MaxTokens: 50}

	b.ToolCall()
	b.Add(llm.Usage{InputTokens: 30, OutputTokens: 30}) // 60 tokens, exceeds 50

	exceeded, reason := b.Exceeded()
	if !exceeded {
		t.Errorf("expected exceeded, got not exceeded")
	}
	// Since ToolCall already exceeded (1 >= 1), we should get tool error first
	if !strings.Contains(strings.ToLower(reason), "tool") {
		t.Errorf("expected reason to mention 'tool' (checked first), got: %s", reason)
	}
}

func TestBudgetExceededWithZeroCapMeansNoLimit(t *testing.T) {
	// Test that zero cap really means no limit (not immediate exceed)
	b := &Budget{MaxToolCalls: 0, MaxTokens: 0}

	// Do a lot of calls and tokens
	for i := 0; i < 100; i++ {
		b.ToolCall()
	}
	b.Add(llm.Usage{InputTokens: 10000, OutputTokens: 20000})

	exceeded, reason := b.Exceeded()
	if exceeded {
		t.Errorf("with zero limits (no limit): expected not exceeded, got exceeded with reason: %s", reason)
	}

	calls, tokens := b.Used()
	if calls != 100 {
		t.Errorf("expected 100 tool calls, got %d", calls)
	}
	if tokens != 30000 {
		t.Errorf("expected 30000 tokens, got %d", tokens)
	}
}
