//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TK (us7 P0 2026-06-13): the self-diagnosing 主因 line. These pin the
// formatting an operator reads on the Feishu card to tell a real fire from
// client noise.
func TestFormatOpsTopCause(t *testing.T) {
	t.Run("us7 real shape: access-gated model dominates", func(t *testing.T) {
		got := formatOpsTopCause([]*OpsTopErrorCause{
			{Model: "claude-fable-5", ErrorOwner: "provider", UpstreamStatus: 404, Count: 34},
		})
		require.Equal(t, "claude-fable-5 ×34（upstream 404 / provider）", got)
	})

	t.Run("top-2 joined, capped at two even if more passed", func(t *testing.T) {
		got := formatOpsTopCause([]*OpsTopErrorCause{
			{Model: "claude-fable-5", ErrorOwner: "provider", UpstreamStatus: 404, Count: 34},
			{Model: "claude-opus-4-8", ErrorOwner: "provider", UpstreamStatus: 529, Count: 3},
			{Model: "claude-sonnet-4-6", ErrorOwner: "provider", UpstreamStatus: 500, Count: 1},
		})
		require.Equal(t, "claude-fable-5 ×34（upstream 404 / provider） · claude-opus-4-8 ×3（upstream 529 / provider）", got)
	})

	t.Run("no upstream status falls back to owner only", func(t *testing.T) {
		got := formatOpsTopCause([]*OpsTopErrorCause{
			{Model: "gpt-5.4", ErrorOwner: "client", UpstreamStatus: 0, Count: 7},
		})
		require.Equal(t, "gpt-5.4 ×7（client）", got)
	})

	t.Run("blank model/owner default safely", func(t *testing.T) {
		got := formatOpsTopCause([]*OpsTopErrorCause{
			{Model: "", ErrorOwner: "", UpstreamStatus: 502, Count: 2},
		})
		require.Equal(t, "(unknown) ×2（upstream 502 / unknown）", got)
	})

	t.Run("empty input is empty string", func(t *testing.T) {
		require.Equal(t, "", formatOpsTopCause(nil))
	})
}

func TestBuildOpsFeishuAlertTextTopCauseLine(t *testing.T) {
	rule := &OpsAlertRule{Name: "上游错误率极高", MetricType: "upstream_error_rate", Operator: ">", Threshold: 20, Severity: "P0"}
	mv := 48.57

	t.Run("renders 主因 line when top_cause present", func(t *testing.T) {
		ev := &OpsAlertEvent{
			MetricValue: &mv,
			Dimensions:  map[string]any{"top_cause": "claude-fable-5 ×34（upstream 404 / provider）"},
		}
		out := buildOpsFeishuAlertText(rule, ev, "us7", "")
		require.Contains(t, out, "**主因**")
		require.Contains(t, out, "claude-fable-5 ×34")
		require.Contains(t, out, "upstream 404 / provider")
	})

	t.Run("omits 主因 line when absent", func(t *testing.T) {
		ev := &OpsAlertEvent{MetricValue: &mv}
		out := buildOpsFeishuAlertText(rule, ev, "us7", "")
		require.NotContains(t, out, "主因")
	})

	// Routing-rejection P0s carry the client concentration as top_cause_users.
	// It renders on its OWN line under 主因 (the 换行 readability fix), so a long
	// "anthropic ×339 · newapi ×4 ｜ 用户 #1 …" no longer wraps as one blob.
	t.Run("renders 用户 on its own line under 主因 when top_cause_users present", func(t *testing.T) {
		ev := &OpsAlertEvent{
			MetricValue: &mv,
			Dimensions: map[string]any{
				"top_cause":       "anthropic ×339 · newapi ×4",
				"top_cause_users": `#1 "eval-harness" ×275 · #16 "mobile-app" ×29`,
			},
		}
		out := buildOpsFeishuAlertText(rule, ev, "prod", "")
		require.Contains(t, out, "**主因**：anthropic ×339 · newapi ×4")
		require.Contains(t, out, `**用户**：#1 "eval-harness" ×275`)
		// the 用户 breakdown must sit on its own line, immediately after 主因
		require.Contains(t, out, "anthropic ×339 · newapi ×4\n**用户**：")
	})

	t.Run("renders 模型 on its own line when top_cause_models present", func(t *testing.T) {
		ev := &OpsAlertEvent{
			MetricValue: &mv,
			Dimensions: map[string]any{
				"top_cause":        "anthropic ×339",
				"top_cause_models": "claude-sonnet-4-5 ×180 · claude-opus-4-8 ×49 · qwen3-coder-plus ×12",
			},
		}
		out := buildOpsFeishuAlertText(rule, ev, "prod", "")
		require.Contains(t, out, "**主因**：anthropic ×339")
		require.Contains(t, out, "**模型**：claude-sonnet-4-5 ×180 · claude-opus-4-8 ×49 · qwen3-coder-plus ×12")
		require.Contains(t, out, "anthropic ×339\n**模型**：")
	})

	// No top_cause_users dimension (non-rejection rules, or a degraded user query)
	// => no 用户 line. (Edge nodes never reach this renderer at all — the whole
	// routing-rejection alert is suppressed upstream in maybeSendAlertNotifications.)
	t.Run("omits 用户 line when top_cause_users absent", func(t *testing.T) {
		ev := &OpsAlertEvent{
			MetricValue: &mv,
			Dimensions:  map[string]any{"top_cause": "anthropic ×216 · kiro ×3"},
		}
		out := buildOpsFeishuAlertText(rule, ev, "prod", "")
		require.Contains(t, out, "**主因**：anthropic ×216 · kiro ×3")
		require.NotContains(t, out, "**用户**")
	})

	// Best-effort degradation: if the pool sub-query fails but the user sub-query
	// succeeds (computeTopCause guarantees one failing must not drop the other),
	// the evaluator stashes top_cause_users alone. The card then shows WHO without
	// WHICH — still useful, and must render cleanly (用户 line, no orphan 主因).
	t.Run("renders 用户 line alone when 主因 absent (pool-query degraded)", func(t *testing.T) {
		ev := &OpsAlertEvent{
			MetricValue: &mv,
			Dimensions:  map[string]any{"top_cause_users": `#1 "eval-harness" ×275`},
		}
		out := buildOpsFeishuAlertText(rule, ev, "prod", "")
		require.Contains(t, out, `**用户**：#1 "eval-harness" ×275`)
		require.NotContains(t, out, "**主因**")
	})
}

