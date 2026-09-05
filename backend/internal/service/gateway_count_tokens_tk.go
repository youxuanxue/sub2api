package service

import (
	"context"
	"log/slog"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// tkPrepareCountTokensAnthropicBody applies TokenKey-only count_tokens ingress
// transforms that run BEFORE shouldEstimateCountTokensLocally:
// context-window alias strip, Anthropic request compatibility, native body
// normalize, unsupported-field strip.
//
// The canonical-OAuth UA gate MUST remain in ForwardCountTokens AFTER the
// estimate short-circuit — estimate platforms never hit that gate.
// replaceBody mutates the parsed request buffer; getBody re-reads it after
// each successful rewrite.
func (s *GatewayService) tkPrepareCountTokensAnthropicBody(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	reqModel string,
	replaceBody func(next []byte) error,
	getBody func() []byte,
) (nextBody []byte, nextModel string, err error) {
	nextBody, nextModel = body, reqModel

	// Strip Claude Code context-window aliases before count_tokens reaches
	// upstream or per-account breaker accounting.
	if account != nil && account.Platform == PlatformAnthropic {
		if bare, aliased := tkStripContextWindowModelAlias(nextModel); aliased {
			if err := replaceBody(s.replaceModelInBody(nextBody, bare)); err != nil {
				return nextBody, nextModel, err
			}
			nextBody = getBody()
			nextModel = bare
			logger.LegacyPrintf("service.gateway",
				"TK context-window alias stripped on count_tokens (claude-code #60913): -> %s account=%d", bare, account.ID)
		}
	}

	// Pre-filter: sanitize invalid UTF-8 / lone surrogate escapes, strip empty
	// text blocks, drop explicit disabled thinking for Fable, and strip fields
	// rejected by newer Anthropic models.
	if err := replaceBody(tkApplyAnthropicRequestCompatibilityRules(account, tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(nextBody, account))))); err != nil {
		return nextBody, nextModel, err
	}
	nextBody = getBody()

	// TK: normalize Anthropic native request body for count_tokens path.
	if account != nil && account.Platform == PlatformAnthropic {
		nextBody = s.tkNormalizeAnthropicRequestBody(ctx, c, nextBody, account)
	}

	if next, stripped := StripCountTokensUnsupportedFields(nextBody); len(stripped) > 0 {
		nextBody = next
		slog.Info(
			"count_tokens.stripped_unsupported_fields",
			"account_id", account.ID,
			"account_name", account.Name,
			"fields", stripped,
		)
	}

	return nextBody, nextModel, nil
}
