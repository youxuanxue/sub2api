package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestIsModelRateLimited(t *testing.T) {
	now := time.Now()
	future := now.Add(10 * time.Minute).Format(time.RFC3339)
	past := now.Add(-10 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		account        *Account
		requestedModel string
		expected       bool
	}{
		{
			name: "official model ID hit - claude-sonnet-4-6",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			expected:       true,
		},
		{
			name: "official model ID hit via mapping - request claude-3-5-sonnet, mapped to claude-sonnet-4-6",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-3-5-sonnet": "claude-sonnet-4-6",
					},
				},
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet",
			expected:       true,
		},
		{
			name: "no rate limit - expired",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": past,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			expected:       false,
		},
		{
			name: "no rate limit - no matching key",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"gemini-3-flash": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			expected:       false,
		},
		{
			name:           "no rate limit - unsupported model",
			account:        &Account{},
			requestedModel: "gpt-4",
			expected:       false,
		},
		{
			name:           "no rate limit - empty model",
			account:        &Account{},
			requestedModel: "",
			expected:       false,
		},
		{
			name: "gemini model hit",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"gemini-3-pro-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-pro-high",
			expected:       true,
		},
		{
			name: "antigravity platform - gemini-3.1-pro mapped to gemini-pro-agent",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						// 2026-06-15 起 gemini-3.1-pro → gemini-pro-agent
						// （gemini-3.1-pro-high 上游目录已无）。
						"gemini-3.8-flash-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-flash-preview",
			expected:       true,
		},
		{
			name: "antigravity platform - gemini family rate limit blocks mapped alias",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						antigravityGeminiModelRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-flash-preview",
			expected:       true,
		},
		{
			name: "antigravity platform - gemini family rate limit does not block claude",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						antigravityGeminiModelRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			expected:       false,
		},
		{
			name: "non-antigravity platform - gemini-3-pro-preview NOT mapped",
			account: &Account{
				Platform: PlatformGemini,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"gemini-3-pro-high": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gemini-3-flash-preview",
			expected:       false, // gemini 平台不走 antigravity 映射
		},
		{
			name: "antigravity platform - account mapping still remaps claude-opus-4-6 to thinking",
			account: &Account{
				Platform: PlatformAntigravity,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-opus-4-6": "claude-opus-4-6-thinking"},
				},
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-opus-4-6-thinking": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-opus-4-6",
			expected:       true,
		},
		{
			name: "no scope fallback - claude_sonnet should not match",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude_sonnet": map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet-20241022",
			expected:       false,
		},
		{
			name: "openai image generation family key blocks image model",
			account: &Account{
				Platform: PlatformOpenAI,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						openAIImageGenerationRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gpt-image-2",
			expected:       true,
		},
		{
			name: "openai image generation family key does not block text model",
			account: &Account{
				Platform: PlatformOpenAI,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						openAIImageGenerationRateLimitKey: map[string]any{
							"rate_limit_reset_at": future,
						},
					},
				},
			},
			requestedModel: "gpt-5.4",
			expected:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.account.isModelRateLimitedWithContext(context.Background(), tt.requestedModel)
			if result != tt.expected {
				t.Errorf("isModelRateLimited(%q) = %v, want %v", tt.requestedModel, result, tt.expected)
			}
		})
	}
}

func TestIsModelRateLimited_GeminiDispatchMappingAffectsModelKey(t *testing.T) {
	now := time.Now()
	future := now.Add(10 * time.Minute).Format(time.RFC3339)

	account := &Account{
		Platform: PlatformGemini,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"gemini-3.1-pro-preview": map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}
	group := &Group{
		ID:       8,
		Platform: PlatformGemini,
		Status:   StatusActive,
		Hydrated: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{
			OpusMappedModel: "gemini-3.1-pro-preview",
		},
	}
	ctx := context.WithValue(context.Background(), ctxkey.Group, group)

	if !account.isModelRateLimitedWithContext(ctx, "claude-opus-4-7") {
		t.Errorf("expected Gemini dispatch mapping to hit upstream model rate limit")
	}
	if account.isModelRateLimitedWithContext(context.Background(), "claude-opus-4-7") {
		t.Errorf("expected no hit without Gemini group context")
	}
}

