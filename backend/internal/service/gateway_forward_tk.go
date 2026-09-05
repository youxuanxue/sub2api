package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
)

// tkPrepareAnthropicForwardIngress applies TokenKey early Forward prep:
// context-window alias strip and canonical-OAuth ingress UA / Opus remap.
// getBody/replaceBody must share the caller's body view (same as Forward).
func (s *GatewayService) tkPrepareAnthropicForwardIngress(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	reqModel string,
	originalModel string,
	getBody func() []byte,
	replaceBody func([]byte) error,
) (newReqModel, newOriginalModel string, groupAdmitsNonCC bool, err error) {
	newReqModel, newOriginalModel = reqModel, originalModel

	// TK: strip the Claude Code 1M-context model alias suffix before model
	// mapping / scheduling / pricing. See gateway_anthropic_context_window_alias_tk.go.
	if account.Platform == PlatformAnthropic {
		if bare, aliased := tkStripContextWindowModelAlias(reqModel); aliased {
			if err := replaceBody(s.replaceModelInBody(getBody(), bare)); err != nil {
				return newReqModel, newOriginalModel, false, err
			}
			logger.LegacyPrintf("service.gateway",
				"TK context-window alias stripped before forward (prevents Anthropic 404 + silent 200K fallback, claude-code #60913): %s -> %s account=%d",
				reqModel, bare, account.ID)
			newReqModel, parsed.Model, newOriginalModel = bare, bare, bare
			reqModel = bare
		}
	}

	// TK canonical-OAuth ingress gates. cc_only=false groups admit non-CC
	// traffic and complete the disguise on egress via haiku mimicry below.
	groupAdmitsNonCC = s.tkGroupAdmitsNonCC(ctx, parsed)
	if c != nil && c.Request != nil && s.isCanonicalAnthropicOAuth(account) {
		if s.settingService.IsAnthropicCanonicalIngressStrictEnabled(ctx) {
			if err := checkCanonicalIngressUAStrict(c.Request.Header); err != nil {
				return newReqModel, newOriginalModel, groupAdmitsNonCC, err
			}
		} else if !groupAdmitsNonCC {
			if err := checkCanonicalIngressUA(c.Request.Header); err != nil {
				return newReqModel, newOriginalModel, groupAdmitsNonCC, err
			}
		}
		if newModel, remapped := remapDeprecatedOpusOnCanonical(reqModel); remapped {
			if err := replaceBody(s.replaceModelInBody(getBody(), newModel)); err != nil {
				return newReqModel, newOriginalModel, groupAdmitsNonCC, err
			}
			logger.LegacyPrintf("service.gateway",
				"Canonical OAuth model remap: %s -> %s (account: %s)",
				reqModel, newModel, account.Name)
			newReqModel, parsed.Model = newModel, newModel
		}
	}
	return newReqModel, newOriginalModel, groupAdmitsNonCC, nil
}

// tkSanitizeAnthropicForwardBody applies the TokenKey sanitize wrap around the
// upstream StripEmptyTextBlocks pre-filter (UTF-8 / fable / sampling compat).
func (s *GatewayService) tkSanitizeAnthropicForwardBody(
	account *Account,
	getBody func() []byte,
	replaceBody func([]byte) error,
) error {
	return replaceBody(tkApplyAnthropicRequestCompatibilityRules(account, tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(getBody(), account)))))
}

// tkApplyAnthropicForwardPreSendGuards applies signature preempt, tool-context
// reject, and sticky injection after ToolSearch + upstream thinking filters.
// Sig-preempt updates a local body view without replaceBody until sticky,
// matching the prior inline order.
func (s *GatewayService) tkApplyAnthropicForwardPreSendGuards(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	reqModel string,
	isClaudeCode bool,
	getBody func() []byte,
	replaceBody func([]byte) error,
) error {
	if account.Platform != PlatformAnthropic {
		return nil
	}
	body := s.applySigPreemptIfArmed(ctx, c, account, getBody(), reqModel)
	if err := s.tkRejectInvalidAnthropicToolContext(ctx, c, account, body, s.tkRequiresClaudeCodeSystemSurface(ctx, c, account), false); err != nil {
		return err
	}
	stickyBody, _, err := applyStickyToAnthropicMessagesBody(ctx, c, s.settingService, account, body, reqModel, isClaudeCode)
	if err != nil {
		return err
	}
	return replaceBody(stickyBody)
}

// tkHandleKiroForwardUpstreamError records rate-limit side effects for a Kiro
// UpstreamFailoverError after kiroGateway.Forward returns.
func (s *GatewayService) tkHandleKiroForwardUpstreamError(ctx context.Context, account *Account, err error) {
	if err == nil || s.rateLimitService == nil {
		return
	}
	var failoverErr *UpstreamFailoverError
	if errors.As(err, &failoverErr) {
		s.rateLimitService.HandleUpstreamError(ctx, account, failoverErr.StatusCode, failoverErr.ResponseHeaders, failoverErr.ResponseBody)
	}
}

// tkApplyAnthropicForwardPostMappingGates runs deprecated-model, priced-serving,
// and model-not-found short-circuits after channel mapping.
func (s *GatewayService) tkApplyAnthropicForwardPostMappingGates(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	originalModel string,
	mappedModel string,
) error {
	if account.Platform == PlatformAnthropic {
		if replacement, deprecated := tkIsDeprecatedAnthropicModel(mappedModel); deprecated {
			TkWriteAnthropicDeprecatedModelError(c, mappedModel, replacement)
			return fmt.Errorf("anthropic model %q is retired (suggest %q)", mappedModel, replacement)
		}
	}
	if !s.tkPricedServingGate(ctx, c, tkGateWireAnthropic, account.Platform, originalModel, originalModel) {
		return fmt.Errorf("priced serving gate: model %q not priced for platform %q", originalModel, account.Platform)
	}
	if handled, ncErr := s.tkModelNotFoundShortCircuit(c, account, mappedModel); handled {
		return ncErr
	}
	return nil
}
