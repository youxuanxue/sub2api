//go:build unit

package repository

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBuildSchedulerMetadataAccount_KeepsOpenAIWSFlags(t *testing.T) {
	account := service.Account{
		ID:       42,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"openai_oauth_responses_websockets_v2_enabled": true,
			"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModePassthrough,
			"openai_ws_force_http":                         true,
			"mixed_scheduling":                             true,
			"unused_large_field":                           "drop-me",
		},
	}

	got := buildSchedulerMetadataAccount(account)

	require.Equal(t, true, got.Extra["openai_oauth_responses_websockets_v2_enabled"])
	require.Equal(t, service.OpenAIWSIngressModePassthrough, got.Extra["openai_oauth_responses_websockets_v2_mode"])
	require.Equal(t, true, got.Extra["openai_ws_force_http"])
	require.Equal(t, true, got.Extra["mixed_scheduling"])
	require.Nil(t, got.Extra["unused_large_field"])
}

// 回归保护：调度快照必须保留 privacy_mode 字段。
// 缺失会导致 Account.IsPrivacySet() 永远返回 false，
// 凡是开启 require_privacy_set 的分组都会卡住所有 OpenAI/Antigravity 账号
// （SetError("Privacy not set, required by group ...") 反复触发）。
func TestBuildSchedulerMetadataAccount_KeepsPrivacyMode(t *testing.T) {
	t.Run("openai_training_off", func(t *testing.T) {
		account := service.Account{
			ID:       1,
			Platform: service.PlatformOpenAI,
			Extra: map[string]any{
				"privacy_mode": service.PrivacyModeTrainingOff,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.PrivacyModeTrainingOff, got.Extra["privacy_mode"])
		require.True(t, got.IsPrivacySet(),
			"meta account 必须能够通过 IsPrivacySet 检查；privacy_mode 被白名单过滤会触发 ‘Privacy not set’ 死循环")
	})

	t.Run("antigravity_privacy_set", func(t *testing.T) {
		account := service.Account{
			ID:       2,
			Platform: service.PlatformAntigravity,
			Extra: map[string]any{
				"privacy_mode": service.AntigravityPrivacySet,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.AntigravityPrivacySet, got.Extra["privacy_mode"])
		require.True(t, got.IsPrivacySet())
	})

	t.Run("training_set_failed_remains_unset", func(t *testing.T) {
		account := service.Account{
			ID:       3,
			Platform: service.PlatformOpenAI,
			Extra: map[string]any{
				"privacy_mode": service.PrivacyModeFailed,
			},
		}

		got := buildSchedulerMetadataAccount(account)

		require.Equal(t, service.PrivacyModeFailed, got.Extra["privacy_mode"])
		require.False(t, got.IsPrivacySet(),
			"非 training_off 的 privacy_mode 仍应被识别为未设置，避免误放行")
	})
}
