package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

var newAPIResponsesChatFallbackErrorPatterns = []string{
	"not supported",
	"unsupported model",
	"stream mode",
	"enable_thinking",
	"invalid model",
	"convert request failed",
}

func isNewAPIResponsesConvertNotImplemented(apiErr *newapitypes.NewAPIError) bool {
	if apiErr == nil || apiErr.GetErrorCode() != newapitypes.ErrorCodeConvertRequestFailed {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(apiErr.Error()))
	return strings.Contains(msg, "not implemented")
}

func shouldFallbackNewAPIResponsesToChat(apiErr *newapitypes.NewAPIError) bool {
	if isNewAPIResponsesConvertNotImplemented(apiErr) {
		return true
	}
	if apiErr == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(apiErr.Error()))
	if msg == "" {
		return false
	}
	has4xxHint := strings.Contains(msg, "400") || strings.Contains(msg, "404") || strings.Contains(msg, "status code: 4")
	for _, pat := range newAPIResponsesChatFallbackErrorPatterns {
		if strings.Contains(msg, pat) && (has4xxHint || pat == "convert request failed") {
			return true
		}
	}
	return false
}

func applyNewAPIResponsesChatFallbackShape(model string, chatBody []byte) []byte {
	trimmedModel := strings.TrimSpace(strings.ToLower(model))
	if trimmedModel == "" || len(chatBody) == 0 {
		return chatBody
	}

	requiresStream := isNewAPIResponsesChatFallbackStreamModel(trimmedModel)
	isQwenPreview := isNewAPIResponsesQwen37PreviewVariant(trimmedModel)
	isQwen3 := strings.HasPrefix(trimmedModel, "qwen3-") || strings.HasPrefix(trimmedModel, "qwen3.")
	if !requiresStream && !isQwenPreview && !isQwen3 {
		return chatBody
	}

	var payload map[string]any
	if err := json.Unmarshal(chatBody, &payload); err != nil {
		return chatBody
	}

	if requiresStream {
		payload["stream"] = true
	}
	if isQwenPreview {
		payload["enable_thinking"] = true
	} else if isQwen3 {
		payload["enable_thinking"] = false
	}

	shaped, err := json.Marshal(payload)
	if err != nil {
		return chatBody
	}
	return shaped
}

func chatFallbackUpstreamRequiresStream(chatBody []byte) bool {
	if len(chatBody) == 0 {
		return false
	}
	return gjson.GetBytes(chatBody, "stream").Bool()
}

func ensureNewAPIChatFallbackStreamOptions(chatBody []byte) []byte {
	if !chatFallbackUpstreamRequiresStream(chatBody) {
		return chatBody
	}
	if gjson.GetBytes(chatBody, "stream_options.include_usage").Bool() {
		return chatBody
	}

	var payload map[string]any
	if err := json.Unmarshal(chatBody, &payload); err != nil {
		return chatBody
	}
	payload["stream_options"] = map[string]any{"include_usage": true}
	shaped, err := json.Marshal(payload)
	if err != nil {
		return chatBody
	}
	return shaped
}

func isNewAPIResponsesChatFallbackStreamModel(model string) bool {
	return model == "glm-4.5" ||
		model == "glm-4.5-air" ||
		isNewAPIResponsesQwen37PreviewVariant(model)
}

func isNewAPIResponsesQwen37PreviewVariant(model string) bool {
	return model == "qwen3.7-max-preview" || model == "qwen3.7-max-2026-05-17"
}

