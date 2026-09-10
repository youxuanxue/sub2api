package service

// TK companion: shared Anthropic SSE buffered-assembly loop scaffolding for
// ForwardAsChatCompletions / ForwardAsResponses. Terminal error / failover /
// partial-failure semantics stay owned by gateway_anthropic_buffered_error_tk.go;
// this file owns the duplicated event-accumulation + post-scan resolution body.

import (
	"context"
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// tkAnthropicBufferedAssembly accumulates one Anthropic Messages response from
// a forced-SSE upstream while capturing TK terminal failure state.
type tkAnthropicBufferedAssembly struct {
	FinalResp       *apicompat.AnthropicResponse
	Usage           ClaudeUsage
	StreamCompleted bool
	UpstreamErr     *tkAnthropicBufferedUpstreamError
}

func (a *tkAnthropicBufferedAssembly) noteSSEError(payload []byte, cfg *config.Config) bool {
	if a == nil {
		return false
	}
	if parsed, ok := tkParseAnthropicBufferedSSEError(payload, cfg); ok {
		a.UpstreamErr = parsed
		return true
	}
	return false
}

// applyEvent merges one AnthropicStreamEvent into the assembly. Returns true
// when the stream is complete and the caller should break the scan loop.
func (a *tkAnthropicBufferedAssembly) applyEvent(event *apicompat.AnthropicStreamEvent) bool {
	if a == nil || event == nil {
		return false
	}
	if event.Type == "message_start" && event.Message != nil {
		a.FinalResp = event.Message
		mergeAnthropicUsage(&a.Usage, event.Message.Usage)
	}
	if event.Type == "message_delta" {
		if event.Usage != nil {
			mergeAnthropicUsage(&a.Usage, *event.Usage)
		}
		if event.Delta != nil && event.Delta.StopReason != "" && a.FinalResp != nil {
			a.FinalResp.StopReason = apicompat.AnthropicStopReasonPtr(event.Delta.StopReason)
		}
	}
	if tkAnthropicBufferedEventCompletesMessage(event) {
		a.StreamCompleted = true
		return true
	}
	if event.Type == "content_block_start" && event.ContentBlock != nil && a.FinalResp != nil {
		a.FinalResp.Content = append(a.FinalResp.Content, *event.ContentBlock)
	}
	if event.Type == "content_block_delta" && event.Delta != nil && a.FinalResp != nil && event.Index != nil {
		idx := *event.Index
		if idx >= 0 && idx < len(a.FinalResp.Content) {
			switch event.Delta.Type {
			case "text_delta":
				a.FinalResp.Content[idx].Text += event.Delta.Text
			case "thinking_delta":
				a.FinalResp.Content[idx].Thinking += event.Delta.Thinking
			case "input_json_delta":
				a.FinalResp.Content[idx].Input = appendRawJSON(a.FinalResp.Content[idx].Input, event.Delta.PartialJSON)
			}
		}
	}
	return false
}

// tkResolveAnthropicBufferedAssembly applies the shared post-scan failure
// contract. On success it returns the assembled Anthropic response; on terminal
// empty failure it returns an UpstreamFailoverError without committing the
// client response.
func (s *GatewayService) tkResolveAnthropicBufferedAssembly(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	requestID string,
	mappedModel string,
	assembly *tkAnthropicBufferedAssembly,
	scannerErr error,
	readErrWarnMsg string,
) (*apicompat.AnthropicResponse, error) {
	if assembly == nil {
		assembly = &tkAnthropicBufferedAssembly{}
	}
	upstreamErr := assembly.UpstreamErr
	finalResp := assembly.FinalResp

	if scannerErr != nil {
		if !errors.Is(scannerErr, context.Canceled) && !errors.Is(scannerErr, context.DeadlineExceeded) {
			logger.L().Warn(readErrWarnMsg,
				zap.Error(scannerErr),
				zap.String("request_id", requestID),
			)
		}
		if upstreamErr == nil {
			upstreamErr = tkAnthropicBufferedSyntheticFailure("stream_read_error", "Upstream stream read failed before response completion")
		}
	}

	if !assembly.StreamCompleted && upstreamErr == nil {
		upstreamErr = tkAnthropicBufferedSyntheticFailure("stream_incomplete", "Upstream stream ended before response completion")
	}
	if upstreamErr != nil {
		if !tkAnthropicBufferedHasUsableContent(finalResp) {
			return nil, s.tkAnthropicBufferedFailoverError(c, account, resp, requestID, mappedModel, upstreamErr)
		}
		tkAnthropicBufferedPartialFailure(c, account, requestID, upstreamErr)
	}
	if finalResp == nil {
		return nil, s.tkAnthropicBufferedFailoverError(c, account, resp, requestID, mappedModel, nil)
	}
	return finalResp, nil
}