func TestFormatUserVisibleFailureAffectedShowsAPIKeyRoutingMode(t *testing.T) {
	got := formatUserVisibleFailureAffected([]*OpsUserVisibleFailureUser{
		{
			UserID:            16,
			UserEmail:         "compute@tk.com",
			APIKeyName:        "benchmark组-赵欣宇",
			APIKeyRoutingMode: RoutingModeUniversal,
			GroupName:         "GPT专线",
			Count:             171,
		},
		{
			UserID:            17,
			UserEmail:         "ops@tk.com",
			APIKeyName:        "direct-bench",
			APIKeyRoutingMode: RoutingModeDirect,
			Count:             20,
		},
		{
			UserID:     18,
			UserEmail:  "deleted@tk.com",
			APIKeyName: "deleted-key",
			Count:      3,
		},
	})

	require.Contains(t, got, `#16 compute@tk.com ×171（key "benchmark组-赵欣宇" / universal key / group GPT专线）`)
	require.Contains(t, got, `#17 ops@tk.com ×20（key "direct-bench" / direct key）`)
	require.Contains(t, got, `#18 deleted@tk.com ×3（key "deleted-key"）`)
}

func TestFormatRoutingRejectionByModel(t *testing.T) {
	t.Run("top three requested models", func(t *testing.T) {
		require.Equal(t,
			"claude-sonnet-4-5 ×120 · claude-opus-4-8 ×32 · qwen3-coder-plus ×4",
			formatRoutingRejectionByModel([]*OpsRoutingRejectionModel{
				{Model: "claude-sonnet-4-5", Count: 120},
				{Model: "claude-opus-4-8", Count: 32},
				{Model: "qwen3-coder-plus", Count: 4},
				{Model: "gpt-5.1", Count: 1},
			}))
	})

	t.Run("blank model defaults safely and non-positive counts skipped", func(t *testing.T) {
		require.Equal(t, "(unknown) ×5", formatRoutingRejectionByModel([]*OpsRoutingRejectionModel{
			{Model: "skip", Count: 0},
			{Model: "", Count: 5},
		}))
	})

	t.Run("markdown in client-requested model is neutralized", func(t *testing.T) {
		got := formatRoutingRejectionByModel([]*OpsRoutingRejectionModel{
			{Model: "[claude-sonnet](http://evil)", Count: 12},
		})
		require.NotContains(t, got, "[")
		require.NotContains(t, got, "](")
		require.NotContains(t, got, "http://evil")
		require.Contains(t, got, "claude-sonnet")
		require.Contains(t, got, "×12")
	})
}

func TestOpsTopCauseApplies(t *testing.T) {
	require.True(t, opsTopCauseApplies("upstream_error_rate"))
	require.True(t, opsTopCauseApplies("error_rate"))
	require.False(t, opsTopCauseApplies("success_rate"))
	require.False(t, opsTopCauseApplies("group_available_accounts"))
	require.False(t, opsTopCauseApplies(""))
	// routing_capacity_rejection_count has its OWN cause path (by platform, not
	// the model/owner breakdown), so it must NOT be in opsTopCauseApplies.
	require.False(t, opsTopCauseApplies("routing_capacity_rejection_count"))
}

