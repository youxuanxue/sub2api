//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTkIsPoolModeRetryableStatus_Default 锁定 TK 默认集 {401,403,429,503,529}。
func TestTkIsPoolModeRetryableStatus_Default(t *testing.T) {
	for _, sc := range []int{401, 403, 429, 503, 529} {
		require.Truef(t, tkIsPoolModeRetryableStatus(sc), "status %d should be TK pool-retryable by default", sc)
	}
	for _, sc := range []int{200, 400, 402, 404, 408, 422, 500, 501, 502, 504, 505} {
		require.Falsef(t, tkIsPoolModeRetryableStatus(sc), "status %d must NOT be TK pool-retryable by default", sc)
	}
}

// TestAccount_IsPoolModeRetryableStatus_TKDefaultIncludes503And529
// 回归现场（edge us1 整体瞬时 529/503 → prod 单 stub 透出）：未显式配置
// pool_mode_retry_status_codes 的 pool stub，其 503/529 现在默认触发同账号重试
// （= 池内轮换），而 upstream 裸默认 {401,403,429} 不含这两个。
func TestAccount_IsPoolModeRetryableStatus_TKDefaultIncludes503And529(t *testing.T) {
	// 未配置 retry_status_codes 的转发 stub。
	acc := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":   "k",
			"base_url":  "https://api-us1.tokenkey.dev",
			"pool_mode": true,
		},
	}

	// TK 默认新增的两个状态码。
	require.True(t, acc.IsPoolModeRetryableStatus(503), "503 must be retryable under TK default")
	require.True(t, acc.IsPoolModeRetryableStatus(529), "529 must be retryable under TK default")
	// upstream 原有的三个仍然成立。
	require.True(t, acc.IsPoolModeRetryableStatus(401))
	require.True(t, acc.IsPoolModeRetryableStatus(403))
	require.True(t, acc.IsPoolModeRetryableStatus(429))
	// 不该误纳的。
	require.False(t, acc.IsPoolModeRetryableStatus(500))
	require.False(t, acc.IsPoolModeRetryableStatus(502))
	require.False(t, acc.IsPoolModeRetryableStatus(504))
	require.False(t, acc.IsPoolModeRetryableStatus(400))
}

// TestAccount_IsPoolModeRetryableStatus_ExplicitConfigOverridesTKDefault
// per-account 显式配置仍然优先：显式列表覆盖 TK 默认，显式空列表关闭全部。
func TestAccount_IsPoolModeRetryableStatus_ExplicitConfigOverridesTKDefault(t *testing.T) {
	// 显式只配 502 → 502 命中、TK 默认的 529 不再命中。
	explicit := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                      "k",
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{float64(502)},
		},
	}
	require.True(t, explicit.IsPoolModeRetryableStatus(502))
	require.False(t, explicit.IsPoolModeRetryableStatus(529), "explicit list must override TK default")
	require.False(t, explicit.IsPoolModeRetryableStatus(401))

	// 显式空列表 → 关闭全部按状态码触发的同账号重试。
	empty := &Account{
		Type: AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":                      "k",
			"pool_mode":                    true,
			"pool_mode_retry_status_codes": []any{},
		},
	}
	require.False(t, empty.IsPoolModeRetryableStatus(529))
	require.False(t, empty.IsPoolModeRetryableStatus(429))
}