func TestIsModelRateLimited_UniversalGeminiUsesAccountMapping(t *testing.T) {
	const requested = "claude-opus-4-7"
	group := &Group{
		ID: 8, Platform: PlatformGemini, Status: StatusActive, Hydrated: true,
		MessagesDispatchModelConfig: OpenAIMessagesDispatchModelConfig{OpusMappedModel: "gemini-3.1-pro-preview"},
	}
	directCtx := context.WithValue(context.Background(), ctxkey.Group, group)
	resolver := NewUniversalRoutingResolver(nil)
	universalCtx := resolver.WithRequest(WithUniversalKeyRouting(directCtx), ShapeAnthropicMessages, "/v1/messages", requested, nil)
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeServiceAccount, AccountTypeOAuth} {
		for _, cooledModel := range []string{"gemini-3.1-pro-preview", "gemini-3-flash", requested} {
			t.Run(accountType+"/"+cooledModel, func(t *testing.T) {
				account := &Account{
					Platform: PlatformGemini, Type: accountType,
					Credentials: map[string]any{"model_mapping": map[string]any{requested: "gemini-3-flash"}},
					Extra: map[string]any{modelRateLimitsKey: map[string]any{
						cooledModel: map[string]any{"rate_limit_reset_at": time.Now().Add(10 * time.Minute).Format(time.RFC3339)},
					}},
				}
				wantModel := "gemini-3-flash"
				if accountType == AccountTypeOAuth {
					wantModel = requested
				}
				require.Equal(t, cooledModel == wantModel, account.isModelRateLimitedWithContext(universalCtx, requested))
				require.Equal(t, cooledModel == wantModel, account.GetModelRateLimitRemainingTimeWithContext(universalCtx, requested) > 0)
				require.Equal(t, cooledModel == "gemini-3.1-pro-preview", account.isModelRateLimitedWithContext(directCtx, requested), "Direct retains its existing group dispatch policy")
			})
		}
	}
	require.Same(t, group, universalCtx.Value(ctxkey.Group), "billing and authorization keep the group context")
}

func TestIsModelRateLimited_OpenAIImageGenerationIntentBlocksTextModelImageTool(t *testing.T) {
	future := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	account := &Account{
		Platform: PlatformOpenAI,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				openAIImageGenerationRateLimitKey: map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}

	require.False(t, account.isModelRateLimitedWithContext(context.Background(), "gpt-5.4"))
	require.True(t, account.isModelRateLimitedWithContext(WithOpenAIImageGenerationIntent(context.Background()), "gpt-5.4"))
}

func TestIsModelRateLimited_Antigravity_PreviewAliasUsesHighWireKey(t *testing.T) {
	account := &Account{
		Platform: PlatformAntigravity,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				"gemini-3.8-flash-high": map[string]any{
					"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
					"rate_limit_reset_at": time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if !account.isModelRateLimitedWithContext(context.Background(), "gemini-3-flash-preview") {
		t.Fatal("expected preview alias to honour high wire rate-limit key")
	}
}

func TestGetModelRateLimitRemainingTime(t *testing.T) {
	now := time.Now()
	future10m := now.Add(10 * time.Minute).Format(time.RFC3339)
	future5m := now.Add(5 * time.Minute).Format(time.RFC3339)
	past := now.Add(-10 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		account        *Account
		requestedModel string
		minExpected    time.Duration
		maxExpected    time.Duration
	}{
		{
			name:           "nil account",
			account:        nil,
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "model rate limited - direct hit",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			minExpected:    9 * time.Minute,
			maxExpected:    11 * time.Minute,
		},
		{
			name: "model rate limited - via mapping",
			account: &Account{
				Credentials: map[string]any{
					"model_mapping": map[string]any{
						"claude-3-5-sonnet": "claude-sonnet-4-6",
					},
				},
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet",
			minExpected:    4 * time.Minute,
			maxExpected:    6 * time.Minute,
		},
		{
			name: "expired rate limit",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-sonnet-4-6": map[string]any{
							"rate_limit_reset_at": past,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name:           "no rate limit data",
			account:        &Account{},
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "no scope fallback",
			account: &Account{
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude_sonnet": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-3-5-sonnet-20241022",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "antigravity platform - account mapping still remaps claude-opus-4-6 to thinking",
			account: &Account{
				Platform: PlatformAntigravity,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"claude-opus-4-6": "claude-opus-4-6-thinking"},
				},
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"claude-opus-4-6-thinking": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "claude-opus-4-6",
			minExpected:    4 * time.Minute,
			maxExpected:    6 * time.Minute,
		},
		{
			name: "antigravity platform - gemini family rate limit remaining",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						antigravityGeminiModelRateLimitKey: map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "gemini-3-flash-preview",
			minExpected:    9 * time.Minute,
			maxExpected:    11 * time.Minute,
		},
		{
			name: "antigravity platform - gemini family remaining ignored for claude",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						antigravityGeminiModelRateLimitKey: map[string]any{
							"rate_limit_reset_at": future10m,
						},
					},
				},
			},
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.account.GetModelRateLimitRemainingTimeWithContext(context.Background(), tt.requestedModel)
			if result < tt.minExpected || result > tt.maxExpected {
				t.Errorf("GetModelRateLimitRemainingTime() = %v, want between %v and %v", result, tt.minExpected, tt.maxExpected)
			}
		})
	}
}

