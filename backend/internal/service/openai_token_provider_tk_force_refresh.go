package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// ForceRefresh 在上游已明确拒绝当前 access_token 时强制刷新（忽略 3min skew）。
// 成功后清掉本地 cache，避免下一跳再命中被拒的 token。
func (p *OpenAITokenProvider) ForceRefresh(ctx context.Context, account *Account) (*Account, error) {
	if p == nil {
		return account, errors.New("openai token provider is nil")
	}
	if account == nil {
		return nil, errors.New("account is nil")
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return account, errors.New("not an openai oauth account")
	}
	if account.IsOpenAIPersonalAccessToken() {
		return account, errors.New("personal access token cannot be force-refreshed")
	}
	if strings.TrimSpace(account.GetOpenAIRefreshToken()) == "" {
		return account, errors.New("refresh_token missing")
	}
	if p.refreshAPI == nil || p.executor == nil {
		return account, errors.New("oauth refresh api is not configured")
	}

	if p.tokenCache != nil {
		if err := p.tokenCache.DeleteAccessToken(ctx, OpenAITokenCacheKey(account)); err != nil {
			slog.Warn("openai_token_force_refresh_cache_delete_failed",
				"account_id", account.ID, "error", err)
		}
	}

	result, err := p.refreshAPI.RefreshNow(ctx, account, p.executor)
	if err != nil {
		return account, err
	}
	if result == nil {
		return account, errors.New("empty oauth refresh result")
	}
	if result.LockHeld {
		return account, errors.New("oauth refresh lock held")
	}
	if result.Account != nil {
		account = result.Account
	}
	if strings.TrimSpace(account.GetOpenAIAccessToken()) == "" {
		return account, errors.New("force refresh did not produce access_token")
	}
	return account, nil
}

// tkIsPermanentOpenAIAuth401 是 OpenAI 永久认证失败的单一判定。
// HandleUpstreamError 与请求内 RefreshNow 必须共用，避免一边刷新一边停号。
func tkIsPermanentOpenAIAuth401(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(extractUpstreamErrorCode(body))) {
	case "token_invalidated", "token_revoked":
		return true
	}
	return gjson.GetBytes(body, "detail").String() == "Unauthorized"
}

// tkIsRecoverableOpenAI401 识别「当前 access_token 被拒、但 refresh_token 仍可能救活」的 401。
// 能力缺失 / 永久吊销必须继续走既有惩罚路径。
func tkIsRecoverableOpenAI401(statusCode int, body []byte) bool {
	if statusCode != http.StatusUnauthorized {
		return false
	}
	if tkIsCapabilityScope401(statusCode, body) {
		return false
	}
	return !tkIsPermanentOpenAIAuth401(body)
}
