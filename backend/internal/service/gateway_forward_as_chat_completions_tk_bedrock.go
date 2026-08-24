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

// forwardAsChatCompletionsViaBedrock routes CC→Anthropic converted bodies through
// the Bedrock upstream (SigV4 or Bedrock API Key) instead of the Anthropic HTTP API,
// then converts the captured Anthropic response back to Chat Completions.
func (s *GatewayService) forwardAsChatCompletionsViaBedrock(
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
	upstreamStream := clientStream
	var err error
	if upstreamStream {
		anthropicBody, err = sjson.SetBytes(anthropicBody, "stream", true)
	} else {
		anthropicBody, err = sjson.SetBytes(anthropicBody, "stream", false)
	}
	if err != nil {
		return nil, fmt.Errorf("set anthropic stream for bedrock bridge: %w", err)
	}

	bedrockParsed := &ParsedRequest{
		Body:   NewRequestBodyRef(anthropicBody),
		Model:  originalModel,
		Stream: upstreamStream,
	}
	if bedrockParsed.Model == "" {
		bedrockParsed.Model = mappedModel
	}

	var captureBuf bytes.Buffer
	captureWriter := newBridgeCaptureWriter(&captureBuf)
	origWriter := c.Writer
	c.Writer = captureWriter

	fwdResult, err := s.forwardBedrock(ctx, c, account, bedrockParsed, startTime)
	c.Writer = origWriter
	if err != nil {
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

	reasoningEffort := ExtractChatCompletionsReasoningEffortFromBody(ccBody)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, ccBody, mappedModel)

	var result *ForwardResult
	if clientStream {
		result, err = s.handleCCStreamingFromAnthropic(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	} else if isAnthropicMessagesJSONResponse(upstreamResp) {
		result, err = s.handleCCBufferedFromAnthropicJSON(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime)
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