func TestGetRateLimitRemainingTime(t *testing.T) {
	now := time.Now()
	future15m := now.Add(15 * time.Minute).Format(time.RFC3339)
	future5m := now.Add(5 * time.Minute).Format(time.RFC3339)

	tests := []struct {
		name           string
		account        *Account
		requestedModel string
		minExpected    time.Duration
		maxExpected    time.Duration
	}{
		{
			name:           "nil account",
			account:        nil,
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
		{
			name: "model rate limited - 15 minutes",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"gemini-3.8-flash-high": map[string]any{
							"rate_limit_reset_at": future15m,
						},
					},
				},
			},
			requestedModel: "gemini-3.8-flash",
			minExpected:    14 * time.Minute,
			maxExpected:    16 * time.Minute,
		},
		{
			name: "only model rate limited",
			account: &Account{
				Platform: PlatformAntigravity,
				Extra: map[string]any{
					modelRateLimitsKey: map[string]any{
						"gemini-3.8-flash-high": map[string]any{
							"rate_limit_reset_at": future5m,
						},
					},
				},
			},
			requestedModel: "gemini-3.8-flash",
			minExpected:    4 * time.Minute,
			maxExpected:    6 * time.Minute,
		},
		{
			name: "neither rate limited",
			account: &Account{
				Platform: PlatformAntigravity,
			},
			requestedModel: "claude-sonnet-4-6",
			minExpected:    0,
			maxExpected:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.account.GetRateLimitRemainingTimeWithContext(context.Background(), tt.requestedModel)
			if result < tt.minExpected || result > tt.maxExpected {
				t.Errorf("GetRateLimitRemainingTime() = %v, want between %v and %v", result, tt.minExpected, tt.maxExpected)
			}
		})
	}
}

func TestIsModelRateLimited_AnthropicFableFamilyKey(t *testing.T) {
	now := time.Now()
	future := now.Add(48 * time.Hour).Format(time.RFC3339)

	account := &Account{
		Platform: PlatformAnthropic,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				anthropicFableRateLimitKey: map[string]any{
					"rate_limit_reset_at": future,
				},
			},
		},
	}

	tests := []struct {
		requestedModel string
		expected       bool
	}{
		{"claude-fable-5", true},
		{"claude-fable-5[1m]", true},      // 家族 key 覆盖变体
		{"Claude-Fable-5-20260601", true}, // 大小写不敏感
		{"claude-sonnet-4-6", false},      // 其他模型不受影响
		{"claude-opus-4-8", false},
	}

	for _, tc := range tests {
		t.Run(tc.requestedModel, func(t *testing.T) {
			got := account.isModelRateLimitedWithContext(context.Background(), tc.requestedModel)
			require.Equal(t, tc.expected, got)
			remaining := account.GetModelRateLimitRemainingTimeWithContext(context.Background(), tc.requestedModel)
			require.Equal(t, tc.expected, remaining > 0)
		})
	}
}

func TestAntigravityImageModelRateLimitStaysOffGeminiFamily(t *testing.T) {
	imageKeys := antigravityModelRateLimitKeys("gemini-3.1-flash-image")
	require.Equal(t, []string{"gemini-3.1-flash-image"}, imageKeys)

	textKeys := antigravityModelRateLimitKeys("gemini-3-flash-preview")
	require.Equal(t, []string{"gemini-3-flash-preview", antigravityGeminiModelRateLimitKey}, textKeys)

	future := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	account := &Account{
		Platform: PlatformAntigravity,
		Extra: map[string]any{
			modelRateLimitsKey: map[string]any{
				antigravityGeminiModelRateLimitKey: map[string]any{"rate_limit_reset_at": future},
			},
		},
	}
	// Use catalog aliases that DefaultAntigravityModelMapping still accepts.
	require.False(t, account.isModelRateLimitedWithContext(context.Background(), "gemini-3.1-flash-image"))
	require.True(t, account.isModelRateLimitedWithContext(context.Background(), "gemini-3-flash-preview"))
}

func TestIsAnthropicFableModel(t *testing.T) {
	require.True(t, isAnthropicFableModel("claude-fable-5"))
	require.True(t, isAnthropicFableModel("claude-fable-5-1"))
	require.True(t, isAnthropicFableModel("claude-fable-5[1m]"))
	require.True(t, isAnthropicFableModel("Claude-Fable-5"))
	require.False(t, isAnthropicFableModel("claude-sonnet-4-6"))
	require.False(t, isAnthropicFableModel(""))
}

