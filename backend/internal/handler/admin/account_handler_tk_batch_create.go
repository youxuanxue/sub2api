package admin

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// tkBatchCreateAccounts creates accounts from a validated batch payload.
// Privacy setup for Antigravity/OpenAI OAuth runs asynchronously so the
// idempotent admin write path is not blocked by upstream privacy calls.
func (h *AccountHandler) tkBatchCreateAccounts(ctx context.Context, accounts []CreateAccountRequest) (any, error) {
	success := 0
	failed := 0
	results := make([]gin.H, 0, len(accounts))
	// 收集需要异步设置隐私的 OAuth 账号
	var antigravityPrivacyAccounts []*service.Account
	var openaiPrivacyAccounts []*service.Account

	for _, item := range accounts {
		if item.RateMultiplier != nil && *item.RateMultiplier < 0 {
			failed++
			results = append(results, gin.H{
				"name":    item.Name,
				"success": false,
				"error":   "rate_multiplier must be >= 0",
			})
			continue
		}
		// US-024: 单条 Create 走 tkValidateNewAPIAccountCreate，BatchCreate 此前漏调，
		// 导致 newapi 行只能在 service 层被 "channel_type must be > 0" 拦截，错误信息
		// 不一致；同时 channel_type / load_factor 之前未透传，使 newapi 批量创建在
		// service 层 100% 失败。这里补齐验证 + 字段透传。
		if msg := tkValidateNewAPIAccountCreate(item.Platform, item.ChannelType, item.Credentials); msg != "" {
			failed++
			results = append(results, gin.H{
				"name":    item.Name,
				"success": false,
				"error":   msg,
			})
			continue
		}

		// base_rpm 输入校验：负值归零，超过 10000 截断
		sanitizeExtraBaseRPM(item.Extra)

		skipCheck := item.ConfirmMixedChannelRisk != nil && *item.ConfirmMixedChannelRisk

		account, err := h.adminService.CreateAccount(ctx, &service.CreateAccountInput{
			Name:                  item.Name,
			Notes:                 item.Notes,
			Platform:              item.Platform,
			Type:                  item.Type,
			Credentials:           item.Credentials,
			Extra:                 item.Extra,
			ProxyID:               item.ProxyID,
			Concurrency:           item.Concurrency,
			Priority:              item.Priority,
			ChannelType:           item.ChannelType,
			RateMultiplier:        item.RateMultiplier,
			LoadFactor:            item.LoadFactor,
			GroupIDs:              item.GroupIDs,
			ExpiresAt:             item.ExpiresAt,
			AutoPauseOnExpired:    item.AutoPauseOnExpired,
			SkipMixedChannelCheck: skipCheck,
		})
		if err != nil {
			failed++
			results = append(results, gin.H{
				"name":    item.Name,
				"success": false,
				"error":   err.Error(),
			})
			continue
		}
		// 收集需要异步设置隐私的 OAuth 账号
		if account.Type == service.AccountTypeOAuth {
			switch account.Platform {
			case service.PlatformAntigravity:
				antigravityPrivacyAccounts = append(antigravityPrivacyAccounts, account)
			case service.PlatformOpenAI:
				openaiPrivacyAccounts = append(openaiPrivacyAccounts, account)
			}
		}
		// OpenAI APIKey 账号异步探测 /v1/responses 能力。
		h.scheduleProtocolCapabilityProbes(account)
		h.scheduleGrokImportProbe(account)
		success++
		results = append(results, gin.H{
			"name":    item.Name,
			"id":      account.ID,
			"success": true,
		})
	}

	// 异步设置隐私，避免批量创建时阻塞请求
	adminSvc := h.adminService
	if len(antigravityPrivacyAccounts) > 0 {
		accounts := antigravityPrivacyAccounts
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("batch_create_antigravity_privacy_panic", "recover", r)
				}
			}()
			bgCtx := context.Background()
			for _, acc := range accounts {
				adminSvc.ForceAntigravityPrivacy(bgCtx, acc)
			}
		}()
	}
	if len(openaiPrivacyAccounts) > 0 {
		accounts := openaiPrivacyAccounts
		go func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("batch_create_openai_privacy_panic", "recover", r)
				}
			}()
			bgCtx := context.Background()
			for _, acc := range accounts {
				adminSvc.ForceOpenAIPrivacy(bgCtx, acc)
			}
		}()
	}

	return gin.H{
		"success": success,
		"failed":  failed,
		"results": results,
	}, nil
}
