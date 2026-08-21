package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// tryRefreshOpenAI401Token 在 OpenAI OAuth 上游 401 后强制刷新并返回新 token。
// 成功时调用方应 continue 用新 token 重试当前请求，且不得先 temp-unschedule。
func (s *OpenAIGatewayService) tryRefreshOpenAI401Token(ctx context.Context, account *Account, respBody []byte) (string, bool) {
	if s == nil || account == nil || s.openAITokenProvider == nil {
		return "", false
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return "", false
	}
	if !tkIsRecoverableOpenAI401(http.StatusUnauthorized, respBody) {
		return "", false
	}

	refreshed, err := s.openAITokenProvider.ForceRefresh(ctx, account)
	if err != nil || refreshed == nil {
		slog.Warn("openai_oauth_401_force_refresh_failed",
			"account_id", account.ID,
			"error", err)
		return "", false
	}
	if refreshed != account && refreshed.Credentials != nil {
		account.Credentials = refreshed.Credentials
	}

	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil || strings.TrimSpace(token) == "" {
		slog.Warn("openai_oauth_401_force_refresh_token_reload_failed",
			"account_id", account.ID,
			"error", err)
		return "", false
	}
	slog.Info("openai_oauth_401_force_refresh_retry", "account_id", account.ID)
	return token, true
}

// recoverOpenAIWS401AccessToken 是 WS 握手 401 的唯一注入。它复用
// tryRefreshOpenAI401Token，并把新 token 写回 Authorization，避免 HTTP / WS
// 各写一份刷新逻辑。
func (s *OpenAIGatewayService) recoverOpenAIWS401AccessToken(
	ctx context.Context,
	account *Account,
	statusCode int,
	respBody []byte,
	headers http.Header,
) (http.Header, string, bool) {
	if statusCode != http.StatusUnauthorized {
		return headers, "", false
	}
	token, ok := s.tryRefreshOpenAI401Token(ctx, account, respBody)
	if !ok {
		return headers, "", false
	}
	refreshed := cloneHeader(headers)
	if refreshed == nil {
		refreshed = make(http.Header)
	}
	refreshed.Set("Authorization", "Bearer "+token)
	return refreshed, token, true
}
