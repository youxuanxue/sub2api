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

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// tkCCPreservesKiroUpstreamStream reports whether /v1/chat/completions should
// forward the client's stream preference to Kiro upstream instead of forcing SSE.
func tkCCPreservesKiroUpstreamStream(account *Account, clientStream bool) bool {
	if clientStream || account == nil {
		return clientStream
	}
	return account.IsKiro()
}

// forwardAsChatCompletionsViaKiro routes CC→Anthropic converted bodies through
// the Kiro gateway (EventStream upstream) instead of the Anthropic HTTP API,
// then converts the captured Anthropic response back to Chat Completions.
func (s *GatewayService) forwardAsChatCompletionsViaKiro(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	ccBody []byte,
	originalModel, mappedModel string,
	anthropicBody []byte,
	clientStream bool,
	includeUsage bool,
	startTime time.Time,
) (*ForwardResult, error) {
	if s.kiroGateway == nil {
		return nil, fmt.Errorf("kiro gateway service not configured")
	}

	upstreamStream := clientStream
	var err error
	if upstreamStream {
		anthropicBody, err = sjson.SetBytes(anthropicBody, "stream", true)
	} else {
		anthropicBody, err = sjson.SetBytes(anthropicBody, "stream", false)
	}
	if err != nil {
		return nil, fmt.Errorf("set anthropic stream for kiro bridge: %w", err)
	}
	anthropicBody = applyKiroChatCompletionsReasoningEffort(ccBody, anthropicBody)

	kiroParsed := &ParsedRequest{
		Body:   NewRequestBodyRef(anthropicBody),
		Model:  mappedModel,
		Stream: upstreamStream,
	}
	if kiroParsed.Model == "" {
		kiroParsed.Model = originalModel
	}

	var captureBuf bytes.Buffer
	captureWriter := newBridgeCaptureWriter(&captureBuf)
	origWriter := c.Writer
	origCommitted := IsResponseCommitted(c)
	c.Writer = captureWriter

	fwdResult, err := s.kiroGateway.Forward(ctx, c, account, kiroParsed, startTime)
	c.Writer = origWriter
	// Errors written into the capture have not reached the real client.
	c.Set(ResponseCommittedKey, origCommitted)
	if err != nil {
		var contentFilteredErr *KiroContentFilteredError
		if errors.As(err, &contentFilteredErr) {
			MarkOpsClientContentFiltered(c)
			writeGatewayCCError(c, http.StatusBadRequest, "content_filter_error", KiroContentFilteredClientMessage())
			return nil, contentFilteredErr
		}
		var invalidRequestErr *KiroInvalidRequestError
		if errors.As(err, &invalidRequestErr) {
			writeGatewayCCError(
				c,
				invalidRequestErr.ClientStatusCode(),
				invalidRequestErr.ClientErrorType(),
				invalidRequestErr.ClientMessage(),
			)
			return nil, invalidRequestErr
		}
		if s.rateLimitService != nil {
			var failoverErr *UpstreamFailoverError
			if errors.As(err, &failoverErr) && failoverErr.Scope != GatewayFailureScopeRequest {
				s.rateLimitService.HandleUpstreamError(ctx, account, failoverErr.StatusCode, failoverErr.ResponseHeaders, failoverErr.ResponseBody)
			}
		}
		return nil, err
	}

	upstreamResp := &http.Response{
		StatusCode: captureWriter.statusCode,
		Header:     captureWriter.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(captureBuf.Bytes())),
	}
	if upstreamResp.StatusCode == 0 {
		upstreamResp.StatusCode = http.StatusOK
	}

	reasoningEffort := ExtractChatCompletionsReasoningEffortFromBody(ccBody)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, ccBody, mappedModel)

	var result *ForwardResult
	if clientStream {
		result, err = s.handleCCStreamingFromAnthropic(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else if isAnthropicMessagesJSONResponse(upstreamResp) {
		result, err = s.handleCCBufferedFromAnthropicJSON(upstreamResp, c, account, originalModel, mappedModel, reasoningEffort, startTime)
	} else {
		result, err = s.handleCCBufferedFromAnthropic(upstreamResp, c, account, originalModel, mappedModel, reasoningEffort, startTime)
	}
	if err != nil {
		return nil, err
	}
	if result != nil && fwdResult != nil {
		if fwdResult.BillingTier != "" {
			result.BillingTier = fwdResult.BillingTier
		}
		if fwdResult.RequestID != "" {
			result.RequestID = fwdResult.RequestID
		}
		if fwdResult.Usage.InputTokens > 0 || fwdResult.Usage.OutputTokens > 0 {
			result.Usage = fwdResult.Usage
		}
		if fwdResult.UpstreamModel != "" {
			result.UpstreamModel = fwdResult.UpstreamModel
		}
	}
	return result, nil
}

// applyKiroChatCompletionsReasoningEffort copies Chat Completions
// reasoning_effort onto output_config.effort. The shared converter maps xhigh
// to max; Kiro must receive the caller's value, including xhigh and max.
func applyKiroChatCompletionsReasoningEffort(ccBody, anthropicBody []byte) []byte {
	raw := strings.TrimSpace(gjson.GetBytes(ccBody, "reasoning_effort").String())
	if raw == "" {
		raw = strings.TrimSpace(gjson.GetBytes(ccBody, "reasoning.effort").String())
	}
	effort := kiroPassthroughReasoningEffort(raw)
	if effort == "" {
		return anthropicBody
	}
	updated, err := sjson.SetBytes(anthropicBody, "output_config.effort", effort)
	if err != nil {
		return anthropicBody
	}
	if strings.TrimSpace(gjson.GetBytes(updated, "thinking.type").String()) == "" {
		withThinking, thinkErr := sjson.SetBytes(updated, "thinking.type", "adaptive")
		if thinkErr != nil {
			return updated
		}
		updated = withThinking
	}
	return updated
}

func kiroPassthroughReasoningEffort(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return ""
	}
	value = strings.NewReplacer("-", "", "_", "", " ", "").Replace(value)
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return value
	default:
		return ""
	}
}
