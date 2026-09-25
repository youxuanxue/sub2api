package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/apipath"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func shouldForwardNativeAnthropicMessagesForModel(body []byte) bool {
	model := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	return tkIsForwardableAnthropicModelName(model)
}

// forwardAnthropicViaNativeMessages serves /v1/messages clients by passthrough
// to an upstream that natively exposes Anthropic Messages (dual-stack OpenAI
// relays such as agent.tokensea.ai).
func (s *OpenAIGatewayService) forwardAnthropicViaNativeMessages(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (result *OpenAIForwardResult, forwardErr error) {
	startTime := time.Now()
	selectedModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	selectedModel = protocolExecutionResolvedModel(ctx, selectedModel)
	if !tkIsForwardableAnthropicModelName(selectedModel) && !cursorMappedModelAllowed(account, gjson.GetBytes(body, "model").String(), selectedModel) {
		return nil, fmt.Errorf("native anthropic messages requires a Claude model")
	}

	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if originalModel == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	billingModel, upstreamModel := resolveOpenAICompatForwardModels(account, originalModel, defaultMappedModel)
	upstreamModel = protocolExecutionResolvedModel(ctx, upstreamModel)
	billingModel = settleOpenAIBillingFromUpstream(billingModel, upstreamModel)

	upstreamBody := body
	if upstreamModel != originalModel {
		upstreamBody = ReplaceModelInBody(body, upstreamModel)
	}

	targetURL := strings.TrimSpace(protocolExecutionEndpoint(ctx, ""))
	if targetURL == "" {
		var err error
		targetURL, err = s.nativeAnthropicMessagesTargetURL(account)
		if err != nil {
			return nil, err
		}
	}

	apiKey := nativeOpenAIApiKeyForAccount(account)
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("account %d missing api_key", account.ID)
	}

	logger.L().Debug("openai messages: forwarding via native anthropic passthrough",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	resp, upstreamBody, err := s.sendNativeAnthropicMessagesRequest(ctx, c, account, targetURL, upstreamBody, clientStream, apiKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	defer func() { cursorResponseOutcome(account, resp, &result, &forwardErr) }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest {
			retryResp, _, retryBody, retryMsg, recovered := s.retryAnthropicThinkingContract400HTTP(
				ctx, c, account, upstreamModel, upstreamBody, resp, respBody, upstreamMsg,
				func(body []byte) (*http.Response, error) {
					return s.doNativeAnthropicMessagesHTTP(ctx, c, account, targetURL, body, clientStream, apiKey)
				},
				s.readOpenAIUpstreamError,
			)
			resp = retryResp
			respBody, upstreamMsg = retryBody, retryMsg
			if recovered {
				if clientStream {
					return s.streamNativeAnthropicMessages(c, resp, account, originalModel, billingModel, upstreamModel, startTime)
				}
				return s.bufferNativeAnthropicMessages(c, resp, originalModel, billingModel, upstreamModel, startTime)
			}
		}
		if resp.Body == nil {
			resp.Body = io.NopCloser(bytes.NewReader(respBody))
		}
		if forwardNativeMessagesPolicy(c, respBody, resp.StatusCode, nil, "messages", false, "", "") {
			return nil, errOpenAICyberPolicyForwarded
		}
		if foErr := s.failoverNativeMessagesUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if clientStream {
		return s.streamNativeAnthropicMessages(c, resp, account, originalModel, billingModel, upstreamModel, startTime)
	}
	return s.bufferNativeAnthropicMessages(c, resp, originalModel, billingModel, upstreamModel, startTime)
}

func (s *OpenAIGatewayService) nativeAnthropicMessagesTargetURL(account *Account) (string, error) {
	baseURL := nativeOpenAIBaseURLForAccount(account)
	if baseURL == "" {
		if !OfficialOpenAIFallbackAllowed(account) {
			return "", ErrForeignCredentialOfficialOpenAIFallback
		}
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := validateCursorBaseURL(account, baseURL, s.validateUpstreamBaseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base_url: %w", err)
	}
	return buildOpenAIEndpointURL(validatedURL, apipath.Messages), nil
}

func (s *OpenAIGatewayService) sendNativeAnthropicMessagesRequest(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	targetURL string,
	body []byte,
	stream bool,
	bearerToken string,
) (*http.Response, []byte, error) {
	mappedModel := tkMappedModelFromAnthropicBody(body, "")
	// Thinking-contract SSOT + tokensea fable CM. This path builds http.NewRequest
	// directly and does NOT call buildNativeAnthropicUpstreamRequest (prod
	// 2026-09-20 / 2026-09-25 user16).
	body = tkPrepareAnthropicMessagesWireBody(account, body, mappedModel)

	resp, err := s.doNativeAnthropicMessagesHTTP(ctx, c, account, targetURL, body, stream, bearerToken)
	return resp, body, err
}

// doNativeAnthropicMessagesHTTP sends an already-prepared Anthropic Messages body.
// Callers that need a thinking-contract 400 retry must prepare the repaired body
// themselves and call this helper so filters are not applied twice incorrectly.
func (s *OpenAIGatewayService) doNativeAnthropicMessagesHTTP(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	targetURL string,
	body []byte,
	stream bool,
	bearerToken string,
) (*http.Response, error) {
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
	if account.IsCursor() {
		upstreamCtx = ctx
	}
	upstreamReq, err := http.NewRequestWithContext(upstreamCtx, http.MethodPost, targetURL, bytes.NewReader(body))
	releaseUpstreamCtx()
	if err != nil {
		return nil, fmt.Errorf("build upstream request: %w", err)
	}
	upstreamReq = upstreamReq.WithContext(WithHTTPUpstreamProfile(upstreamReq.Context(), HTTPUpstreamProfileOpenAI))
	upstreamReq.Header.Set("Content-Type", "application/json")
	upstreamReq.Header.Set("Authorization", "Bearer "+bearerToken)
	if stream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	for key, values := range c.Request.Header {
		lowerKey := strings.ToLower(key)
		if openaiCCRawAllowedHeaders[lowerKey] {
			for _, v := range values {
				upstreamReq.Header.Add(key, v)
			}
		}
	}
	if ua := account.GetOpenAIUserAgent(); ua != "" {
		upstreamReq.Header.Set("user-agent", ua)
	}
	account.ApplyHeaderOverrides(upstreamReq.Header)
	if err := prepareCursorUpstreamRequest(upstreamReq, c, account); err != nil {
		return nil, err
	}

	hwka := s.beginAnthropicClientHeaderWaitKeepalive(c, stream)
	resp, err := s.doNativeMessagesRequest(upstreamReq, account)
	hwka.stop()
	if err != nil {
		return nil, s.handleOpenAIUpstreamTransportError(ctx, c, account, err, false)
	}
	return resp, nil
}

func (s *OpenAIGatewayService) bufferNativeAnthropicMessages(
	c *gin.Context,
	resp *http.Response,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			writeAnthropicError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}

	if forwardNativeMessagesPolicy(c, respBody, resp.StatusCode, nil, "messages", false, "", "") {
		return nil, errOpenAICyberPolicyForwarded
	}

	usage := parseClaudeUsageFromResponseBody(respBody)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	} else {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return &OpenAIForwardResult{
		RequestID:     requestID,
		Usage:         claudeUsageToOpenAIUsage(usage),
		BillingTier:   usage.BillingTier,
		Model:         originalModel,
		BillingModel:  billingModel,
		UpstreamModel: upstreamModel,
		Stream:        false,
		Duration:      time.Since(startTime),
	}, nil
}

func (s *OpenAIGatewayService) streamNativeAnthropicMessages(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)
	scanner := s.newUpstreamSSEScanner(resp.Body)

	var usage ClaudeUsage
	var firstTokenMs *int
	clientDisconnected := false
	headersWritten := false

	writeChunk := func(chunk string) {
		if clientDisconnected {
			return
		}
		if !headersWritten {
			writeStreamHeaders()
			headersWritten = true
		}
		if _, werr := c.Writer.WriteString(chunk); werr != nil {
			clientDisconnected = true
			logger.L().Debug("openai messages native: client disconnected, continuing to drain upstream for billing",
				zap.Error(werr),
				zap.String("request_id", requestID),
			)
		}
	}

	terminalError := false
	policyBlocked := false
	streamCompleted := false
	var upstreamErr *tkAnthropicBufferedUpstreamError
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" && payload != "[DONE]" {
				parseSSEUsagePassthrough(payload, &usage)
				if gjson.Get(payload, "type").String() == "message_stop" {
					streamCompleted = true
				}
				if gjson.Get(payload, "type").String() == "error" {
					terminalError = true
					upstreamErr, _ = tkParseAnthropicBufferedSSEError([]byte(payload), s.cfg)
					u := claudeUsageToOpenAIUsage(&usage)
					policyBlocked = markOpenAISafetyPolicyEvent(c, []byte(payload), resp.StatusCode, &u) != ""
				}
				if firstTokenMs == nil && anthropicStreamPayloadHasOutput(payload) {
					elapsed := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &elapsed
				}
			}
		}
		writeChunk(line + "\n")
		if !clientDisconnected {
			if line == "" && terminalError {
				MarkResponseCommitted(c)
			}
			c.Writer.Flush()
		}
		if line == "" && policyBlocked {
			return nil, errOpenAICyberPolicyForwarded
		}
		if line == "" && terminalError {
			if nativeErr := cursorMessagesResponseError(account, resp, upstreamErr); nativeErr != nil {
				return nil, nativeErr
			}
		}
	}
	if policyBlocked {
		return nil, errOpenAICyberPolicyForwarded
	}
	if !streamCompleted && upstreamErr == nil {
		upstreamErr = tkAnthropicBufferedSyntheticFailure("stream_incomplete", "Upstream stream ended before response completion")
	}
	if nativeErr := cursorMessagesResponseError(account, resp, upstreamErr); nativeErr != nil {
		return nil, nativeErr
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.L().Warn("openai messages native: stream read error",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
	}

	return &OpenAIForwardResult{
		RequestID:     requestID,
		Usage:         claudeUsageToOpenAIUsage(&usage),
		BillingTier:   usage.BillingTier,
		Model:         originalModel,
		BillingModel:  billingModel,
		UpstreamModel: upstreamModel,
		Stream:        true,
		FirstTokenMs:  firstTokenMs,
		Duration:      time.Since(startTime),
	}, nil
}

func anthropicStreamPayloadHasOutput(payload string) bool {
	eventType := strings.TrimSpace(gjson.Get(payload, "type").String())
	switch eventType {
	case "content_block_delta", "message_delta":
		return true
	case "message_start":
		return gjson.Get(payload, "message.content").Exists() && len(gjson.Get(payload, "message.content").Array()) > 0
	default:
		return false
	}
}