func (s *OpenAIGatewayService) forwardResponsesViaNewAPIBridgeChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	in bridge.ChannelContextInput,
) (*OpenAIForwardResult, error) {
	startTime := time.Now()
	if c == nil {
		return nil, fmt.Errorf("nil gin context")
	}

	var responsesReq apicompat.ResponsesRequest
	if err := json.Unmarshal(body, &responsesReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "Failed to parse request body",
			},
		})
		return nil, fmt.Errorf("parse responses request: %w", err)
	}

	originalModel := strings.TrimSpace(responsesReq.Model)
	if originalModel == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": "model is required",
			},
		})
		return nil, fmt.Errorf("missing model in request")
	}

	clientStream := responsesReq.Stream
	reasoningEffort := extractOpenAIReasoningEffortFromBody(body, originalModel)
	serviceTier := extractOpenAIServiceTierFromBody(body)

	chatReq, err := apicompat.ResponsesToChatCompletionsRequest(&responsesReq)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{
				"type":    "invalid_request_error",
				"message": err.Error(),
			},
		})
		return nil, fmt.Errorf("convert responses to chat completions: %w", err)
	}

	billingModel := resolveOpenAIForwardModel(account, originalModel, "")
	upstreamModel := normalizeOpenAIModelForUpstream(account, billingModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, body, billingModel)
	chatReq.Model = upstreamModel
	if clientStream {
		chatReq.StreamOptions = &apicompat.ChatStreamOptions{IncludeUsage: true}
	}

	chatBody, err := marshalOpenAIUpstreamJSON(chatReq)
	if err != nil {
		return nil, fmt.Errorf("marshal bridge chat fallback request: %w", err)
	}
	chatBody = applyNewAPIResponsesChatFallbackShape(upstreamModel, chatBody)
	chatBody = ensureNewAPIChatFallbackStreamOptions(chatBody)
	upstreamStream := chatFallbackUpstreamRequiresStream(chatBody)
	chatBody = applyStickyToNewAPIBridge(ctx, c, s.settingService, account, chatBody, upstreamModel)
	if serviceTier == nil {
		serviceTier = extractOpenAIServiceTierFromBody(chatBody)
	}

	logger.L().Debug("openai responses: forwarding newapi bridge via chat completions",
		zap.Int64("account_id", account.ID),
		zap.Int("channel_type", account.ChannelType),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	recordBridgeDispatch()
	var buf bytes.Buffer
	captureWriter := newBridgeCaptureWriter(&buf)
	origWriter := c.Writer
	origPath := ""
	if c.Request != nil && c.Request.URL != nil {
		origPath = c.Request.URL.Path
	}

	var dispatchPanic any
	var out *bridge.DispatchOutcome
	var apiErr *newapitypes.NewAPIError
	func() {
		defer func() {
			c.Writer = origWriter
			if c.Request != nil && c.Request.URL != nil {
				c.Request.URL.Path = origPath
			}
			if r := recover(); r != nil {
				dispatchPanic = r
				logger.L().Error("openai_gateway.bridge_responses_chat_fallback_panic",
					zap.Int64("account_id", account.ID),
					zap.Int("channel_type", account.ChannelType),
					zap.String("panic", fmt.Sprintf("%v", r)),
					zap.String("stack", string(debug.Stack())),
				)
			}
		}()
		c.Writer = captureWriter
		if c.Request != nil && c.Request.URL != nil {
			c.Request.URL.Path = "/v1/chat/completions"
		}
		out, apiErr = dispatchNewAPIChatCompletions(ctx, c, in, chatBody)
	}()

	if dispatchPanic != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Bridge dispatch panicked"}})
		return nil, fmt.Errorf("bridge chat fallback dispatch panic: %v", dispatchPanic)
	}
	if apiErr != nil {
		recordBridgeDispatchError()
		logger.L().Info("openai_gateway.newapi_bridge_dispatch",
			zap.String("endpoint", "responses_via_chat_completions"),
			zap.Int("channel_type", account.ChannelType),
			zap.String("bridge_path", "newapi_adaptor_error"),
			zap.Int64("account_id", account.ID),
		)
		return nil, s.tkWrapBridgeRelayErrorWithPenalty(ctx, c, account, apiErr)
	}
	if captureWriter.statusCode >= 400 {
		statusCode := captureWriter.statusCode
		if statusCode < 400 || statusCode > 599 {
			statusCode = http.StatusBadGateway
		}
		contentType := strings.TrimSpace(captureWriter.header.Get("Content-Type"))
		if contentType != "" && buf.Len() > 0 {
			c.Data(statusCode, contentType, buf.Bytes())
		} else {
			c.JSON(statusCode, gin.H{"error": gin.H{"type": "api_error", "message": "Bridge fallback upstream returned error"}})
		}
		return nil, fmt.Errorf("bridge chat fallback upstream error (status %d)", captureWriter.statusCode)
	}
	if out == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"type": "api_error", "message": "Bridge fallback returned no result"}})
		return nil, fmt.Errorf("bridge chat fallback returned nil outcome")
	}

	bridgeUpstream := strings.TrimSpace(out.UpstreamModel)
	if bridgeUpstream == "" {
		bridgeUpstream = out.Model
	}
	resp := &http.Response{
		StatusCode: captureWriter.statusCode,
		Header:     captureWriter.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(buf.Bytes())),
	}

	var result *OpenAIForwardResult
	var handleErr error
	switch {
	case clientStream:
		result, handleErr = s.streamChatCompletionsAsResponses(
			c, resp, originalModel, billingModel, bridgeUpstream, reasoningEffort, serviceTier, startTime)
	case upstreamStream:
		result, handleErr = s.bufferStreamChatCompletionsAsResponses(
			c, resp, originalModel, billingModel, bridgeUpstream, reasoningEffort, serviceTier, startTime)
	default:
		result, handleErr = s.bufferChatCompletionsAsResponses(
			c, resp, originalModel, billingModel, bridgeUpstream, reasoningEffort, serviceTier, startTime)
	}
	if handleErr == nil && result != nil {
		if out.Usage != nil {
			result.Usage = openAIUsageFromNewAPIDTO(out.Usage)
		}
		result.EnableThinking = tkThinkingModeActiveFromBody(body)
	}
	return result, handleErr
}
