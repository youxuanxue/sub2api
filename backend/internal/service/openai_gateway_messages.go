package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// ForwardAsAnthropic accepts an Anthropic Messages request body, converts it
// to OpenAI Responses API format, forwards to the OpenAI upstream, and converts
// the response back to Anthropic Messages format. This enables Claude Code
// clients to access OpenAI models through the standard /v1/messages endpoint.
func (s *OpenAIGatewayService) ForwardAsAnthropic(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	promptCacheKey string,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	// 1. Parse Anthropic request
	var anthropicReq apicompat.AnthropicRequest
	if err := json.Unmarshal(body, &anthropicReq); err != nil {
		return nil, fmt.Errorf("parse anthropic request: %w", err)
	}
	anthropicDigestReq := cloneAnthropicRequestForDigest(&anthropicReq)
	originalModel := anthropicReq.Model
	applyOpenAICompatModelNormalization(&anthropicReq)
	normalizedModel := anthropicReq.Model
	clientStream := anthropicReq.Stream // client's original stream preference

	// 2. Model mapping
	billingModel := resolveOpenAIForwardModel(account, normalizedModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	promptCacheKey = strings.TrimSpace(promptCacheKey)
	apiKeyID := getAPIKeyIDFromContext(c)
	anthropicDigestChain := ""
	anthropicMatchedDigestChain := ""
	compatPromptCacheInjected := false
	if promptCacheKey == "" && shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
		promptCacheKey = promptCacheKeyFromAnthropicMetadataSession(&anthropicReq)
		if promptCacheKey == "" {
			promptCacheKey = deriveAnthropicCacheControlPromptCacheKey(&anthropicReq)
		}
		if promptCacheKey == "" {
			anthropicDigestChain = buildOpenAICompatAnthropicDigestChain(anthropicDigestReq)
			if reusedKey, matchedChain := s.findOpenAICompatAnthropicDigestPromptCacheKey(account, apiKeyID, anthropicDigestChain); reusedKey != "" {
				promptCacheKey = reusedKey
				anthropicMatchedDigestChain = matchedChain
			} else {
				promptCacheKey = promptCacheKeyFromAnthropicDigest(anthropicDigestChain)
			}
		}
		compatPromptCacheInjected = promptCacheKey != ""
	}
	compatReplayTrimmed := false
	compatReplayTrimmedByPolicy := false
	compatReplayGuardEnabled := shouldAutoInjectPromptCacheKeyForCompat(upstreamModel)
	compatCompactionPolicy := resolveOpenAICompatMessagesCompactionPolicy(account, apiKeyGroup(getAPIKeyFromContext(c)))
	compatContinuationEnabled := openAICompatContinuationEnabled(account, upstreamModel)
	previousResponseID := ""
	if compatContinuationEnabled {
		previousResponseID = s.getOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey)
	}
	compatContinuationDisabled := compatContinuationEnabled &&
		s.isOpenAICompatSessionContinuationDisabled(ctx, c, account, promptCacheKey)
	compatTurnState := ""
	if compatReplayGuardEnabled && shouldEvaluateOpenAICompatMessagesCompactionForAccount(account, previousResponseID, compatContinuationDisabled) {
		if shouldApplyOpenAICompatMessagesCompaction(compatCompactionPolicy, &anthropicReq) {
			compatReplayTrimmed = applyOpenAICompatMessagesCompaction(account, &anthropicReq)
			compatReplayTrimmedByPolicy = compatReplayTrimmed
		}
	}

	// 3. Convert Anthropic → Responses after compatibility-only replay guard.
	responsesReq, err := apicompat.AnthropicToResponses(&anthropicReq)
	if err != nil {
		return nil, fmt.Errorf("convert anthropic to responses: %w", err)
	}

	// Upstream always uses streaming (upstream may not support sync mode).
	// The client's original preference determines the response format.
	responsesReq.Stream = true
	isStream := true

	// 3b. Handle BetaFastMode → service_tier: "priority"
	if containsBetaToken(c.GetHeader("anthropic-beta"), claude.BetaFastMode) {
		responsesReq.ServiceTier = "priority"
	}

	responsesReq.Model = upstreamModel
	if previousResponseID != "" {
		responsesReq.PreviousResponseID = previousResponseID
		// Only trim for APIKey accounts: OpenAI Platform keeps full history in
		// session state so only the new turn is needed. For OAuth (ChatGPT Codex),
		// trimming strips the role=system item and breaks system-prompt delivery
		// to the transform — keep full replay (same rationale as line 111 above).
		if openAICompatShouldTrimForContinuation(account) {
			trimAnthropicCompatResponsesInputToLatestTurn(responsesReq)
		}
	}
	if compatReplayGuardEnabled && !account.IsOpenAIOAuth() {
		appendOpenAICompatClaudeCodeTodoGuard(responsesReq)
	}

	logFields := []zap.Field{
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("normalized_model", normalizedModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", isStream),
	}
	if compatPromptCacheInjected {
		logFields = append(logFields,
			zap.Bool("compat_prompt_cache_key_injected", true),
			zap.String("compat_prompt_cache_key_sha256", hashSensitiveValueForLog(promptCacheKey)),
		)
	}
	if compatReplayTrimmed {
		logFields = append(logFields,
			zap.Bool("compat_full_replay_trimmed", true),
			zap.Int("compat_messages_after_trim", len(anthropicReq.Messages)),
		)
	}
	if compatReplayTrimmedByPolicy {
		logFields = append(logFields,
			zap.Bool("compat_messages_compaction_applied", true),
			zap.Int("compat_messages_compaction_input_tokens_threshold", compatCompactionPolicy.inputTokenLimit),
		)
	}
	if previousResponseID != "" {
		logFields = append(logFields,
			zap.Bool("compat_previous_response_id_attached", true),
			zap.String("compat_previous_response_id", truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen)),
		)
	}
	if compatTurnState != "" {
		logFields = append(logFields, zap.Bool("compat_turn_state_attached", true))
	}
	logger.L().Debug("openai messages: model mapping applied", logFields...)

	compactCandidate := openAICompatMessagesCompactCandidate(&anthropicReq)

	// 4. Marshal Responses request body, then apply OAuth codex transform
	responsesBody, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}

	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok {
		var reqBody map[string]any
		if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
			return nil, fmt.Errorf("unmarshal for codex transform: %w", err)
		}
		codexResult := applyCodexOAuthTransformWithOptions(reqBody, codexOAuthTransformOptions{
			SkipDefaultInstructions: true,
			PreserveToolCallIDs:     true,
		})
		forcedTemplateText := ""
		if s.cfg != nil {
			forcedTemplateText = s.cfg.Gateway.ForcedCodexInstructionsTemplate
		}
		templateUpstreamModel := upstreamModel
		if codexResult.NormalizedModel != "" {
			templateUpstreamModel = codexResult.NormalizedModel
		}
		existingInstructions, _ := reqBody["instructions"].(string)
		if strings.TrimSpace(existingInstructions) == "" {
			existingInstructions = extractPromptLikeInstructionsFromInput(reqBody)
		}
		if _, err := applyForcedCodexInstructionsTemplate(reqBody, forcedTemplateText, forcedCodexInstructionsTemplateData{
			ExistingInstructions: strings.TrimSpace(existingInstructions),
			OriginalModel:        originalModel,
			NormalizedModel:      normalizedModel,
			BillingModel:         billingModel,
			UpstreamModel:        templateUpstreamModel,
		}); err != nil {
			return nil, err
		}
		ensureCodexOAuthInstructionsField(reqBody)
		if shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
			appendOpenAICompatClaudeCodeTodoGuardToRequestBody(reqBody)
		}
		if codexResult.NormalizedModel != "" {
			upstreamModel = codexResult.NormalizedModel
		}
		if codexResult.PromptCacheKey != "" {
			promptCacheKey = codexResult.PromptCacheKey
		}
		delete(reqBody, "prompt_cache_key")
		if shouldAutoInjectPromptCacheKeyForCompat(upstreamModel) {
			compatTurnState = s.getOpenAICompatSessionTurnState(ctx, c, account, promptCacheKey)
		}
		// OAuth codex transform forces stream=true upstream, so always use
		// the streaming response handler regardless of what the client asked.
		isStream = true
		responsesBody, err = json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("remarshal after codex transform: %w", err)
		}
	}

	// For API key accounts (including OpenAI-compatible upstream gateways),
	// ensure promptCacheKey is also propagated via the request body so that
	// upstreams using the Responses API can derive a stable session identifier
	// from prompt_cache_key. This makes our Anthropic /v1/messages compatibility
	// path behave more like a native Responses client.
	if account.Type == AccountTypeAPIKey {
		if trimmedKey := strings.TrimSpace(promptCacheKey); trimmedKey != "" {
			var reqBody map[string]any
			if err := json.Unmarshal(responsesBody, &reqBody); err != nil {
				return nil, fmt.Errorf("unmarshal for prompt cache key injection: %w", err)
			}
			if existing, ok := reqBody["prompt_cache_key"].(string); !ok || strings.TrimSpace(existing) == "" {
				reqBody["prompt_cache_key"] = trimmedKey
				updated, err := json.Marshal(reqBody)
				if err != nil {
					return nil, fmt.Errorf("remarshal after prompt cache key injection: %w", err)
				}
				responsesBody = updated
			}
		}
	}

	// 4c. Apply OpenAI fast policy (may filter service_tier or block the request).
	// Mirrors the Claude anthropic-beta "fast-mode-2026-02-01" filter, but keyed
	// on the body-level service_tier field (priority/flex).
	updatedBody, policyErr := s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, responsesBody)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			MarkOpsClientPolicyDenied(c, OpsClientPolicyDeniedReasonLocalPolicyDenied)
			writeAnthropicError(c, http.StatusForbidden, "forbidden_error", blocked.Message)
		}
		return nil, policyErr
	}
	responsesBody = updatedBody
	if account.Platform == PlatformGrok {
		patchedBody, patchErr := patchGrokResponsesBody(responsesBody, upstreamModel)
		if patchErr != nil {
			return nil, patchErr
		}
		responsesBody = patchedBody
	}

	// 5. Get access token
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// Native-catalog Grok SKUs are provisioned on /v1/chat/completions; /v1/responses
	// often returns opaque 400s. Skip the responses hop for SSOT / universal parity.
	if account.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(upstreamModel) {
		fallbackResult, fallbackErr := s.fallbackAnthropicToGrokChatCompletions(
			ctx,
			c,
			account,
			&anthropicReq,
			clientStream,
			originalModel,
			billingModel,
			upstreamModel,
			token,
			startTime,
		)
		if fallbackResult != nil {
			fallbackResult.CompactCandidate = compactCandidate
		}
		return fallbackResult, fallbackErr
	}

	// 6. Build upstream request
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	var upstreamReq *http.Request
	if account.Platform == PlatformGrok {
		targetURL, targetErr := s.resolveGrokResponsesUpstream(account)
		if targetErr != nil {
			releaseUpstreamCtx()
			return nil, targetErr
		}
		upstreamReq, err = buildGrokResponsesRequest(upstreamCtx, c, targetURL, responsesBody, token)
	} else {
		upstreamReq, err = s.buildUpstreamRequest(upstreamCtx, c, account, responsesBody, token, isStream, promptCacheKey, false)
	}
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}

	// Override session_id with a deterministic UUID derived from the isolated
	// session key, ensuring different API keys produce different upstream sessions.
	if promptCacheKey != "" {
		isolatedSessionID := generateSessionUUID(isolateOpenAISessionID(apiKeyID, promptCacheKey))
		upstreamReq.Header.Set("session_id", isolatedSessionID)
		if upstreamReq.Header.Get("conversation_id") != "" {
			upstreamReq.Header.Set("conversation_id", isolatedSessionID)
		}
	}
	if account.Type == AccountTypeOAuth && account.Platform != PlatformGrok {
		// Anthropic Messages compatibility uses the ChatGPT Codex SSE endpoint.
		// Match airgate-openai's request shape: the SSE endpoint does not need
		// the Responses experimental beta header. Keep originator so ChatGPT
		// classifies the request under the official Codex surface instead of
		// Uncategorized.
		upstreamReq.Header.Del("OpenAI-Beta")
	}
	if account.IsOpenAIOAuth() && promptCacheKey != "" && strings.TrimSpace(c.GetHeader("conversation_id")) == "" {
		upstreamReq.Header.Del("conversation_id")
	}
	// Note: OAuth accounts with continuation enabled may send BOTH
	// previous_response_id (body) and x-codex-turn-state (header) simultaneously.
	// If the upstream rejects the combination, isOpenAICompatPreviousResponseUnsupported
	// (line ~308) catches the 400 and disableOpenAICompatSessionContinuation reverts
	// the session to turn-state-only — one failed turn, then automatic recovery.
	if compatTurnState != "" && upstreamReq.Header.Get("x-codex-turn-state") == "" {
		upstreamReq.Header.Set("x-codex-turn-state", compatTurnState)
	}

	// 7. Send request
	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		safeErr := sanitizeUpstreamErrorMessage(err.Error())
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: 0,
			Kind:               "request_error",
			Message:            safeErr,
		})
		writeAnthropicError(c, http.StatusBadGateway, "api_error", "Upstream request failed")
		return nil, fmt.Errorf("upstream request failed: %s", safeErr)
	}
	defer func() { _ = resp.Body.Close() }()

	// 8. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		if account.Platform == PlatformGrok {
			s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
			s.handleGrokAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody)
		}

		upstreamMsg := strings.TrimSpace(extractUpstreamErrorMessage(respBody))
		upstreamMsg = sanitizeUpstreamErrorMessage(upstreamMsg)
		previousResponseNotFound := isOpenAICompatPreviousResponseNotFound(resp.StatusCode, upstreamMsg, respBody)
		previousResponseUnsupported := isOpenAICompatPreviousResponseUnsupported(resp.StatusCode, upstreamMsg, respBody)
		if previousResponseID != "" && (previousResponseNotFound || previousResponseUnsupported) {
			fallbackReason := "not_found"
			continuationDisabledAfterFallback := false
			continuationDisableReason := ""
			if previousResponseUnsupported {
				fallbackReason = "unsupported"
				continuationDisableReason = "unsupported"
				s.disableOpenAICompatSessionContinuation(ctx, c, account, promptCacheKey)
				continuationDisabledAfterFallback = true
			} else if openAICompatShouldDisableContinuationOnPreviousResponseNotFound(account) {
				continuationDisableReason = "oauth_not_found_persistent"
				s.disableOpenAICompatSessionContinuation(ctx, c, account, promptCacheKey)
				continuationDisabledAfterFallback = true
			} else {
				s.deleteOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey)
			}
			logFields := []zap.Field{
				zap.Int64("account_id", account.ID),
				zap.String("account_type", account.Type),
				zap.String("previous_response_id", truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen)),
				zap.String("upstream_model", upstreamModel),
				zap.String("compat_previous_response_fallback_reason", fallbackReason),
				zap.Bool("compat_turn_state_present", strings.TrimSpace(compatTurnState) != ""),
				zap.Bool("compat_continuation_disabled_after_fallback", continuationDisabledAfterFallback),
				zap.Bool("compat_previous_response_retry_without_continuation", true),
				zap.Int("compat_retry_attempt", 1),
			}
			if continuationDisableReason != "" {
				logFields = append(logFields, zap.String("compat_continuation_disable_reason", continuationDisableReason))
			}
			if promptCacheKey != "" {
				logFields = append(logFields, zap.String("compat_prompt_cache_key_sha256", hashSensitiveValueForLog(promptCacheKey)))
			}
			logger.L().Info("openai messages: previous_response_id unavailable, retrying without continuation", logFields...)
			return s.ForwardAsAnthropic(ctx, c, account, body, promptCacheKey, defaultMappedModel)
		}
		if s.shouldFailoverOpenAIUpstreamResponse(resp.StatusCode, upstreamMsg, respBody) {
			upstreamDetail := ""
			if s.cfg != nil && s.cfg.Gateway.LogUpstreamErrorBody {
				maxBytes := s.cfg.Gateway.LogUpstreamErrorBodyMaxBytes
				if maxBytes <= 0 {
					maxBytes = 2048
				}
				upstreamDetail = truncateString(string(respBody), maxBytes)
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  resp.Header.Get("x-request-id"),
				Kind:               "failover",
				Message:            upstreamMsg,
				Detail:             upstreamDetail,
			})
			s.handleOpenAIAccountUpstreamError(ctx, account, resp.StatusCode, resp.Header, respBody, upstreamModel)
			return nil, &UpstreamFailoverError{
				StatusCode:             resp.StatusCode,
				ResponseBody:           respBody,
				RetryableOnSameAccount: account.IsPoolMode() && (account.IsPoolModeRetryableStatus(resp.StatusCode) || isOpenAITransientProcessingError(resp.StatusCode, upstreamMsg, respBody)),
			}
		}
		if account.Platform == PlatformGrok && isGrokResponsesModelNotSupportedRetryable(resp.StatusCode, upstreamMsg, respBody) {
			logger.L().Info("openai messages: grok responses model not supported, fallback to chat completions",
				zap.Int64("account_id", account.ID),
				zap.Int("upstream_status", resp.StatusCode),
				zap.String("upstream_model", upstreamModel),
			)
			fallbackResult, fallbackErr := s.fallbackAnthropicToGrokChatCompletions(
				ctx,
				c,
				account,
				&anthropicReq,
				clientStream,
				originalModel,
				billingModel,
				upstreamModel,
				token,
				startTime,
			)
			if fallbackResult != nil {
				fallbackResult.CompactCandidate = compactCandidate
			}
			return fallbackResult, fallbackErr
		}
		// Non-failover error: return Anthropic-formatted error to client
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if account.IsOpenAIOAuth() && promptCacheKey != "" {
		if turnState := strings.TrimSpace(resp.Header.Get("x-codex-turn-state")); turnState != "" {
			s.bindOpenAICompatSessionTurnState(ctx, c, account, promptCacheKey, turnState)
		}
	}

	// 9. Handle normal response
	// Upstream is always streaming; choose response format based on client preference.
	var result *OpenAIForwardResult
	var handleErr error
	if clientStream {
		result, handleErr = s.handleAnthropicStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	} else {
		// Client wants JSON: buffer the streaming response and assemble a JSON reply.
		result, handleErr = s.handleAnthropicBufferedStreamingResponse(resp, c, account, originalModel, billingModel, upstreamModel, startTime)
	}
	if result != nil {
		result.CompactCandidate = compactCandidate
	}

	// cyber_policy：标记已设、error 已按 Anthropic 格式发给客户端。丢弃 result、返回哨兵，
	// 使 handler 落入 tokens=0 免费用量行（对齐 /v1/responses），不计费、不 failover。
	if GetOpsCyberPolicy(c) != nil {
		if handleErr == nil {
			handleErr = errOpenAICyberPolicyForwarded
		}
		return nil, handleErr
	}

	// Propagate ServiceTier and ReasoningEffort to result for billing
	if handleErr == nil && result != nil {
		if compatContinuationEnabled && promptCacheKey != "" && result.ResponseID != "" {
			s.bindOpenAICompatSessionResponseID(ctx, c, account, promptCacheKey, result.ResponseID)
		}
		if promptCacheKey != "" && anthropicDigestChain != "" {
			s.bindOpenAICompatAnthropicDigestPromptCacheKey(account, apiKeyID, anthropicDigestChain, promptCacheKey, anthropicMatchedDigestChain)
		}
		if responsesReq.ServiceTier != "" {
			st := responsesReq.ServiceTier
			result.ServiceTier = &st
		}
		if responsesReq.Reasoning != nil && responsesReq.Reasoning.Effort != "" {
			re := responsesReq.Reasoning.Effort
			result.ReasoningEffort = &re
		}
	}

	// Extract and save Codex usage snapshot from response headers (for OAuth accounts).
	// 排除 spark 影子:其 codex_* 仅由 QueryUsage(/wham/usage bengalfox)更新(外审第7轮 P1)。
	if handleErr == nil && account.Type == AccountTypeOAuth && !account.IsShadow() {
		if account.Platform == PlatformGrok {
			s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
		} else if snapshot := ParseCodexRateLimitHeaders(resp.Header); snapshot != nil {
			s.updateCodexUsageSnapshot(ctx, account.ID, snapshot)
		}
	}

	// Access log for the OpenAI-compat → Anthropic Messages bridge. Surfaces
	// the fields that are easy to read on prod (`docker logs tokenkey | grep
	// openai_messages.completed`) but expensive to derive after the fact:
	// stop_reason, incomplete_reason, reasoning_effort, output_tokens. Used to
	// verify the stop_reason mapping and reasoning effort fixes.
	if result != nil {
		fields := []zap.Field{
			zap.String("request_id", result.RequestID),
			zap.Int64("account_id", account.ID),
			zap.String("account_name", account.Name),
			zap.String("platform", account.Platform),
			zap.String("original_model", originalModel),
			zap.String("billing_model", result.BillingModel),
			zap.String("upstream_model", result.UpstreamModel),
			zap.Bool("stream", result.Stream),
			zap.Int("input_tokens", result.Usage.InputTokens),
			zap.Int("output_tokens", result.Usage.OutputTokens),
			zap.Int("cache_read_input_tokens", result.Usage.CacheReadInputTokens),
			zap.String("stop_reason", result.StopReason),
			zap.Int("content_text_len", result.ContentTextLen),
			zap.Bool("compact_candidate", result.CompactCandidate),
			zap.Int64("duration_ms", result.Duration.Milliseconds()),
		}
		// Emit optional fields only when non-empty so happy-path log lines stay compact.
		if effort := derefOpenAIForwardString(result.ReasoningEffort); effort != "" {
			fields = append(fields, zap.String("reasoning_effort", effort))
		}
		if result.IncompleteReason != "" {
			fields = append(fields, zap.String("incomplete_reason", result.IncompleteReason))
		}
		if result.FirstTokenMs != nil {
			fields = append(fields, zap.Int("first_token_ms", *result.FirstTokenMs))
		}
		if handleErr != nil {
			fields = append(fields, zap.NamedError("handle_error", handleErr))
		}
		logger.L().Info("openai_messages.completed", fields...)
	}

	return result, handleErr
}

