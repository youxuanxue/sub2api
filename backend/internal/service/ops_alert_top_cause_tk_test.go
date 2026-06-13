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
}

func TestOpsTopCauseApplies(t *testing.T) {
	require.True(t, opsTopCauseApplies("upstream_error_rate"))
	require.True(t, opsTopCauseApplies("error_rate"))
	require.False(t, opsTopCauseApplies("success_rate"))
	require.False(t, opsTopCauseApplies("group_available_accounts"))
	require.False(t, opsTopCauseApplies(""))
}
