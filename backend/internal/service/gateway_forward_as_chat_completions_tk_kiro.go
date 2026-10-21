package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
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

	kiroParsed := &ParsedRequest{
		Body:   NewRequestBodyRef(anthropicBody),
		Model:  mappedModel,
		Stream: upstreamStream,
	}
	if kiroParsed.Model == "" {
		kiroParsed.Model = originalModel
	}

	if clientStream {
		return s.forwardKiroChatStreamLive(ctx, c, account, kiroParsed, originalModel, mappedModel, ccBody, reasoningEffortFromCCBody(ccBody, mappedModel), includeUsage, startTime)
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

// forwardKiroChatStreamLive connects Kiro's Anthropic SSE writer to the
// existing Anthropic→Responses→Chat converter through an io.Pipe. This keeps
// the conversion state machine while allowing each upstream event to reach the
// Chat Completions client immediately instead of buffering the whole turn.
func (s *GatewayService) forwardKiroChatStreamLive(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	originalModel, mappedModel string,
	ccBody []byte,
	reasoningEffort *string,
	includeUsage bool,
	startTime time.Time,
) (*ForwardResult, error) {
	pipeReader, pipeWriter := io.Pipe()
	liveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = pipeReader.Close() }()
	bridge := newKiroLiveBridgeWriter(pipeWriter)
	upstreamContext := c.Copy()
	upstreamContext.Writer = bridge
	resultCh := make(chan struct {
		result *ForwardResult
		err    error
	}, 1)
	go func() {
		result, err := s.kiroGateway.Forward(liveCtx, upstreamContext, account, parsed, startTime)
		_ = bridge.closeWithError(err)
		resultCh <- struct {
			result *ForwardResult
			err    error
		}{result: result, err: err}
	}()

	upstreamResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     bridge.Header(),
		Body:       pipeReader,
	}
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, ccBody, mappedModel)
	converted, convertErr := s.handleCCStreamingFromAnthropic(upstreamResp, c, originalModel, mappedModel, reasoningEffort, startTime, includeUsage)
	fwd := <-resultCh
	if fwd.err != nil {
		return nil, fwd.err
	}
	if convertErr != nil {
		return nil, convertErr
	}
	if converted != nil && fwd.result != nil {
		if fwd.result.BillingTier != "" {
			converted.BillingTier = fwd.result.BillingTier
		}
		if fwd.result.RequestID != "" {
			converted.RequestID = fwd.result.RequestID
		}
		if fwd.result.Usage.InputTokens > 0 || fwd.result.Usage.OutputTokens > 0 {
			converted.Usage = fwd.result.Usage
		}
		if fwd.result.UpstreamModel != "" {
			converted.UpstreamModel = fwd.result.UpstreamModel
		}
	}
	return converted, nil
}

func reasoningEffortFromCCBody(body []byte, model string) *string {
	effort := ExtractChatCompletionsReasoningEffortFromBody(body)
	return ApplyThinkingEnabledFallback(effort, body, model)
}

type kiroLiveBridgeWriter struct {
	header http.Header
	pipe   *io.PipeWriter
	mu     sync.Mutex
	status int
	wrote  bool
}

func newKiroLiveBridgeWriter(pipe *io.PipeWriter) *kiroLiveBridgeWriter {
	return &kiroLiveBridgeWriter{header: make(http.Header), pipe: pipe, status: http.StatusOK}
}

func (w *kiroLiveBridgeWriter) Header() http.Header { return w.header }
func (w *kiroLiveBridgeWriter) WriteHeader(code int) {
	w.mu.Lock()
	w.status, w.wrote = code, true
	w.mu.Unlock()
}
func (w *kiroLiveBridgeWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.wrote = true
	w.mu.Unlock()
	return w.pipe.Write(data)
}
func (w *kiroLiveBridgeWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *kiroLiveBridgeWriter) Status() int                       { return w.status }
func (w *kiroLiveBridgeWriter) Size() int                         { return -1 }
func (w *kiroLiveBridgeWriter) Written() bool                     { return w.wrote }
func (w *kiroLiveBridgeWriter) WriteHeaderNow()                   {}
func (w *kiroLiveBridgeWriter) Flush()                            {}
func (w *kiroLiveBridgeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, fmt.Errorf("not supported")
}
func (w *kiroLiveBridgeWriter) CloseNotify() <-chan bool { return make(chan bool) } //nolint:staticcheck
func (w *kiroLiveBridgeWriter) Pusher() http.Pusher      { return nil }
func (w *kiroLiveBridgeWriter) closeWithError(err error) error {
	if err != nil {
		return w.pipe.CloseWithError(err)
	}
	return w.pipe.Close()
}
