package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
)

// KiroGatewayService forwards Anthropic /v1/messages requests onto the Kiro
// (sixth platform) CodeWhisperer EventStream upstream via the vendored
// internal/integration/kiro protocol layer.
//
// The vendored layer speaks EventStream and emits text / tool-use / completion
// callbacks; this service translates those callbacks into the canonical
// Anthropic SSE event sequence (message_start → content_block_* → message_delta
// → message_stop) for streaming requests, or accumulates them into a single
// Anthropic Messages JSON response for non-streaming requests, so the
// /v1/messages response shape is identical to the native Anthropic platform.
type KiroGatewayService struct {
	httpUpstream        HTTPUpstream
	tlsFPProfileService *TLSFingerprintProfileService
}

// NewKiroGatewayService constructs a KiroGatewayService.
func NewKiroGatewayService(
	httpUpstream HTTPUpstream,
	tlsFPProfileService *TLSFingerprintProfileService,
) *KiroGatewayService {
	return &KiroGatewayService{
		httpUpstream:        httpUpstream,
		tlsFPProfileService: tlsFPProfileService,
	}
}

// kiroDoer adapts httpUpstream.DoWithTLS to the kiroproto.HTTPDoer interface,
// pinning the per-account proxy/concurrency/TLS-profile context.
type kiroDoer struct {
	httpUpstream HTTPUpstream
	proxyURL     string
	accountID    int64
	concurrency  int
	tlsProfile   *tlsfingerprint.Profile
}

func (d *kiroDoer) Do(req *http.Request) (*http.Response, error) {
	return d.httpUpstream.DoWithTLS(req, d.proxyURL, d.accountID, d.concurrency, d.tlsProfile)
}

// Forward translates and forwards a parsed Anthropic request to the Kiro
// upstream. It mirrors forwardBedrock's ForwardResult contract so that usage
// recording and quota deduction in the handler remain platform-agnostic.
func (s *KiroGatewayService) Forward(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	if parsed == nil {
		return nil, fmt.Errorf("kiro forward: empty request")
	}

	kiroAcct := account.toKiroProtoAccount()

	var req kiroproto.ClaudeRequest
	if err := json.Unmarshal(parsed.Body.Bytes(), &req); err != nil {
		return nil, fmt.Errorf("kiro forward: parse request body: %w", err)
	}

	thinking := req.Thinking != nil &&
		(req.Thinking.Type == "enabled" || req.Thinking.Type == "adaptive")

	payload := kiroproto.ClaudeToKiro(&req, thinking)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	var tlsProfile *tlsfingerprint.Profile
	if s.tlsFPProfileService != nil {
		tlsProfile = s.tlsFPProfileService.ResolveTLSProfile(account)
	}
	doer := &kiroDoer{
		httpUpstream: s.httpUpstream,
		proxyURL:     proxyURL,
		accountID:    account.ID,
		concurrency:  account.Concurrency,
		tlsProfile:   tlsProfile,
	}

	requestID := "msg_" + uuid.New().String()
	model := req.Model
	if model == "" {
		model = parsed.Model
	}

	if req.Stream {
		return s.forwardStreaming(ctx, c, doer, kiroAcct, payload, &req, requestID, model, startTime)
	}
	return s.forwardNonStreaming(ctx, c, doer, kiroAcct, payload, &req, requestID, model, startTime)
}

