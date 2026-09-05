package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/gin-gonic/gin"
)

func classifyAnthropicResponseInputAsCacheRead(body []byte, usage *ClaudeUsage) ([]byte, error) {
	classified, err := sjson.SetBytes(body, "usage.input_tokens", 0)
	if err != nil {
		return nil, fmt.Errorf("classify forced cache billing input tokens: %w", err)
	}
	classified, err = sjson.SetBytes(classified, "usage.cache_read_input_tokens", usage.CacheReadInputTokens+usage.InputTokens)
	if err != nil {
		return nil, fmt.Errorf("classify forced cache billing cache read tokens: %w", err)
	}
	return classified, nil
}

// tkPrepareAnthropicPassthroughBody applies TokenKey request prep before the
// upstream passthrough hop: context-window alias strip, compatibility
// sanitize, CC prompt-surface normalize, web-search history filter,
// ToolSearch thinking prefilter, sig-preempt, tool-context reject, sticky.
func (s *GatewayService) tkPrepareAnthropicPassthroughBody(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	input *anthropicPassthroughForwardInput,
) error {
	// TK: strip Claude Code context-window aliases on passthrough before
	// billing/quota and upstream forward see the bracketed id.
	if bare, aliased := tkStripContextWindowModelAlias(input.RequestModel); aliased {
		input.Body = s.replaceModelInBody(input.Body, bare)
		input.RequestModel = bare
		input.OriginalModel = bare
		if input.Parsed != nil {
			input.Parsed.Model = bare
		}
		logger.LegacyPrintf("service.gateway",
			"TK context-window alias stripped on APIKey passthrough (claude-code #60913): -> %s account=%d", bare, account.ID)
	}
	// Pre-filter: sanitize invalid UTF-8 / lone surrogate escapes, strip empty
	// text blocks, drop explicit disabled thinking for Fable, and strip fields
	// rejected by newer Anthropic models.
	input.Body = tkApplyAnthropicRequestCompatibilityRules(account, tkStripFableDisabledThinking(StripEmptyTextBlocks(TkSanitizeRequestBody(input.Body, account))))
	if input.Parsed != nil {
		if err := input.Parsed.ReplaceBody(input.Body); err != nil {
			return err
		}
	}
	// TK: CC prompt-surface normalize; passthrough skips the full normalize hook.
	if s != nil && s.settingService != nil && s.settingService.IsAnthropicRequestNormalizeEnabled(ctx) {
		var changes []tkAnthropicNormalizeChange
		input.Body, changes = tkApplyAnthropicCCPromptSurfaceNormalize(ctx, c, account, input.Body)
		if len(changes) > 0 && input.Parsed != nil {
			if err := input.Parsed.ReplaceBody(input.Body); err != nil {
				return err
			}
		}
	}
	// Pre-filter: strip web-search history blocks the upstream cannot accept
	// (emulation-synthesized ones always; genuine ones additionally for
	// passback-required third-party upstreams such as GLM/Kimi/DeepSeek,
	// which reject server_tool_use with 400). input.RequestModel 已是映射后的模型 ID。
	input.Body = FilterWebSearchHistoryBlocks(input.Body, input.RequestModel)
	// TK: ToolSearch + stale signed thinking pre-filter.
	input.Body = TkPrefilterToolSearchHistoricalThinking(input.Body, input.RequestModel)
	if account.Platform == PlatformAnthropic {
		input.Body = s.applySigPreemptIfArmed(ctx, c, account, input.Body, input.RequestModel)
	}
	if account.Platform == PlatformAnthropic {
		if err := s.tkRejectInvalidAnthropicToolContext(ctx, c, account, input.Body, s.tkRequiresClaudeCodeSystemSurface(ctx, c, account), true); err != nil {
			return err
		}
	}
	metadataUserID := ""
	if input.Parsed != nil {
		metadataUserID = input.Parsed.MetadataUserID
	}
	if metadataUserID == "" {
		metadataUserID = gjson.GetBytes(input.Body, "metadata.user_id").String()
	}
	userAgent := ""
	if c != nil && c.Request != nil {
		userAgent = c.GetHeader("User-Agent")
	}
	isClaudeCode := IsClaudeCodeClient(ctx) || isClaudeCodeClient(userAgent, metadataUserID)
	var err error
	input.Body, _, err = applyStickyToAnthropicMessagesBody(ctx, c, s.settingService, account, input.Body, input.RequestModel, isClaudeCode)
	if err != nil {
		return err
	}
	if input.Parsed != nil {
		// 透传分支也会改写实际 wire body，成功 usage hash 依赖这里同步当前 body。
		if err := input.Parsed.ReplaceBody(input.Body); err != nil {
			return err
		}
	}
	return nil
}

