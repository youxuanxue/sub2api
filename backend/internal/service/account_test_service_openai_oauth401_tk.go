package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

type openAIProbe401RefreshTriedKey struct{}

func markOpenAIProbe401RefreshTried(ctx context.Context) context.Context {
	return context.WithValue(ctx, openAIProbe401RefreshTriedKey{}, true)
}

func openAIProbe401RefreshWasTried(ctx context.Context) bool {
	v, _ := ctx.Value(openAIProbe401RefreshTriedKey{}).(bool)
	return v
}

func (s *AccountTestService) resolveOpenAIGatewayForProbe() *OpenAIGatewayService {
	if s == nil {
		return nil
	}
	if s.openAIGatewayService != nil {
		return s.openAIGatewayService
	}
	return s.openaiGatewayService
}

// tryRefreshOpenAIOAuthProbe401 在管理员/定时探测收到可恢复 401 时强制刷新并重试一次，
// 避免仍可 refresh 的号被 SetError 永久停掉（Wei-Shaw/sub2api#8855）。
func (s *AccountTestService) tryRefreshOpenAIOAuthProbe401(
	c *gin.Context,
	account *Account,
	credentialAccount *Account,
	body []byte,
	modelID string,
	prompt string,
	mode string,
) (retried bool, err error) {
	if s == nil || c == nil || c.Request == nil || account == nil || credentialAccount == nil {
		return false, nil
	}
	ctx := c.Request.Context()
	if openAIProbe401RefreshWasTried(ctx) || !credentialAccount.IsOAuth() {
		return false, nil
	}
	gw := s.resolveOpenAIGatewayForProbe()
	if gw == nil {
		return false, nil
	}
	if _, ok := gw.tryRefreshOpenAI401Token(ctx, credentialAccount, body); !ok {
		return false, nil
	}
	c.Request = c.Request.WithContext(markOpenAIProbe401RefreshTried(ctx))
	return true, s.testOpenAIAccountConnection(c, account, modelID, prompt, mode)
}
