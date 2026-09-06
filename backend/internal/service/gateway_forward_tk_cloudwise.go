package service

import (
	"context"

	"github.com/gin-gonic/gin"
)

// tkTryForwardCloudwiseAnthropicViaChatCompletions is the Forward() early
// branch for Cloudwise Anthropic accounts that must ride chat completions.
// Returns handled=false when the account is not on that path.
func (s *GatewayService) tkTryForwardCloudwiseAnthropicViaChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
) (result *ForwardResult, handled bool, err error) {
	if account == nil || !shouldForwardCloudwiseAnthropicViaChatCompletions(account, parsed) {
		return nil, false, nil
	}
	result, err = s.forwardCloudwiseAnthropicViaChatCompletions(ctx, c, account, parsed)
	return result, true, err
}