// forwardNonStreaming accumulates text/thinking/tool-use then writes a single
// Anthropic Messages JSON response.
func (s *KiroGatewayService) forwardNonStreaming(
	ctx context.Context,
	c *gin.Context,
	doer kiroproto.HTTPDoer,
	kiroAcct *kiroproto.Account,
	payload *kiroproto.KiroPayload,
	req *kiroproto.ClaudeRequest,
	requestID, model string,
	startTime time.Time,
) (*ForwardResult, error) {
	var (
		textBuf     string
		thinkingBuf string
		toolUses    []kiroproto.KiroToolUse
		callbackErr error
		redactor    kiroproto.InlineThinkingRedactor
	)

	callback := &kiroproto.KiroStreamCallback{
		OnText: func(text string, isThinking bool) {
			if isThinking {
				thinkingBuf += text
			} else {
				visible, inlineThinking := redactor.Push(text)
				if inlineThinking != "" {
					thinkingBuf += inlineThinking
				}
				textBuf += visible
			}
		},
		OnToolUse: func(tu kiroproto.KiroToolUse) {
			toolUses = append(toolUses, tu)
		},
		// Kiro upstream reports no token usage; OnComplete(in,out) is always (0,0).
		// We estimate token usage locally below instead of trusting these values.
		OnCredits: func(credits float64) {
			logKiroCredits(kiroAcct, model, credits)
		},
		OnError: func(err error) {
			callbackErr = err
		},
	}

	if err := kiroproto.CallKiroAPIWithDoer(doer, kiroAcct, payload, callback); err != nil {
		return nil, classifyKiroForwardError(err, model)
	}
	if callbackErr != nil {
		return nil, fmt.Errorf("kiro stream error: %w", callbackErr)
	}
	if visible, inlineThinking := redactor.Flush(); visible != "" || inlineThinking != "" {
		textBuf += visible
		thinkingBuf += inlineThinking
	}

	// Estimate token usage (Kiro upstream returns credits only — see estimate.go).
	inputTokens := kiroproto.EstimateInputTokens(req)
	outputToks := kiroproto.EstimateOutputTokens(textBuf, thinkingBuf, toolUses)

	resp := kiroproto.KiroToClaudeResponse(
		textBuf, thinkingBuf, false, toolUses, inputTokens, outputToks, model,
	)
	resp.ID = requestID

	if c != nil {
		c.Header("x-request-id", requestID)
		c.JSON(http.StatusOK, resp)
	}

	return &ForwardResult{
		RequestID:     requestID,
		Usage:         ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputToks},
		Model:         model,
		UpstreamModel: kiroproto.MapModel(model),
		Stream:        false,
		Duration:      time.Since(startTime),
		BillingTier:   kiroproto.KiroEstimatedBillingTier,
	}, nil
}