func ensureCodexOAuthInstructionsField(reqBody map[string]any) {
	if reqBody == nil {
		return
	}
	if value, ok := reqBody["instructions"]; !ok || value == nil {
		reqBody["instructions"] = ""
		return
	}
	if _, ok := reqBody["instructions"].(string); !ok {
		reqBody["instructions"] = ""
	}
}

// derefOpenAIForwardString returns the string pointed to by p, or "" if p is
// nil. Used by the openai_messages.completed access log to render optional
// pointer fields (ReasoningEffort, ServiceTier) without crashing on nil.
func derefOpenAIForwardString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// handleAnthropicErrorResponse reads an upstream error and returns it in
// Anthropic error format.
func (s *OpenAIGatewayService) handleAnthropicErrorResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	requestedModel ...string,
) (*OpenAIForwardResult, error) {
	return s.handleCompatErrorResponse(resp, c, account, writeAnthropicError, requestedModel...)
}

// handleAnthropicBufferedStreamingResponse reads all Responses SSE events from
// the upstream streaming response, finds the terminal event (the six-event
// set in isOpenAICompatResponsesTerminalEvent), converts the complete response
// to Anthropic Messages JSON format, and writes it to the client.
// This is used when the client requested stream=false but the upstream is always
// streaming.
func (s *OpenAIGatewayService) handleAnthropicBufferedStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	finalResponse, usage, acc, err := s.readOpenAICompatBufferedTerminal(resp, "openai messages buffered", requestID)
	if err != nil {
		return nil, err
	}

	if finalResponse == nil {
		return s.openAICompatBufferedMissingTerminalResult(c, account, requestID, acc, openAICompatBufferedRouteMessages)
	}

	if strings.TrimSpace(finalResponse.Status) == "failed" {
		payload, _ := json.Marshal(gin.H{"type": "response.failed", "response": finalResponse})
		if hit, code, msg := detectOpenAICyberPolicy(payload); hit {
			MarkOpsCyberPolicy(c, CyberPolicyMark{
				Code:           code,
				Message:        msg,
				Body:           truncateString(string(payload), 4096),
				UpstreamStatus: http.StatusOK,
				UpstreamInTok:  usage.InputTokens,
				UpstreamOutTok: usage.OutputTokens,
			})
			clientMsg := msg
			if clientMsg == "" {
				clientMsg = "Request blocked by upstream cyber-security policy"
			}
			writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", clientMsg)
			return nil, fmt.Errorf("openai cyber_policy: %s", msg)
		}
		return s.openAICompatBufferedFailedResponseResult(c, account, requestID, finalResponse, openAICompatBufferedRouteMessages)
	}

	// When the terminal event has an empty output array, reconstruct from
	// accumulated delta events so the client receives the full content.
	acc.SupplementResponseOutput(finalResponse)

	anthropicResp := apicompat.ResponsesToAnthropic(finalResponse, originalModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	// Same fix as the chat.completions buffered path: upstream is Responses SSE
	// and we buffered into JSON; without this, the client would see a JSON body
	// with `Content-Type: text/event-stream` because gin's c.JSON does not
	// overwrite an already-set Content-Type. See upstream Wei-Shaw/sub2api#1311
	// (reported for /v1/chat/completions but the same root cause applies to
	// /v1/messages non-stream).
	c.Writer.Header().Set("Content-Type", "application/json")
	c.JSON(http.StatusOK, anthropicResp)

	incompleteReason := ""
	if finalResponse.IncompleteDetails != nil {
		incompleteReason = finalResponse.IncompleteDetails.Reason
	}

	return &OpenAIForwardResult{
		RequestID:        requestID,
		ResponseID:       finalResponse.ID,
		Usage:            usage,
		Model:            originalModel,
		BillingModel:     billingModel,
		UpstreamModel:    upstreamModel,
		Stream:           false,
		Duration:         time.Since(startTime),
		StopReason:       anthropicResp.StopReason,
		IncompleteReason: incompleteReason,
		ContentTextLen:   anthropicResponseContentTextLen(anthropicResp),
	}, nil
}

func isOpenAICompatResponsesTerminalEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	// TK: align with openAIStreamEventIsTerminal (gateway_service.go),
	// openai_ws_forwarder.go, and openai_ws_v2/passthrough_relay.go so that
	// upstream-emitted response.cancelled / response.canceled close the
	// streaming loop cleanly instead of falling through to "stream usage
	// incomplete: missing terminal event". See upstream Wei-Shaw/sub2api#1322.
	case "response.completed", "response.done", "response.incomplete", "response.failed",
		"response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func (s *OpenAIGatewayService) recordOpenAIMessagesStreamUpstreamError(c *gin.Context, account *Account, upstreamRequestID, kind, message string) {
	if c == nil {
		return
	}
	message = sanitizeUpstreamErrorMessage(message)
	setOpsUpstreamError(c, http.StatusBadGateway, message, "")
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformOpenAI,
		UpstreamStatusCode: http.StatusBadGateway,
		UpstreamRequestID:  strings.TrimSpace(upstreamRequestID),
		Kind:               kind,
		Message:            message,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func isOpenAICompatDoneSentinelLine(line string) bool {
	payload, ok := extractOpenAISSEDataLine(line)
	return ok && strings.TrimSpace(payload) == "[DONE]"
}

func (s *OpenAIGatewayService) readOpenAICompatBufferedTerminal(
	resp *http.Response,
	logPrefix string,
	requestID string,
) (*apicompat.ResponsesResponse, OpenAIUsage, *apicompat.BufferedResponseAccumulator, error) {
	acc := apicompat.NewBufferedResponseAccumulator()
	var usage OpenAIUsage
	if resp == nil || resp.Body == nil {
		return nil, usage, acc, errors.New("upstream response body is nil")
	}

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var timeoutCh <-chan time.Time
	var timeoutTimer *time.Timer
	resetTimeout := func() {
		if streamInterval <= 0 {
			return
		}
		if timeoutTimer == nil {
			timeoutTimer = time.NewTimer(streamInterval)
			timeoutCh = timeoutTimer.C
			return
		}
		if !timeoutTimer.Stop() {
			select {
			case <-timeoutTimer.C:
			default:
			}
		}
		timeoutTimer.Reset(streamInterval)
	}
	stopTimeout := func() {
		if timeoutTimer == nil {
			return
		}
		if !timeoutTimer.Stop() {
			select {
			case <-timeoutTimer.C:
			default:
			}
		}
	}
	resetTimeout()
	defer stopTimeout()

	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	go func() {
		defer close(events)
		for scanner.Scan() {
			select {
			case events <- scanEvent{line: scanner.Text()}:
			case <-done:
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- scanEvent{err: err}:
			case <-done:
			}
		}
	}()
	defer close(done)

	var parser openAICompatSSEFrameParser
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if frame, ok := parser.Finish(); ok {
					payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)
					var event apicompat.ResponsesStreamEvent
					if err := json.Unmarshal([]byte(payload), &event); err == nil {
						acc.ProcessEvent(&event)
						if isOpenAICompatResponsesTerminalEvent(event.Type) && event.Response != nil {
							if event.Usage != nil {
								usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
								if event.Response.Usage == nil {
									event.Response.Usage = event.Usage
								}
							}
							if event.Response.Usage != nil {
								usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
							}
							return event.Response, usage, acc, nil
						}
					}
				}
				return nil, usage, acc, nil
			}
			resetTimeout()
			if ev.err != nil {
				if !errors.Is(ev.err, context.Canceled) && !errors.Is(ev.err, context.DeadlineExceeded) {
					logger.L().Warn(logPrefix+": read error",
						zap.Error(ev.err),
						zap.String("request_id", requestID),
					)
				}
				return nil, usage, acc, ev.err
			}

			if isOpenAICompatDoneSentinelLine(ev.line) {
				return nil, usage, acc, nil
			}
			frame, ok := parser.AddLine(ev.line)
			if !ok {
				continue
			}
			payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)

			var event apicompat.ResponsesStreamEvent
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				logger.L().Warn(logPrefix+": failed to parse event",
					zap.Error(err),
					zap.String("request_id", requestID),
				)
				continue
			}

			acc.ProcessEvent(&event)

			if isOpenAICompatResponsesTerminalEvent(event.Type) && event.Response != nil {
				if event.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
					if event.Response.Usage == nil {
						event.Response.Usage = event.Usage
					}
				}
				if event.Response.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
				}
				return event.Response, usage, acc, nil
			}

		case <-timeoutCh:
			_ = resp.Body.Close()
			logger.L().Warn(logPrefix+": data interval timeout",
				zap.String("request_id", requestID),
				zap.Duration("interval", streamInterval),
			)
			return nil, usage, acc, fmt.Errorf("stream data interval timeout")
		}
	}
}