// tkMaybeRetryAnthropicPassthrough400 runs the TokenKey 400 rectifier once and
// returns the response to continue with. When retried is true the caller must
// break out of the upstream attempt loop (matching the prior inline control
// flow). On a successful rectifier hop it may replace resp and update
// input.Body; otherwise it restores resp.Body from the buffered 400 body when
// the original body was consumed.
func (s *GatewayService) tkMaybeRetryAnthropicPassthrough400(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	input *anthropicPassthroughForwardInput,
	resp *http.Response,
	upstreamReq *http.Request,
	proxyURL string,
	token string,
	authKind anthropicPassthroughAuthKind,
	retryStart time.Time,
) (out *http.Response, retried bool, err error) {
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		return resp, false, nil
	}
	respBody, readErr := s.readUpstreamErrorBody(resp)
	if readErr != nil {
		return resp, false, nil
	}
	_ = resp.Body.Close()
	retryBody, retryKind, shouldRetry := s.rectifyAnthropicPassthrough400(ctx, account, input.Body, input.RequestModel, respBody)
	if shouldRetry && time.Since(retryStart) < maxRetryElapsed {
		if retryKind == "signature_retry_thinking" {
			s.armSigPreemptOnError(ctx, c, account)
		}
		retryCtx, releaseRetryCtx := detachStreamUpstreamContext(ctx, input.RequestStream)
		retryReq, retryWireBody, buildErr := s.buildAnthropicPassthroughUpstreamRequest(retryCtx, c, account, retryBody, token, authKind)
		releaseRetryCtx()
		if buildErr == nil {
			retryResp, retryErr := s.httpUpstream.DoWithTLS(retryReq, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
			if retryErr == nil {
				appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
					Platform:           account.Platform,
					AccountID:          account.ID,
					AccountName:        account.Name,
					UpstreamStatusCode: resp.StatusCode,
					UpstreamRequestID:  resp.Header.Get("x-request-id"),
					UpstreamURL:        safeUpstreamURL(upstreamReq.URL.String()),
					Passthrough:        true,
					Kind:               retryKind,
					Message:            extractUpstreamErrorMessage(respBody),
				})
				if retryResp.StatusCode < 400 {
					input.Body = retryWireBody
					setOpsUpstreamRequestBody(c, retryWireBody)
					if input.Parsed != nil {
						if err := input.Parsed.ReplaceBody(retryWireBody); err != nil {
							_ = retryResp.Body.Close()
							return nil, true, err
						}
					}
				}
				return retryResp, true, nil
			}
			if retryResp != nil && retryResp.Body != nil {
				_ = retryResp.Body.Close()
			}
			logger.LegacyPrintf("service.gateway", "Anthropic passthrough account %d: 400 rectifier retry failed: %v", account.ID, retryErr)
		} else {
			logger.LegacyPrintf("service.gateway", "Anthropic passthrough account %d: 400 rectifier retry build failed: %v", account.ID, buildErr)
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(respBody))
	return resp, false, nil
}

func (s *GatewayService) rectifyAnthropicPassthrough400(ctx context.Context, account *Account, body []byte, model string, respBody []byte) ([]byte, string, bool) {
	if _, ok := tkRecordAnthropicSamplingParamRuleFrom400(account, model, body, http.StatusBadRequest, respBody); ok {
		if rectified := tkStripDeprecatedSamplingParamsForAccount(account, body); !bytes.Equal(rectified, body) {
			return rectified, "sampling_param_retry", true
		}
	}

	if s.shouldRectifySignatureError(ctx, account, respBody, model) {
		return FilterThinkingBlocksForRetry(body, model), "signature_retry_thinking", true
	}

	errMsg := extractUpstreamErrorMessage(respBody)
	if isThinkingTypeAdaptiveRequiredError(errMsg) {
		tkRecordAnthropicThinkingRuleFrom400(account, model, body, http.StatusBadRequest, respBody)
		if rectified, ok := RectifyThinkingTypeAdaptive(body); ok {
			return rectified, "thinking_adaptive_retry", true
		}
	}
	if isThinkingBudgetConstraintError(errMsg) {
		if s.settingService != nil && !s.settingService.IsBudgetRectifierEnabled(ctx) {
			return body, "", false
		}
		if rectified, ok := RectifyThinkingBudget(body, model); ok {
			return rectified, "budget_constraint_retry", true
		}
	}
	return body, "", false
}

// tkRecordAnthropicPassthroughStreamTerminalError records ops evidence for an
// already-forwarded SSE error frame without appending a second client error.
func (s *GatewayService) tkRecordAnthropicPassthroughStreamTerminalError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	err error,
) {
	var sseErr *sseStreamErrorEventError
	if errors.As(err, &sseErr) {
		// The error frame has already been forwarded as the terminal client
		// outcome. Reuse the canonical recorder for ops evidence, but keep
		// the non-failover error shape so the handler does not append a
		// second generic SSE error after semantic output was committed.
		_ = s.sseStreamErrorFailover(c, account, resp, sseErr, http.StatusBadGateway)
	}
}
