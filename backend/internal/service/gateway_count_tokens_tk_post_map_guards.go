package service

import (
	"context"
	"fmt"

	"github.com/gin-gonic/gin"
)

// tkApplyCountTokensPostMapGuards runs TokenKey post-model-mapping guards on
// count_tokens: deprecated Anthropic model reject, signature preempt, and
// invalid tool-context reject. Must stay AFTER model mapping and AFTER the
// estimate-local / canonical-UA gate short-circuits in ForwardCountTokens.
func (s *GatewayService) tkApplyCountTokensPostMapGuards(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	reqModel string,
) ([]byte, error) {
	if account.Platform == PlatformAnthropic && reqModel != "" {
		if replacement, deprecated := tkIsDeprecatedAnthropicModel(reqModel); deprecated {
			TkWriteAnthropicDeprecatedModelError(c, reqModel, replacement)
			return body, fmt.Errorf("anthropic model %q is retired (suggest %q)", reqModel, replacement)
		}
	}
	if account.Platform == PlatformAnthropic {
		body = s.applySigPreemptIfArmed(ctx, c, account, body, reqModel)
	}
	if account.Platform == PlatformAnthropic {
		if err := s.tkRejectInvalidAnthropicToolContext(ctx, c, account, body, s.tkRequiresClaudeCodeSystemSurface(ctx, c, account), false); err != nil {
			return body, err
		}
	}
	return body, nil
}