// handleAnthropicStreamingResponse reads Responses SSE events from upstream,
// converts each to Anthropic SSE events, and writes them to the client.
// When StreamKeepaliveInterval is configured, it uses a goroutine + channel
// pattern to send Anthropic ping events during periods of upstream silence,
// preventing proxy/client timeout disconnections.
func (s *OpenAIGatewayService) handleAnthropicStreamingResponse(
	resp *http.Response,
	c *gin.Context,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	headersWritten := false
	writeStreamHeaders := func() {
		if headersWritten {
			return
		}
		headersWritten = true
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
	}

	state := apicompat.NewResponsesEventToAnthropicState()
	state.Model = originalModel
	var usage OpenAIUsage
	responseID := ""
	contentTextLen := 0
	var firstTokenMs *int
	firstChunk := true
	clientDisconnected := false
	clientOutputStarted := false

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)

	streamInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamDataIntervalTimeout > 0 {
		streamInterval = time.Duration(s.cfg.Gateway.StreamDataIntervalTimeout) * time.Second
	}
	var intervalTicker *time.Ticker
	if streamInterval > 0 {
		intervalTicker = time.NewTicker(streamInterval)
		defer intervalTicker.Stop()
	}
	var intervalCh <-chan time.Time
	if intervalTicker != nil {
		intervalCh = intervalTicker.C
	}

	// resultWithUsage builds the final result snapshot.
	resultWithUsage := func() *OpenAIForwardResult {
		return &OpenAIForwardResult{
			RequestID:        requestID,
			ResponseID:       responseID,
			Usage:            usage,
			Model:            originalModel,
			BillingModel:     billingModel,
			UpstreamModel:    upstreamModel,
			Stream:           true,
			Duration:         time.Since(startTime),
			FirstTokenMs:     firstTokenMs,
			StopReason:       state.StopReason,
			IncompleteReason: state.IncompleteReason,
			ContentTextLen:   contentTextLen,
			ClientDisconnect: clientDisconnected,
		}
	}

	// processDataLine handles a single "data: ..." SSE line from upstream.
	processDataLine := func(payload string) bool {
		if firstChunk {
			firstChunk = false
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			logger.L().Warn("openai messages stream: failed to parse event",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
			return false
		}

		// 仅按兼容转换器支持的终止事件提取 usage，避免无意扩大事件语义。
		switch event.Type {
		case "response.output_text.delta", "response.reasoning_summary_text.delta":
			if event.Delta != "" {
				contentTextLen += utf8.RuneCountInString(event.Delta)
			}
		}

		// Nested evt.Response.Usage takes precedence over top-level evt.Usage:
		// upstream's spec puts usage under response.usage; some compat upstreams
		// duplicate it at the top level. When both are present, nested wins.
		isTerminalEvent := isOpenAICompatResponsesTerminalEvent(event.Type)
		if isTerminalEvent {
			if event.Response != nil {
				if id := strings.TrimSpace(event.Response.ID); id != "" {
					responseID = id
				}
				if event.Response.Usage != nil {
					usage = copyOpenAIUsageFromResponsesUsage(event.Response.Usage)
				}
			}
			if event.Usage != nil {
				usage = copyOpenAIUsageFromResponsesUsage(event.Usage)
			}
			// cyber_policy 致命不可重试：标记供 handler 事后记录；以 Anthropic SSE error 事件
			// 回写让客户端感知并停止重试（F4），丢弃后续转换输出。
			if strings.TrimSpace(event.Type) == "response.failed" {
				if hit, code, msg := detectOpenAICyberPolicy([]byte(payload)); hit {
					MarkOpsCyberPolicy(c, CyberPolicyMark{
						Code:           code,
						Message:        msg,
						Body:           truncateString(payload, 4096),
						UpstreamStatus: http.StatusOK,
						UpstreamInTok:  usage.InputTokens,
						UpstreamOutTok: usage.OutputTokens,
					})
					if !clientDisconnected {
						writeStreamHeaders()
						clientMsg := msg
						if clientMsg == "" {
							clientMsg = "Request blocked by upstream cyber-security policy"
						}
						if _, err := fmt.Fprint(c.Writer, buildAnthropicStreamErrorSSE("invalid_request_error", clientMsg)); err == nil {
							c.Writer.Flush()
						}
						clientDisconnected = true
					}
					return true
				}
			}
		}

		// Convert to Anthropic events
		events := apicompat.ResponsesEventToAnthropicEvents(&event, state)
		if !clientDisconnected {
			for _, evt := range events {
				sse, err := apicompat.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					logger.L().Warn("openai messages stream: failed to marshal event",
						zap.Error(err),
						zap.String("request_id", requestID),
					)
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected, continuing to drain upstream for billing",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
		}
		if len(events) > 0 && !clientDisconnected {
			c.Writer.Flush()
		}
		return isTerminalEvent
	}

	// finalizeStream sends any remaining Anthropic events and returns the result.
	// US-027 safety net (empty-content schema firewall) lives one layer down in
	// apicompat: resToAnthHandleCompleted and FinalizeResponsesAnthropicStream
	// both call ensureContentBlockEmittedAsEmptyText to guarantee at least one
	// content_block_start/_stop pair before message_delta/_stop. See
	// docs/approved/openai-codex-as-claude-thinking-continuity.md §2.1.
	finalizeStream := func() (*OpenAIForwardResult, error) {
		if finalEvents := apicompat.FinalizeResponsesAnthropicStream(state); len(finalEvents) > 0 && !clientDisconnected {
			for _, evt := range finalEvents {
				sse, err := apicompat.ResponsesAnthropicEventToSSE(evt)
				if err != nil {
					continue
				}
				writeStreamHeaders()
				if _, err := fmt.Fprint(c.Writer, sse); err != nil {
					clientDisconnected = true
					logger.L().Info("openai messages stream: client disconnected during final flush",
						zap.String("request_id", requestID),
					)
					break
				}
				clientOutputStarted = true
			}
			if !clientDisconnected {
				c.Writer.Flush()
			}
		}
		return resultWithUsage(), nil
	}

	// handleScanErr logs scanner errors if meaningful.
	handleScanErr := func(err error) {
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			logger.L().Warn("openai messages stream: read error",
				zap.Error(err),
				zap.String("request_id", requestID),
			)
		}
	}
	missingTerminalErr := func() (*OpenAIForwardResult, error) {
		result := resultWithUsage()
		if clientDisconnected {
			return result, fmt.Errorf("stream usage incomplete: missing terminal event")
		}
		message := "OpenAI messages stream ended before a terminal event"
		if !clientOutputStarted {
			return result, s.newOpenAIStreamFailoverError(c, account, false, requestID, nil, message)
		}
		s.recordOpenAIMessagesStreamUpstreamError(c, account, requestID, "stream_missing_terminal", message)
		return result, fmt.Errorf("stream usage incomplete: missing terminal event")
	}
	processFrame := func(frame openAICompatSSEFrame) bool {
		payload := openAICompatPayloadWithEventType(frame.Data, frame.EventType)
		return processDataLine(payload)
	}

	// ── Determine keepalive interval ──
	keepaliveInterval := time.Duration(0)
	if s.cfg != nil && s.cfg.Gateway.StreamKeepaliveInterval > 0 {
		keepaliveInterval = time.Duration(s.cfg.Gateway.StreamKeepaliveInterval) * time.Second
	}

	// ── No keepalive: fast synchronous path (no goroutine overhead) ──
	if streamInterval <= 0 && keepaliveInterval <= 0 {
		var parser openAICompatSSEFrameParser
		for scanner.Scan() {
			line := scanner.Text()
			if isOpenAICompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		if err := scanner.Err(); err != nil {
			handleScanErr(err)
			return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", err)
		}
		if frame, ok := parser.Finish(); ok {
			if strings.TrimSpace(frame.Data) == "[DONE]" {
				return missingTerminalErr()
			}
			if processFrame(frame) {
				return finalizeStream()
			}
		}
		return missingTerminalErr()
	}

	// ── With keepalive: goroutine + channel + select ──
	type scanEvent struct {
		line string
		err  error
	}
	events := make(chan scanEvent, 16)
	done := make(chan struct{})
	var lastReadAt int64
	atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
	sendEvent := func(ev scanEvent) bool {
		select {
		case events <- ev:
			return true
		case <-done:
			return false
		}
	}
	go func() {
		defer close(events)
		for scanner.Scan() {
			atomic.StoreInt64(&lastReadAt, time.Now().UnixNano())
			if !sendEvent(scanEvent{line: scanner.Text()}) {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			_ = sendEvent(scanEvent{err: err})
		}
	}()
	defer close(done)

	var keepaliveTicker *time.Ticker
	if keepaliveInterval > 0 {
		keepaliveTicker = time.NewTicker(keepaliveInterval)
		defer keepaliveTicker.Stop()
	}
	var keepaliveCh <-chan time.Time
	if keepaliveTicker != nil {
		keepaliveCh = keepaliveTicker.C
	}
	lastDataAt := time.Now()
	var parser openAICompatSSEFrameParser

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Upstream closed
				if frame, ok := parser.Finish(); ok {
					if strings.TrimSpace(frame.Data) == "[DONE]" {
						return missingTerminalErr()
					}
					if processFrame(frame) {
						return finalizeStream()
					}
				}
				return missingTerminalErr()
			}
			if ev.err != nil {
				handleScanErr(ev.err)
				return resultWithUsage(), fmt.Errorf("stream usage incomplete: %w", ev.err)
			}
			lastDataAt = time.Now()
			line := ev.line
			if isOpenAICompatDoneSentinelLine(line) {
				return missingTerminalErr()
			}
			frame, ok := parser.AddLine(line)
			if !ok {
				continue
			}
			if processFrame(frame) {
				return finalizeStream()
			}

		case <-intervalCh:
			lastRead := time.Unix(0, atomic.LoadInt64(&lastReadAt))
			if time.Since(lastRead) < streamInterval {
				continue
			}
			if clientDisconnected {
				return resultWithUsage(), fmt.Errorf("stream usage incomplete after timeout")
			}
			logger.L().Warn("openai messages stream: data interval timeout",
				zap.String("request_id", requestID),
				zap.String("model", originalModel),
				zap.Duration("interval", streamInterval),
			)
			return resultWithUsage(), fmt.Errorf("stream data interval timeout")

		case <-keepaliveCh:
			if clientDisconnected {
				continue
			}
			if time.Since(lastDataAt) < keepaliveInterval {
				continue
			}
			// Send Anthropic-format ping event
			writeStreamHeaders()
			if _, err := fmt.Fprint(c.Writer, "event: ping\ndata: {\"type\":\"ping\"}\n\n"); err != nil {
				// Client disconnected
				logger.L().Info("openai messages stream: client disconnected during keepalive",
					zap.String("request_id", requestID),
				)
				clientDisconnected = true
				continue
			}
			clientOutputStarted = true
			c.Writer.Flush()
		}
	}
}

