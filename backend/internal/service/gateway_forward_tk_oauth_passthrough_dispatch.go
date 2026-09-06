package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// tkTryForwardAnthropicPassthrough covers API-key and OAuth Anthropic
// passthrough early exits from Forward(). Returns handled=false when neither
// passthrough mode applies. Both branches must stay ahead of canonical /
// mimic / fingerprint rewrite.
func (s *GatewayService) tkTryForwardAnthropicPassthrough(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (result *ForwardResult, handled bool, err error) {
	if account == nil {
		return nil, false, nil
	}
	if account.IsAnthropicAPIKeyPassthroughEnabled() {
		passthroughBody := parsed.Body.Bytes()
		passthroughModel := parsed.Model
		if passthroughModel != "" {
			if mappedModel := account.GetMappedModel(passthroughModel); mappedModel != passthroughModel {
				passthroughBody = s.replaceModelInBody(passthroughBody, mappedModel)
				logger.LegacyPrintf("service.gateway", "Passthrough model mapping: %s -> %s (account: %s)", parsed.Model, mappedModel, account.Name)
				passthroughModel = mappedModel
			}
		}
		result, err = s.forwardAnthropicAPIKeyPassthroughWithInput(ctx, c, account, anthropicPassthroughForwardInput{
			Body:          passthroughBody,
			Parsed:        parsed,
			RequestModel:  passthroughModel,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     startTime,
		})
		return result, true, err
	}
	if account.IsAnthropicOAuthPassthroughEnabled() {
		result, err = s.forwardAnthropicOAuthPassthroughWithInput(ctx, c, account, anthropicPassthroughForwardInput{
			Body:          parsed.Body.Bytes(),
			Parsed:        parsed,
			RequestModel:  parsed.Model,
			OriginalModel: parsed.Model,
			RequestStream: parsed.Stream,
			StartTime:     startTime,
		})
		return result, true, err
	}
	return nil, false, nil
}