// 冷却的 reason 必须能被查出来，而且要按「实际生效的 scope」查 —— scope 不是
// 客户请求的名字，而是映射后的模型或平台级 key。2026-10-23 排查
// gemini-3.7-flash 时，上游 404 钉下的 reason 在每条日志里都看不到，只剩
// remaining=29m，导致把「上游已下架」误判成「容量不足」多次。
func TestModelRateLimitDiagnosisReportsWinningScopeAndReason(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()

	// AG 账号：裸名映射到 -high，冷却钉在映射后的名字上。
	acc := &Account{
		ID:       228,
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": map[string]any{"gemini-3.7-flash": "gemini-3.7-flash-high"},
		},
	}
	setAccountModelRateLimitSnapshot(acc, "gemini-3.7-flash-high", now.Add(29*time.Minute), "antigravity_404_model_unavailable", now)

	remaining, scope, reason := acc.ModelRateLimitDiagnosisForRequest(ctx, "gemini-3.7-flash")
	require.Greater(t, remaining, 20*time.Minute)
	require.Equal(t, "gemini-3.7-flash-high", scope, "scope 必须是映射后的名字，不是请求名")
	require.Equal(t, "antigravity_404_model_unavailable", reason)

	// 按请求名直接取 reason 会落空 —— 这正是旧代码看不到它的原因。
	require.Equal(t, "", acc.modelRateLimitReason("gemini-3.7-flash"))

	// reason 可选：有冷却但没记 reason 时，remaining 仍要报出来。
	bare := &Account{ID: 229, Platform: PlatformAntigravity}
	setAccountModelRateLimitSnapshot(bare, "gemini-4-argon-high", now.Add(10*time.Minute), "", now)
	remaining, scope, reason = bare.ModelRateLimitDiagnosisForRequest(ctx, "gemini-4-argon-high")
	require.Greater(t, remaining, 5*time.Minute)
	require.Equal(t, "gemini-4-argon-high", scope)
	require.Equal(t, "", reason, "没记 reason 时为空，不代表没有冷却")

	// 无冷却：三个返回值都是零值。
	clean := &Account{ID: 230, Platform: PlatformAntigravity}
	remaining, scope, reason = clean.ModelRateLimitDiagnosisForRequest(ctx, "gemini-3.8-flash")
	require.Zero(t, remaining)
	require.Equal(t, "", scope)
	require.Equal(t, "", reason)
}

// 样本渲染带上 reason，不带时退回旧格式。
func TestAppendSelectionFailureRateSampleCarriesReason(t *testing.T) {
	t.Parallel()
	got := appendSelectionFailureRateSample(nil, 228, 29*time.Minute, "antigravity_404_model_unavailable")
	require.Equal(t, []string{"228(29m0s:antigravity_404_model_unavailable)"}, got)

	got = appendSelectionFailureRateSample(nil, 229, 10*time.Minute, "")
	require.Equal(t, []string{"229(10m0s)"}, got)
}

// 诊断函数报出的 remaining 必须与 GetModelRateLimitRemainingTimeWithContext
// 一致，否则同一个冷却会在日志里显示两个不同的剩余时间。首版漏了 Anthropic
// 统一窗口的 class 冷却（TK G4），class 冷却比按模型冷却更长时会少报。
func TestModelRateLimitDiagnosisMatchesRemainingIncludingAnthropicClass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()

	acc := &Account{ID: 301, Platform: PlatformAnthropic}
	classKey := tkAnthropicModelClassScopeKeyForModel("claude-sonnet-4-6")
	require.NotEmpty(t, classKey, "该模型必须有 class scope，否则本测试无意义")

	// 按模型冷却 5 分钟，class 冷却 50 分钟 —— 应报 class 的那个。
	setAccountModelRateLimitSnapshot(acc, "claude-sonnet-4-6", now.Add(5*time.Minute), "429_anthropic_model", now)
	setAccountModelRateLimitSnapshot(acc, classKey, now.Add(50*time.Minute), "429_anthropic_class_window:5h", now)

	remaining, scope, reason := acc.ModelRateLimitDiagnosisForRequest(ctx, "claude-sonnet-4-6")
	want := acc.GetModelRateLimitRemainingTimeWithContext(ctx, "claude-sonnet-4-6")
	require.Equal(t, want.Truncate(time.Second), remaining.Truncate(time.Second),
		"两个入口必须报同一个 remaining")
	require.Equal(t, classKey, scope, "class 冷却更长时 scope 应是 class key")
	require.Equal(t, "429_anthropic_class_window:5h", reason)
}