// writeAnthropicError writes an error response in Anthropic Messages API format.
func writeAnthropicError(c *gin.Context, statusCode int, errType, message string) {
	c.JSON(statusCode, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
}

// buildAnthropicStreamErrorSSE builds one Anthropic SSE `error` event so a
// streaming response can terminate with a visible error (e.g. upstream
// cyber_policy) and programmatic clients stop retrying.
// Marshal 失败的兜底仅保留固定提示。
func buildAnthropicStreamErrorSSE(errType, message string) string {
	payload, err := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	if err != nil {
		return "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + errType + "\",\"message\":\"upstream error\"}}\n\n"
	}
	return "event: error\ndata: " + string(payload) + "\n\n"
}

func copyOpenAIUsageFromResponsesUsage(usage *apicompat.ResponsesUsage) OpenAIUsage {
	if usage == nil {
		return OpenAIUsage{}
	}
	result := OpenAIUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}
	if usage.InputTokensDetails != nil {
		result.CacheReadInputTokens = usage.InputTokensDetails.CachedTokens
	}
	return result
}

func isGrokResponsesModelNotSupportedRetryable(statusCode int, upstreamMsg string, upstreamBody []byte) bool {
	if statusCode != http.StatusBadRequest && statusCode != http.StatusNotFound && statusCode != http.StatusUnprocessableEntity {
		return false
	}
	check := func(message string) bool {
		lower := strings.ToLower(strings.TrimSpace(message))
		if lower == "" {
			return false
		}
		if strings.Contains(lower, "model_not_supported") || strings.Contains(lower, "unsupported_model") {
			return true
		}
		if strings.Contains(lower, "model") && (strings.Contains(lower, "not supported") || strings.Contains(lower, "unsupported")) {
			return true
		}
		if strings.Contains(lower, "responses") && strings.Contains(lower, "not support") {
			return true
		}
		return false
	}
	if check(upstreamMsg) || check(string(upstreamBody)) {
		return true
	}
	return check(gjson.GetBytes(upstreamBody, "error.code").String()) ||
		check(gjson.GetBytes(upstreamBody, "error.message").String())
}

