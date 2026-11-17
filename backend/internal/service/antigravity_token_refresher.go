package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

const (
	// antigravityRefreshWindow Antigravity token 提前刷新窗口：15分钟
	// Google OAuth token 有效期55分钟，提前15分钟刷新
	antigravityRefreshWindow = 15 * time.Minute

	antigravityForceTokenRefreshExtraKey       = "antigravity_force_token_refresh"
	antigravityForceTokenRefreshReasonExtraKey = "antigravity_force_token_refresh_reason"
	antigravityForceTokenRefreshAtExtraKey     = "antigravity_force_token_refresh_at"
)

// AntigravityTokenRefresher 实现 TokenRefresher 接口
type AntigravityTokenRefresher struct {
	antigravityOAuthService *AntigravityOAuthService
}

func NewAntigravityTokenRefresher(antigravityOAuthService *AntigravityOAuthService) *AntigravityTokenRefresher {
	return &AntigravityTokenRefresher{
		antigravityOAuthService: antigravityOAuthService,
	}
}

// CacheKey 返回用于分布式锁的缓存键
func (r *AntigravityTokenRefresher) CacheKey(account *Account) string {
	return AntigravityTokenCacheKey(account)
}

// CanRefresh 检查是否可以刷新此账户
func (r *AntigravityTokenRefresher) CanRefresh(account *Account) bool {
	return account.Platform == PlatformAntigravity && account.Type == AccountTypeOAuth
}

// NeedsRefresh 检查账户是否需要刷新
// Antigravity 使用固定的15分钟刷新窗口，忽略全局配置
func (r *AntigravityTokenRefresher) NeedsRefresh(account *Account, _ time.Duration) bool {
	if !r.CanRefresh(account) {
		return false
	}
	if accountNeedsAntigravityForceTokenRefresh(account) {
		return true
	}
	expiresAt := account.GetCredentialAsTime("expires_at")
	if expiresAt == nil {
		return false
	}
	timeUntilExpiry := time.Until(*expiresAt)
	needsRefresh := timeUntilExpiry < antigravityRefreshWindow
	if needsRefresh {
		fmt.Printf("[AntigravityTokenRefresher] Account %d needs refresh: expires_at=%s, time_until_expiry=%v, window=%v\n",
			account.ID, expiresAt.Format("2006-01-02 15:04:05"), timeUntilExpiry, antigravityRefreshWindow)
	}
	return needsRefresh
}

func accountNeedsAntigravityForceTokenRefresh(account *Account) bool {
	return account != nil &&
		account.Platform == PlatformAntigravity &&
		account.Type == AccountTypeOAuth &&
		account.getExtraBool(antigravityForceTokenRefreshExtraKey)
}

func antigravityForceTokenRefreshExtra(reason string) map[string]any {
	return map[string]any{
		antigravityForceTokenRefreshExtraKey:       true,
		antigravityForceTokenRefreshReasonExtraKey: reason,
		antigravityForceTokenRefreshAtExtraKey:     time.Now().UTC().Format(time.RFC3339),
	}
}

func clearAntigravityForceTokenRefreshExtra() map[string]any {
	return map[string]any{
		antigravityForceTokenRefreshExtraKey:       false,
		antigravityForceTokenRefreshReasonExtraKey: "",
		antigravityForceTokenRefreshAtExtraKey:     "",
	}
}

// Refresh 执行 token 刷新
func (r *AntigravityTokenRefresher) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	tokenInfo, err := r.antigravityOAuthService.RefreshAccountToken(ctx, account)
	if err != nil {
		return nil, err
	}

	newCredentials := r.antigravityOAuthService.BuildAccountCredentials(tokenInfo)
	// 合并旧的 credentials，保留新 credentials 中不存在的字段
	newCredentials = MergeCredentials(account.Credentials, newCredentials)
	return finalizeAntigravityRefreshCredentials(account, tokenInfo, newCredentials)
}

// finalizeAntigravityRefreshCredentials retains a prior project_id across
// temporary LoadCodeAssist failures, and returns errAntigravityOAuthMissingProjectID
// when the account still has no project_id after a successful token exchange.
func finalizeAntigravityRefreshCredentials(account *Account, tokenInfo *AntigravityTokenInfo, newCredentials map[string]any) (map[string]any, error) {
	if newCredentials == nil {
		newCredentials = map[string]any{}
	}
	// 特殊处理 project_id：如果新值为空但旧值非空，保留旧值
	// 这确保了即使 LoadCodeAssist 失败，project_id 也不会丢失
	if newProjectID, _ := newCredentials["project_id"].(string); newProjectID == "" {
		if oldProjectID := strings.TrimSpace(account.GetCredential("project_id")); oldProjectID != "" {
			newCredentials["project_id"] = oldProjectID
		}
	}

	// LoadCodeAssist 失败可能是临时网络问题。有旧 project_id / antigravity_project_id
	// fallback 时保留并继续；dispatchable project id 仍缺失时返回不可重试错误，
	// 由 TokenRefreshService 写入 status=error（missing_project_id:）。
	// Quarantine must share AntigravityOAuthProjectIDReady with admission.
	if tokenInfo != nil && tokenInfo.ProjectIDMissing {
		view := &Account{
			Platform:    PlatformAntigravity,
			Type:        AccountTypeOAuth,
			Credentials: newCredentials,
		}
		if account != nil {
			view.ID = account.ID
			view.Extra = account.Extra
			// Preserve credential/Extra fallback keys MergeCredentials may not
			// have copied when the refresh payload omitted them.
			if view.Credentials == nil {
				view.Credentials = map[string]any{}
			}
			if _, ok := view.Credentials[antigravityProjectIDFallbackCredentialKey]; !ok {
				if fb := strings.TrimSpace(account.GetCredential(antigravityProjectIDFallbackCredentialKey)); fb != "" {
					view.Credentials[antigravityProjectIDFallbackCredentialKey] = fb
					newCredentials[antigravityProjectIDFallbackCredentialKey] = fb
				}
			}
		}
		if AntigravityOAuthProjectIDReady(view) {
			if account != nil {
				log.Printf("[AntigravityTokenRefresher] Account %d: LoadCodeAssist 临时失败，保留既有 project_id/fallback", account.ID)
			}
			return newCredentials, nil
		}
		if account != nil {
			log.Printf("[AntigravityTokenRefresher] Account %d: LoadCodeAssist 失败且无既有 project_id，标记 missing_project_id", account.ID)
		}
		return newCredentials, errAntigravityOAuthMissingProjectID
	}

	return newCredentials, nil
}
