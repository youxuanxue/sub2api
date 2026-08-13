package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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
) (*OpenAIForwardResult, error) {
	startTime := time.Now()

	originalModel := strings.TrimSpace(gjson.GetBytes(body, "model").String())
	if originalModel == "" {
		writeAnthropicError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)

	upstreamBody := body
	if upstreamModel != originalModel {
		upstreamBody = ReplaceModelInBody(body, upstreamModel)
	}

	targetURL, err := s.nativeAnthropicMessagesTargetURL(account)
	if err != nil {
		return nil, err
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

	resp, err := s.sendNativeAnthropicMessagesRequest(ctx, c, account, targetURL, upstreamBody, clientStream, apiKey)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleAnthropicErrorResponse(resp, c, account, billingModel)
	}

	if clientStream {
		return s.streamNativeAnthropicMessages(c, resp, originalModel, billingModel, upstreamModel, startTime)
	}
	return s.bufferNativeAnthropicMessages(c, resp, originalModel, billingModel, upstreamModel, startTime)
}

func (s *OpenAIGatewayService) nativeAnthropicMessagesTargetURL(account *Account) (string, error) {
	baseURL := nativeOpenAIBaseURLForAccount(account)
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	validatedURL, err := s.validateUpstreamBaseURL(baseURL)
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
) (*http.Response, error) {
	upstreamCtx, releaseUpstreamCtx := detachUpstreamContext(ctx)
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

	proxyURL := ""
	if account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.httpUpstream.Do(upstreamReq, proxyURL, account.ID, account.Concurrency)
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

	var usage OpenAIUsage
	if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsedUsage
	}

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
		Usage:         usage,
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
	originalModel string,
	billingModel string,
	upstreamModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)
	scanner := s.newUpstreamSSEScanner(resp.Body)

	var usage OpenAIUsage
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

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload != "" && payload != "[DONE]" {
				if u, ok := extractOpenAIUsageFromJSONBytes([]byte(payload)); ok {
					usage = u
				}
				if firstTokenMs == nil && anthropicStreamPayloadHasOutput(payload) {
					elapsed := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &elapsed
				}
			}
		}
		writeChunk(line + "\n")
		if !clientDisconnected {
			c.Writer.Flush()
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.L().Warn("openai messages native: stream read error",
			zap.Error(err),
			zap.String("request_id", requestID),
		)
	}

	return &OpenAIForwardResult{
		RequestID:     requestID,
		Usage:         usage,
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
