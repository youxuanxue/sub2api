//go:build unit

package service

// Unit tests for the Opus 4.7+ thinking-type-adaptive rectifier and the
// model-aware RectifyThinkingBudget change.
//
// Background: Opus 4.7/4.8 reject manual thinking (`thinking:{type:"enabled",
// budget_tokens:N}`) with a 400 and only accept `{type:"adaptive"}` (Anthropic
// docs; CC issue anthropics/claude-code#61348). The gateway reactively repairs
// that 400 on the direct Anthropic path. These tests lock the decision logic
// (the pure functions); the loop wiring in GatewayService.Forward is a
// line-for-line clone of the already-shipped budget-rectifier block.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOpus47OrNewer(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-7", true},
		{"claude-opus-4-7-20260115", true},
		{"claude-opus-4.7", true},
		{"claude-opus-4-8", true},
		{"claude-opus-5-0", true},
		{"claude-opus-4-6", false},          // deprecated but functional → must NOT match
		{"claude-opus-4-5-20251101", false}, // older opus
		{"claude-sonnet-4-7", false},        // sonnet still accepts enabled
		{"claude-sonnet-4-6", false},
		{"claude-haiku-4-5-20251001", false},
		{"", false},
		{"gpt-5.4", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			require.Equal(t, tc.want, isOpus47OrNewer(tc.model))
		})
	}
}

func TestIsThinkingTypeAdaptiveRequiredError(t *testing.T) {
	// The exact production 400 message (CC issue #61348).
	realMsg := `"thinking.type.enabled" is not supported for this model. Use "thinking.type.adaptive" and "output_config.effort" to control thinking behavior.`
	budgetMsg := `thinking.budget_tokens: Input should be greater than or equal to 1024`

	cases := []struct {
		name string
		msg  string
		want bool
	}{
		{"real opus-4.7 adaptive-required 400", realMsg, true},
		{"anti-collision: budget-constraint 400 is NOT adaptive", budgetMsg, false},
		{"unrelated invalid_request 400", "temperature: Extra inputs are not permitted", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isThinkingTypeAdaptiveRequiredError(tc.msg))
		})
	}

	// Symmetric anti-collision: the budget classifier must reject the adaptive message.
	require.False(t, isThinkingBudgetConstraintError(realMsg),
		"adaptive-required message must NOT be classified as a budget-constraint error")
	require.True(t, isThinkingBudgetConstraintError(budgetMsg))
}

func TestRectifyThinkingTypeAdaptive(t *testing.T) {
	t.Run("enabled converts to adaptive and drops budget_tokens", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-8","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`)
		out, applied := RectifyThinkingTypeAdaptive(body)
		require.True(t, applied)
		require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
		require.False(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists(), "budget_tokens must be removed for adaptive")
		// Surgical edit must not disturb sibling fields.
		require.Equal(t, "claude-opus-4-8", gjson.GetBytes(out, "model").String())
		require.Equal(t, int64(1024), gjson.GetBytes(out, "max_tokens").Int())
		require.Equal(t, "hi", gjson.GetBytes(out, "messages.0.content").String())
	})

	t.Run("already adaptive is a no-op", func(t *testing.T) {
		body := []byte(`{"thinking":{"type":"adaptive"}}`)
		out, applied := RectifyThinkingTypeAdaptive(body)
		require.False(t, applied)
		require.Equal(t, body, out)
	})

	t.Run("no thinking field is a no-op", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-8","messages":[]}`)
		out, applied := RectifyThinkingTypeAdaptive(body)
		require.False(t, applied)
		require.Equal(t, body, out)
	})
}

func TestRectifyThinkingBudget_ModelAware(t *testing.T) {
	t.Run("opus-4.7+ budget constraint produces adaptive, no budget_tokens", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-7","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":100}}`)
		out, changed := RectifyThinkingBudget(body, "claude-opus-4-7")
		require.True(t, changed)
		require.Equal(t, "adaptive", gjson.GetBytes(out, "thinking.type").String())
		require.False(t, gjson.GetBytes(out, "thinking.budget_tokens").Exists())
		// max_tokens floor still honored so a tiny client value doesn't choke thinking.
		require.GreaterOrEqual(t, gjson.GetBytes(out, "max_tokens").Int(), int64(BudgetRectifyMinMaxTokens))
	})

	t.Run("opus-4.6 keeps legacy enabled+32000 behavior (still accepted upstream)", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-4-6","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":100}}`)
		out, changed := RectifyThinkingBudget(body, "claude-opus-4-6")
		require.True(t, changed)
		require.Equal(t, "enabled", gjson.GetBytes(out, "thinking.type").String())
		require.Equal(t, int64(BudgetRectifyBudgetTokens), gjson.GetBytes(out, "thinking.budget_tokens").Int())
	})

	t.Run("sonnet keeps legacy enabled+32000 behavior", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"thinking":{"type":"enabled","budget_tokens":100}}`)
		out, changed := RectifyThinkingBudget(body, "claude-sonnet-4-5")
		require.True(t, changed)
		require.Equal(t, "enabled", gjson.GetBytes(out, "thinking.type").String())
		require.Equal(t, int64(BudgetRectifyBudgetTokens), gjson.GetBytes(out, "thinking.budget_tokens").Int())
		require.Equal(t, int64(BudgetRectifyMaxTokens), gjson.GetBytes(out, "max_tokens").Int())
	})

	t.Run("already adaptive is skipped regardless of model", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-5","thinking":{"type":"adaptive"}}`)
		out, changed := RectifyThinkingBudget(body, "claude-sonnet-4-5")
		require.False(t, changed)
		require.Equal(t, body, out)
	})
}