func (s *OpenAIGatewayService) fallbackAnthropicToGrokChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	anthropicReq *apicompat.AnthropicRequest,
	clientStream bool,
	originalModel string,
	billingModel string,
	upstreamModel string,
	token string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	chatBody, err := anthropicToChatCompletionsBody(anthropicReq, upstreamModel)
	if err != nil {
		return nil, fmt.Errorf("fallback convert anthropic to chat completions: %w", err)
	}
	targetURL, err := s.resolveGrokChatCompletionsUpstream(account)
	if err != nil {
		return nil, err
	}
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	upstreamReq, err := buildGrokChatCompletionsRequest(upstreamCtx, c, targetURL, chatBody, token)
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("fallback build grok chat completions request: %w", err)
	}

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	defer func() { _ = resp.Body.Close() }()

	s.updateGrokUsageSnapshot(ctx, account.ID, xai.ParseQuotaHeaders(resp.Header, resp.StatusCode))
	if resp.StatusCode >= 400 {
		respBody := s.readUpstreamErrorBody(resp)
		resp.Body = io.NopCloser(bytes.NewReader(respBody))
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	upstreamBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("fallback read grok chat completions body: %w", err)
	}

	buf := bytes.NewBuffer(upstreamBody)
	if clientStream {
		return convertBufferedChatCompletionsToAnthropicSSE(c, buf, originalModel, billingModel, upstreamModel, startTime)
	}
	return convertBufferedChatCompletionsToAnthropicJSON(c, buf, originalModel, billingModel, upstreamModel, startTime)
}

