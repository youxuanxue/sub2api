package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// tkGeminiDefaultRateLimitResetAt chooses the TokenKey default 429 reset time
// when ParseGeminiRateLimitResetTime finds no explicit delay: OAuth (including
// google_one) uses tier cooldown; API keys use PST midnight (upstream #641).
func (s *GeminiMessagesCompatService) tkGeminiDefaultRateLimitResetAt(
	ctx context.Context,
	account *Account,
	oauthType, tierID, projectID string,
	isCodeAssist bool,
) time.Time {
	// TK: See upstream Wei-Shaw/sub2api#641 —— 反代 Gemini CLI 的
	// google_one OAuth 账号收到 429（无 quotaResetDelay/retryDelay）时，
	// upstream 旧逻辑直接封禁到 PST 午夜，完全忽略 tier 上的 Cooldown
	// 配置（如 google_ai_pro 的 5min）。所有 OAuth 账号（含 google_one /
	// aistudio OAuth / code_assist）都应走 tier cooldown；只有非 OAuth
	// 的 AI Studio API Key 才用 PST 午夜兜底。
	if account.Type == AccountTypeOAuth {
		cooldown := geminiCooldownForTier(tierID)
		if s.rateLimitService != nil {
			cooldown = s.rateLimitService.GeminiCooldown(ctx, account)
		}
		ra := time.Now().Add(cooldown)
		logger.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Account %d (OAuth oauth_type=%s, tier=%s, project=%s, code_assist=%v) rate limited, cooldown=%v", account.ID, oauthType, tierID, projectID, isCodeAssist, time.Until(ra).Truncate(time.Second))
		return ra
	}
	// API Key (AI Studio): PST 午夜
	if ts := nextGeminiDailyResetUnix(); ts != nil {
		ra := time.Unix(*ts, 0)
		logger.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Account %d (API Key, type=%s) rate limited, reset at PST midnight (%v)", account.ID, account.Type, ra)
		return ra
	}
	// 兜底：5 分钟
	ra := time.Now().Add(5 * time.Minute)
	logger.LegacyPrintf("service.gemini_messages_compat", "[Gemini 429] Account %d rate limited, fallback to 5min", account.ID)
	return ra
}