// forwardStreaming translates Kiro callbacks into the canonical Anthropic SSE
// event sequence written to c. Block-index transitions are managed so that a
// thinking block, a text block, and any number of tool_use blocks each get
// their own content_block_start / _delta(s) / _stop framing.
func (s *KiroGatewayService) forwardStreaming(
	ctx context.Context,
	c *gin.Context,
	doer kiroproto.HTTPDoer,
	kiroAcct *kiroproto.Account,
	payload *kiroproto.KiroPayload,
	req *kiroproto.ClaudeRequest,
	requestID, model string,
	startTime time.Time,
) (*ForwardResult, error) {
	if c == nil {
		return nil, errors.New("kiro streaming: nil gin context")
	}
	w := c.Writer
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming not supported")
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Header("x-request-id", requestID)

	enc := &kiroSSEEncoder{
		w:       w,
		flusher: flusher,
		model:   model,
		msgID:   requestID,
		// Estimate input tokens up-front (pure function of the request) so the
		// first message_start emitted mid-stream carries the real prompt count
		// instead of 0 — the prod relay bills off the parsed SSE usage. See the
		// inputTokens field doc in kiro_sse_encoder.go.
		inputTokens: kiroproto.EstimateInputTokens(req),
	}

	var (
		mu          sync.Mutex
		textBuf     string
		thinkingBuf string
		toolUses    []kiroproto.KiroToolUse
		callbackErr error
		firstTokMs  *int
		redactor    kiroproto.InlineThinkingRedactor
	)

	markFirstToken := func() {
		if firstTokMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokMs = &ms
		}
	}

	// message_start is emitted lazily on first content (see kiroSSEEncoder
	// ensureBlock / writeToolUse). Emitting it eagerly here would set
	// enc.started=true before the upstream call, defeating the post-call
	// `!enc.started` guard and turning an upstream 400 (e.g. INVALID_MODEL_ID)
	// into a clean empty 200 stream that the handler never sees as an error.

	callback := &kiroproto.KiroStreamCallback{
		OnText: func(text string, isThinking bool) {
			mu.Lock()
			defer mu.Unlock()
			markFirstToken()
			if isThinking {
				thinkingBuf += text
				enc.writeThinkingDelta(text)
			} else {
				visible, inlineThinking := redactor.Push(text)
				if inlineThinking != "" {
					thinkingBuf += inlineThinking
					enc.writeThinkingDelta(inlineThinking)
				}
				if visible != "" {
					textBuf += visible
					enc.writeTextDelta(visible)
				}
			}
		},
		OnToolUse: func(tu kiroproto.KiroToolUse) {
			mu.Lock()
			defer mu.Unlock()
			markFirstToken()
			toolUses = append(toolUses, tu)
			enc.writeToolUse(tu)
		},
		// Kiro upstream reports no token usage; OnComplete(in,out) is always (0,0).
		// We estimate token usage locally below instead of trusting these values.
		OnCredits: func(credits float64) {
			logKiroCredits(kiroAcct, model, credits)
		},
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			callbackErr = err
		},
	}

	callErr := kiroproto.CallKiroAPIWithDoer(doer, kiroAcct, payload, callback)

	mu.Lock()
	defer mu.Unlock()

	// If the upstream failed before producing any content, surface the error so
	// the handler can decide on failover instead of emitting a half-finished
	// SSE stream. (Once content has begun — enc.started — we close out the
	// stream cleanly because the client has already received a 200 + bytes.)
	// classifyKiroForwardError maps a recognized HTTP 400 INVALID_MODEL_ID into
	// a typed *KiroInvalidModelError so the handler can return a clean 400.
	if callErr != nil && !enc.started {
		return nil, classifyKiroForwardError(callErr, model)
	}

	// Estimate token usage (Kiro upstream returns credits only — see estimate.go).
	// inputTokens was already computed for the encoder (message_start.usage); reuse
	// it for the ForwardResult so the two never drift.
	if visible, inlineThinking := redactor.Flush(); visible != "" || inlineThinking != "" {
		if inlineThinking != "" {
			thinkingBuf += inlineThinking
			enc.writeThinkingDelta(inlineThinking)
		}
		if visible != "" {
			textBuf += visible
			enc.writeTextDelta(visible)
		}
	}
	inputTokens := enc.inputTokens
	outputToks := kiroproto.EstimateOutputTokens(textBuf, thinkingBuf, toolUses)

	// Upstream succeeded but produced no content (enc.started still false):
	// emit message_start lazily here so the closing events form a valid stream.
	enc.writeMessageStart()
	enc.closeOpenBlock()
	enc.writeMessageDelta(outputToks)
	enc.writeMessageStop()
	flusher.Flush()

	if callbackErr != nil {
		// Content already streamed; log-level handling happens in the handler via
		// the returned ForwardResult/usage. We still return nil error to avoid a
		// double-write to the client after SSE has begun.
		_ = callbackErr
	}

	return &ForwardResult{
		RequestID:     requestID,
		Usage:         ClaudeUsage{InputTokens: inputTokens, OutputTokens: outputToks},
		Model:         model,
		UpstreamModel: kiroproto.MapModel(model),
		Stream:        true,
		Duration:      time.Since(startTime),
		FirstTokenMs:  firstTokMs,
		BillingTier:   kiroproto.KiroEstimatedBillingTier,
	}, nil
}

// logKiroCredits records the Kiro upstream credits cost at info level for
// observability. Credits are NOT used for billing (we estimate tokens instead);
// this is a passive side channel to reconcile estimated cost against upstream.
func logKiroCredits(account *kiroproto.Account, model string, credits float64) {
	var accountID string
	if account != nil {
		accountID = account.ID
	}
	slog.Info("kiro upstream credits",
		"account_id", accountID,
		"model", model,
		"credits", credits,
	)
}