func (s *OpenAIGatewayService) resolveGrokChatCompletionsUpstream(account *Account) (string, error) {
	if s == nil {
		return "", fmt.Errorf("openai gateway service is nil")
	}
	if account == nil {
		return "", fmt.Errorf("grok chat completions: account is nil")
	}
	switch {
	case account.IsGrokOAuth():
		return buildOpenAIChatCompletionsURL(account.GetGrokBaseURL()), nil
	case account.IsGrokAPIKey():
		baseURL := account.GetOpenAIBaseURL()
		if strings.TrimSpace(baseURL) == "" {
			return "", fmt.Errorf("grok relay account %d missing base_url", account.ID)
		}
		validatedURL, err := s.validateUpstreamBaseURLForAccount(account, baseURL)
		if err != nil {
			return "", err
		}
		return buildOpenAIChatCompletionsURL(validatedURL), nil
	default:
		return "", fmt.Errorf("grok account type %s is not supported for chat completions fallback", account.Type)
	}
}

func buildGrokChatCompletionsRequest(ctx context.Context, c *gin.Context, targetURL string, body []byte, token string) (*http.Request, error) {
	if strings.TrimSpace(targetURL) == "" {
		return nil, fmt.Errorf("grok chat completions target URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", "sub2api-grok/1.0")
	if c != nil {
		if v := strings.TrimSpace(c.GetHeader("User-Agent")); v != "" {
			req.Header.Set("User-Agent", v)
		}
	}
	return req, nil
}
