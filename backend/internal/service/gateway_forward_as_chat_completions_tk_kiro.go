package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

// tkCCPreservesKiroUpstreamStream reports whether /v1/chat/completions should
// forward the client's stream preference to Kiro upstream instead of forcing SSE.
func tkCCPreservesKiroUpstreamStream(account *Account, clientStream bool) bool {
	if clientStream || account == nil {
		return clientStream
	}
	return account.IsKiro() || account.IsKiroMirrorStub()
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
	c.Writer = captureWriter

	fwdResult, err := s.kiroGateway.Forward(ctx, c, account, kiroParsed, startTime)
	c.Writer = origWriter
	if err != nil {
		var contentFilteredErr *KiroContentFilteredError
		if errors.As(err, &contentFilteredErr) {
			c.Header(KiroOutcomeHeader, KiroContentFilteredOutcome)
			writeGatewayCCError(c, http.StatusBadRequest, "content_filter_error", KiroContentFilteredClientMessage())
			return nil, contentFilteredErr
		}
		if s.rateLimitService != nil {
			var failoverErr *UpstreamFailoverError
			if errors.As(err, &failoverErr) {
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

	reasoningEffort := extractCCReasoningEffortFromBody(ccBody)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, ccBody, mappedModel)

	var result *ForwardResult
	if clientStream {
		result, err = s.handleCCStreamingFromAnthropic(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else if isAnthropicMessagesJSONResponse(upstreamResp) {
		result, err = s.handleCCBufferedFromAnthropicJSON(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime)
	} else {
		result, err = s.handleCCBufferedFromAnthropic(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime)
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