// TestFormatRoutingRejectionByPlatform pins the empty-pool P0 card's joint 主因
// line: each platform pool that ran out of capacity, with its top contributing
// users nested inline (user id + operator-assigned api-key NAME, never the secret).
// This is the platform→user attribution the old two marginal lines could not
// express. Example names here are synthetic.
func TestFormatRoutingRejectionByPlatform(t *testing.T) {
	t.Run("joint per-platform breakdown with nested users", func(t *testing.T) {
		require.Equal(t,
			`anthropic ×40（#1 "eval-harness" ×30 · #16 "mobile-app" ×10） · newapi ×8（#16 "ci-runner" ×8）`,
			formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
				{Platform: "anthropic", Count: 40, Users: []*OpsRoutingRejectionUser{
					{UserID: 1, APIKeyName: "eval-harness", Count: 30},
					{UserID: 16, APIKeyName: "mobile-app", Count: 10},
				}},
				{Platform: "newapi", Count: 8, Users: []*OpsRoutingRejectionUser{
					{UserID: 16, APIKeyName: "ci-runner", Count: 8},
				}},
			}))
	})

	t.Run("platform with no attributable users renders bare", func(t *testing.T) {
		require.Equal(t, "anthropic ×40", formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "anthropic", Count: 40},
		}))
	})

	t.Run("missing key name falls back to user id only", func(t *testing.T) {
		require.Equal(t, "anthropic ×5（#9 ×5）", formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "anthropic", Count: 5, Users: []*OpsRoutingRejectionUser{{UserID: 9, APIKeyName: "", Count: 5}}},
		}))
	})

	t.Run("capped at two platforms, three users each", func(t *testing.T) {
		require.Equal(t,
			`anthropic ×9（#1 "a" ×4 · #2 "b" ×3 · #3 "c" ×2） · openai ×5`,
			formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
				{Platform: "anthropic", Count: 9, Users: []*OpsRoutingRejectionUser{
					{UserID: 1, APIKeyName: "a", Count: 4},
					{UserID: 2, APIKeyName: "b", Count: 3},
					{UserID: 3, APIKeyName: "c", Count: 2},
					{UserID: 4, APIKeyName: "d", Count: 1},
				}},
				{Platform: "openai", Count: 5},
				{Platform: "gemini", Count: 3},
			}))
	})

	t.Run("blank platform defaults safely", func(t *testing.T) {
		require.Equal(t, "(unknown) ×5", formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "", Count: 5},
		}))
	})

	t.Run("non-positive platform and user counts skipped", func(t *testing.T) {
		require.Equal(t, "anthropic ×40（#7 ×4）", formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "openai", Count: 0},
			{Platform: "anthropic", Count: 40, Users: []*OpsRoutingRejectionUser{
				{UserID: 3, APIKeyName: "x", Count: 0},
				{UserID: 7, APIKeyName: "", Count: 4},
			}},
		}))
	})

	t.Run("long key name is truncated to 24 runes", func(t *testing.T) {
		got := formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "anthropic", Count: 3, Users: []*OpsRoutingRejectionUser{
				{UserID: 5, APIKeyName: "this-is-a-very-long-api-key-name-that-should-truncate", Count: 3},
			}},
		})
		require.Equal(t, `anthropic ×3（#5 "this-is-a-very-long-api-…" ×3）`, got)
	})

	t.Run("empty input is empty string", func(t *testing.T) {
		require.Equal(t, "", formatRoutingRejectionByPlatform(nil))
	})

	// SECURITY: the api-key name is user-controlled and rendered in an operator's
	// lark_md P0 card, where escapeFeishuText only handles & < >. A markdown link in
	// the name would otherwise render as a clickable phishing link. It must be
	// defanged. (Payloads below are synthetic.)
	t.Run("markdown link in key name is neutralized (no phishing injection)", func(t *testing.T) {
		got := formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "anthropic", Count: 30, Users: []*OpsRoutingRejectionUser{
				{UserID: 42, APIKeyName: "[free credits](http://evil)", Count: 30},
			}},
		})
		require.NotContains(t, got, "[", "link-open bracket must be defanged")
		require.NotContains(t, got, "](", "lark_md link syntax must not survive")
		require.Contains(t, got, "#42")
		require.Contains(t, got, "×30")
	})

	t.Run("emphasis/code/table markers defanged (no card-layout bleed)", func(t *testing.T) {
		got := formatRoutingRejectionByPlatform([]*OpsRoutingRejectionPlatform{
			{Platform: "anthropic", Count: 5, Users: []*OpsRoutingRejectionUser{
				{UserID: 7, APIKeyName: "promo*bold*_it_`code`|x", Count: 5},
			}},
		})
		for _, bad := range []string{"*", "`", "_", "|"} {
			require.NotContains(t, got, bad, "markdown marker %q must be defanged", bad)
		}
		require.Contains(t, got, "#7")
	})
}
