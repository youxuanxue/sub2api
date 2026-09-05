package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
)

var errCodexClientRestricted = errors.New("codex_cli_only restriction: only codex official clients are allowed")

func isOfficialOpenAICodexUserAgent(ua string) bool {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		return false
	}
	return openai.IsCodexCLIRequest(ua) || openai.IsCodexOfficialClientRequest(ua)
}

func resolveOpenAICodexUserAgent(ctx context.Context, s *OpenAIGatewayService, account *Account, inboundUserAgent string) string {
	if s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		return codexCLIUserAgent
	}
	if account != nil {
		if customUA := strings.TrimSpace(account.GetOpenAIUserAgent()); customUA != "" {
			return customUA
		}
	}
	if isOfficialOpenAICodexUserAgent(inboundUserAgent) {
		return strings.TrimSpace(inboundUserAgent)
	}
	if s != nil && s.settingService != nil {
		if ua := strings.TrimSpace(s.settingService.GetOpenAICodexUserAgent(ctx)); ua != "" {
			return ua
		}
	}
	return codexCLIUserAgent
}

func (s *OpenAIGatewayService) applyOpenAICodexUserAgent(ctx context.Context, req *http.Request, account *Account, inboundUserAgent string) {
	if req == nil || account == nil || !account.IsOpenAIOAuthLike() {
		return
	}
	req.Header.Set("user-agent", resolveOpenAICodexUserAgent(ctx, s, account, inboundUserAgent))
}

func (s *OpenAIGatewayService) enforceCodexClientRestriction(ctx context.Context, c *gin.Context, account *Account, body []byte) error {
	restrictionResult := s.detectCodexClientRestriction(c, account, body)
	apiKeyID := getAPIKeyIDFromContext(c)
	logCodexCLIOnlyDetection(ctx, c, account, apiKeyID, restrictionResult, body)
	if restrictionResult.Enabled && !restrictionResult.Matched {
		MarkOpsClientPolicyDenied(c, OpsClientPolicyDeniedReasonLocalPolicyDenied)
		c.JSON(http.StatusForbidden, gin.H{
			"error": gin.H{
				"type":    "forbidden_error",
				"message": CodexClientRestrictionMessage(restrictionResult),
			},
		})
		return errCodexClientRestricted
	}
	return nil
}
