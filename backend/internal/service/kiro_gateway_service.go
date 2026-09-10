package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	accountRepo         AccountRepository
	// TK priced-serving gate deps (docs/approved/priced-or-it-doesnt-ship.md).
	// Injected post-construction via SetPricedServingGateDeps. nil = gate off.
	tkSettingService         *SettingService
	tkBillingService         *BillingService
	tkPricingCatalog         *PricingCatalogService
	tkPricingMissingNotifier PricingMissingNotifier
	tkPricingResolver        *ModelPricingResolver
	// kiroCacheBillingSetting owns gateway.kiro_cache_billing.enabled.
	// Injected by ProvideTKKiroCacheBilling (not priced-serving) so the kill
	// switch survives if priced-serving DI is absent. nil fail-opens to on.
	kiroCacheBillingSetting *SettingService
	// kiroCacheStore holds prompt-prefix fingerprints for optional cache_read
	// billing (gateway.kiro_cache_billing.enabled). Defaults to in-process memory.
	kiroCacheStore kiroproto.CacheFingerprintStore
}

// KiroPostOutputStreamDisconnectError marks an incomplete upstream stream after
// response content has already been sent. The current request cannot be replayed
// safely; the handler uses this marker to exclude the account once on the
// session's next request.
type KiroPostOutputStreamDisconnectError struct {
	Err error
}

func (e *KiroPostOutputStreamDisconnectError) Error() string {
	if e == nil || e.Err == nil {
		return "Kiro stream disconnected after output"
	}
	return "Kiro stream disconnected after output: " + e.Err.Error()
}

func (e *KiroPostOutputStreamDisconnectError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsKiroPostOutputStreamDisconnect reports whether replaying the current
// response would risk duplicating text or tool calls.
func IsKiroPostOutputStreamDisconnect(err error) bool {
	var target *KiroPostOutputStreamDisconnectError
	return errors.As(err, &target)
}

func classifyKiroPostOutputStreamError(kind string, err error) error {
	wrapped := fmt.Errorf("kiro stream %s error: %w", kind, err)
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return &KiroPostOutputStreamDisconnectError{Err: wrapped}
	}
	return wrapped
}

// mapKiroStopReason preserves Kiro's authoritative terminal outcome on the
// Anthropic Messages wire. Unknown values fail closed instead of being forged
// into end_turn, which would make Claude Code report an incomplete task as
// successfully completed.
func mapKiroStopReason(raw string, hasToolUse bool) (string, error) {
	normalized := normalizeKiroStopReason(raw)
	switch normalized {
	case "END_TURN":
		if hasToolUse {
			return "tool_use", nil
		}
		return "end_turn", nil
	case "TOOL_USE":
		return "tool_use", nil
	case "MAX_TOKENS":
		return "max_tokens", nil
	case "STOP_SEQUENCE":
		// Kiro exposes no matched sequence in metadataEvent.stopDetails. An
		// Anthropic stop_sequence response without that value is malformed, so
		// fail closed instead of emitting stop_sequence:null.
		return "", fmt.Errorf("%w: STOP_SEQUENCE without matched sequence", errKiroUnsupportedStopReason)
	case "MODEL_CONTEXT_WINDOW_EXCEEDED":
		return "model_context_window_exceeded", nil
	case "CONTENT_FILTERED", "GUARDRAIL_INTERVENED":
		// Empty filtered responses are rejected before this mapper. Visible
		// refusal text uses Anthropic's refusal terminal outcome rather than
		// masquerading as a successful end_turn.
		return "refusal", nil
	case "MALFORMED_MODEL_OUTPUT", "MALFORMED_TOOL_USE":
		return "", fmt.Errorf("%w: %s", errKiroUnsupportedStopReason, normalized)
	default:
		return "", fmt.Errorf("%w: %q", errKiroUnsupportedStopReason, truncateString(raw, 64))
	}
}

func normalizeKiroStopReason(raw string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(raw), "-", "_"))
}

func isKiroPolicyStopReason(raw string) bool {
	switch normalizeKiroStopReason(raw) {
	case "CONTENT_FILTERED", "GUARDRAIL_INTERVENED":
		return true
	default:
		return false
	}
}

func logKiroStopReason(account *Account, model, raw, mapped string, stream bool) {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	slog.Info("gateway.kiro_stop_reason",
		slog.Int64("account_id", accountID),
		slog.String("model", model),
		slog.String("raw_stop_reason", truncateString(raw, 64)),
		slog.String("anthropic_stop_reason", mapped),
		slog.Bool("stream", stream),
	)
}

// NewKiroGatewayService constructs a KiroGatewayService.
func NewKiroGatewayService(
	httpUpstream HTTPUpstream,
	tlsFPProfileService *TLSFingerprintProfileService,
	accountRepo AccountRepository,
) *KiroGatewayService {
	return &KiroGatewayService{
		httpUpstream:        httpUpstream,
		tlsFPProfileService: tlsFPProfileService,
		accountRepo:         accountRepo,
		kiroCacheStore:      kiroproto.NewMemoryCacheFingerprintStore(),
	}
}

// SetKiroCacheFingerprintStore replaces the prompt-prefix fingerprint store
// (typically Redis in production). nil keeps the in-process default.
func (s *KiroGatewayService) SetKiroCacheFingerprintStore(store kiroproto.CacheFingerprintStore) {
	if s == nil || store == nil {
		return
	}
	s.kiroCacheStore = store
}

// SetKiroCacheBillingSetting injects the settings reader used by the
// gateway.kiro_cache_billing.enabled kill-switch.
func (s *KiroGatewayService) SetKiroCacheBillingSetting(setting *SettingService) {
	if s == nil {
		return
	}
	s.kiroCacheBillingSetting = setting
}

// HasKiroCacheBillingDeps reports whether production cache-billing DI attached
// both a fingerprint store and a settings reader.
func (s *KiroGatewayService) HasKiroCacheBillingDeps() bool {
	return s != nil && s.kiroCacheStore != nil && s.kiroCacheBillingSetting != nil
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
	if !s.tkPricedServingGate(ctx, c, tkGateWireAnthropic, account.Platform, model, model) {
		return nil, fmt.Errorf("priced serving gate: model %q not priced for platform %q", model, account.Platform)
	}

	var rawBody []byte
	if parsed.Body != nil {
		rawBody = parsed.Body.Bytes()
	}

	if req.Stream {
		result, err := s.forwardStreaming(ctx, c, account, doer, kiroAcct, payload, &req, rawBody, requestID, model, startTime)
		if err == nil {
			PersistKiroProfileArnIfChanged(ctx, s.accountRepo, account, kiroAcct)
		}
		return result, err
	}
	result, err := s.forwardNonStreaming(ctx, c, account, doer, kiroAcct, payload, &req, rawBody, requestID, model, startTime)
	if err == nil {
		PersistKiroProfileArnIfChanged(ctx, s.accountRepo, account, kiroAcct)
	}
	return result, err
}

// forwardNonStreaming accumulates text/thinking/tool-use then writes a single
// Anthropic Messages JSON response.
func (s *KiroGatewayService) forwardNonStreaming(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	doer kiroproto.HTTPDoer,
	kiroAcct *kiroproto.Account,
	payload *kiroproto.KiroPayload,
	req *kiroproto.ClaudeRequest,
	rawBody []byte,
	requestID, model string,
	startTime time.Time,
) (*ForwardResult, error) {
	inputTokens, cacheReadTokens, cacheCreationTokens, cacheSessionKey, cacheBillingEnabled := s.kiroPromptUsage(ctx, account, req, payload)

	var (
		textBuf        string
		thinkingBuf    string
		thinkingSigBuf string
		rawAssistant   string
		toolUses       []kiroproto.KiroToolUse
		callbackErr    error
		stopReason     string
		redactor       kiroproto.InlineThinkingRedactor
	)

	callback := &kiroproto.KiroStreamCallback{
		OnReasoningContent: func(text, signature string) {
			if text != "" {
				thinkingBuf += text
			}
			if signature != "" && thinkingSigBuf == "" {
				thinkingSigBuf = signature
			}
		},
		OnText: func(text string, isThinking bool) {
			if isThinking {
				thinkingBuf += text
				return
			}
			rawAssistant += text
			visible, inlineThinking := redactor.Push(text)
			textBuf += visible
			thinkingBuf += inlineThinking
		},
		OnToolUse: func(toolUse kiroproto.KiroToolUse) {
			toolUses = append(toolUses, toolUse)
		},
		OnStopReason: func(reason string) {
			stopReason = reason
		},
		// Kiro upstream reports no token usage; OnComplete(in,out) is always (0,0).
		// We estimate token usage locally below instead of trusting these values.
		OnCredits: func(credits float64) {
			logKiroCredits(kiroAcct, model, credits)
		},
		OnError: func(err error) {
			callbackErr = err
		},
		ResetForRetry: func() bool {
			textBuf = ""
			thinkingBuf = ""
			thinkingSigBuf = ""
			rawAssistant = ""
			toolUses = nil
			callbackErr = nil
			stopReason = ""
			redactor = kiroproto.InlineThinkingRedactor{}
			return true
		},
	}

	if err := kiroproto.CallKiroAPIWithDoerContext(ctx, doer, kiroAcct, payload, callback); err != nil {
		return nil, classifyAndRecordKiroForwardError(c, account, err, model)
	}
	if callbackErr != nil {
		return nil, classifyAndRecordKiroForwardError(c, account, callbackErr, model)
	}
	if visible, inlineThinking := redactor.Flush(); visible != "" || inlineThinking != "" {
		textBuf += visible
		thinkingBuf += inlineThinking
	}

	if textBuf == "" && thinkingBuf == "" && len(toolUses) == 0 {
		if isKiroPolicyStopReason(stopReason) {
			return nil, classifyAndRecordKiroForwardError(c, account, &KiroContentFilteredError{}, model)
		}
		return nil, classifyAndRecordKiroForwardError(c, account, errKiroEmptyResponse, model)
	}
	mappedStopReason, err := mapKiroStopReason(stopReason, len(toolUses) > 0)
	if err != nil {
		return nil, classifyAndRecordKiroForwardError(c, account, err, model)
	}
	logKiroStopReason(account, model, stopReason, mappedStopReason, false)
	outputToks := kiroproto.EstimateOutputTokens(textBuf, thinkingBuf, toolUses)

	resp := kiroproto.KiroToClaudeResponse(
		textBuf, thinkingBuf, false, toolUses, inputTokens, outputToks, model, mappedStopReason,
	)
	resp.ID = requestID
	resp.Usage.CacheReadInputTokens = cacheReadTokens
	resp.Usage.CacheCreationInputTokens = cacheCreationTokens

	if c != nil {
		c.Header("x-request-id", requestID)
		stashThinking := kiroproto.ResolveStashThinking(rawAssistant, thinkingBuf, thinkingSigBuf)
		publishKiroInternalThinkingSideChannel(c, nil, c.Writer.Header(), stashThinking, thinkingSigBuf)
		c.JSON(http.StatusOK, resp)
	}

	s.commitKiroCacheFingerprints(ctx, cacheSessionKey, req, rawBody, cacheBillingEnabled)

	return &ForwardResult{
		RequestID:     requestID,
		Usage:         kiroClaudeUsage(inputTokens, outputToks, cacheReadTokens, cacheCreationTokens),
		Model:         model,
		UpstreamModel: kiroproto.MapModel(model),
		Stream:        false,
		Duration:      time.Since(startTime),
		BillingTier:   kiroBillingTier(cacheBillingEnabled),
	}, nil
}

// forwardStreaming translates Kiro callbacks into the canonical Anthropic SSE
// event sequence written to c. Block-index transitions are managed so that a
// thinking block, a text block, and any number of tool_use blocks each get
// their own content_block_start / _delta(s) / _stop framing.
func (s *KiroGatewayService) forwardStreaming(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	doer kiroproto.HTTPDoer,
	kiroAcct *kiroproto.Account,
	payload *kiroproto.KiroPayload,
	req *kiroproto.ClaudeRequest,
	rawBody []byte,
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

	inputTokens, cacheReadTokens, cacheCreationTokens, cacheSessionKey, cacheBillingEnabled := s.kiroPromptUsage(ctx, account, req, payload)
	enc := &kiroSSEEncoder{
		w:       w,
		flusher: flusher,
		model:   model,
		msgID:   requestID,
		// Estimate input tokens up-front (pure function of the request) so the
		// first message_start emitted mid-stream carries the real prompt count
		// instead of 0 — the prod relay bills off the parsed SSE usage. See the
		// inputTokens field doc in kiro_sse_encoder.go.
		inputTokens:         inputTokens,
		cacheReadTokens:     cacheReadTokens,
		cacheCreationTokens: cacheCreationTokens,
	}

	var (
		mu         sync.Mutex
		firstTokMs *int
	)

	markFirstVisibleToken := func() {
		if firstTokMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokMs = &ms
		}
	}
	// Stop header-wait pings before the first client-visible write so the
	// keepalive goroutine cannot race content frames on c.Writer.
	writeVisibleText := func(delta string) {
		if delta == "" {
			return
		}
		stopPreContentStreamKeepalive(c)
		markFirstVisibleToken()
		enc.writeTextDelta(delta)
	}
	writeVisibleToolUse := func(toolUse kiroproto.KiroToolUse) {
		stopPreContentStreamKeepalive(c)
		markFirstVisibleToken()
		enc.writeToolUse(toolUse)
	}

	// A completed upstream turn returns control to the client agent. Transport
	// retries may replay uncommitted output, but must not invent a new user turn.
	var (
		textBuf             string
		thinkingBuf         string
		thinkingSigBuf      string
		rawAssistant        string
		toolUses            []kiroproto.KiroToolUse
		callbackErr         error
		stopReason          string
		redactor            kiroproto.InlineThinkingRedactor
		callOutputCommitted bool
	)

	callback := &kiroproto.KiroStreamCallback{
		OnReasoningContent: func(text, signature string) {
			mu.Lock()
			defer mu.Unlock()
			if text != "" {
				thinkingBuf += text
			}
			if signature != "" && thinkingSigBuf == "" {
				thinkingSigBuf = signature
			}
		},
		OnText: func(text string, isThinking bool) {
			mu.Lock()
			defer mu.Unlock()
			if isThinking {
				// Thinking stays off the client wire (unsigned Kiro
				// reasoning). Keepalive continues until visible text/tool
				// arrives; first_token_ms only arms on client-visible bytes.
				thinkingBuf += text
				return
			}
			rawAssistant += text
			visible, inlineThinking := redactor.Push(text)
			thinkingBuf += inlineThinking
			if visible != "" {
				textBuf += visible
				writeVisibleText(visible)
				callOutputCommitted = true
			}
		},
		OnToolUse: func(toolUse kiroproto.KiroToolUse) {
			mu.Lock()
			defer mu.Unlock()
			toolUses = append(toolUses, toolUse)
			writeVisibleToolUse(toolUse)
			callOutputCommitted = true
		},
		OnStopReason: func(reason string) {
			mu.Lock()
			defer mu.Unlock()
			stopReason = reason
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
		ResetForRetry: func() bool {
			mu.Lock()
			defer mu.Unlock()
			if callOutputCommitted {
				return false
			}
			textBuf = ""
			thinkingBuf = ""
			thinkingSigBuf = ""
			rawAssistant = ""
			toolUses = nil
			callbackErr = nil
			stopReason = ""
			redactor = kiroproto.InlineThinkingRedactor{}
			firstTokMs = nil
			return true
		},
	}

	callErr := kiroproto.CallKiroAPIWithDoerContext(ctx, doer, kiroAcct, payload, callback)

	mu.Lock()
	// If the upstream failed before producing any client-visible content,
	// surface the error for account failover. Once response content was
	// committed, SSE has no replay point and must end with an error event.
	if callErr != nil && !enc.started {
		mu.Unlock()
		return nil, classifyAndRecordKiroForwardError(c, account, callErr, model)
	}
	if callErr != nil {
		msg := "upstream stream disconnected: " + sanitizeStreamError(callErr)
		recordKiroStreamError(c, account, msg)
		stopPreContentStreamKeepalive(c)
		writeKiroStreamError(c, flusher, "stream_read_error", msg)
		mu.Unlock()
		return nil, classifyKiroPostOutputStreamError("read", callErr)
	}
	if callbackErr != nil && !enc.started {
		mu.Unlock()
		return nil, classifyAndRecordKiroForwardError(c, account, callbackErr, model)
	}
	if callbackErr != nil {
		msg := "upstream stream disconnected: " + sanitizeStreamError(callbackErr)
		recordKiroStreamError(c, account, msg)
		stopPreContentStreamKeepalive(c)
		writeKiroStreamError(c, flusher, "stream_read_error", msg)
		mu.Unlock()
		return nil, classifyKiroPostOutputStreamError("callback", callbackErr)
	}
	if visible, inlineThinking := redactor.Flush(); visible != "" || inlineThinking != "" {
		thinkingBuf += inlineThinking
		if visible != "" {
			textBuf += visible
			writeVisibleText(visible)
			callOutputCommitted = true
		}
	}

	if textBuf == "" && thinkingBuf == "" && len(toolUses) == 0 {
		mu.Unlock()
		if isKiroPolicyStopReason(stopReason) {
			return nil, classifyAndRecordKiroForwardError(c, account, &KiroContentFilteredError{}, model)
		}
		return nil, classifyAndRecordKiroForwardError(c, account, errKiroEmptyResponse, model)
	}
	mappedStopReason, err := mapKiroStopReason(stopReason, len(toolUses) > 0)
	if err != nil {
		msg := sanitizeStreamError(err)
		recordKiroStreamError(c, account, msg)
		stopPreContentStreamKeepalive(c)
		writeKiroStreamError(c, flusher, "unsupported_stop_reason", msg)
		mu.Unlock()
		return nil, fmt.Errorf("kiro stream stop reason error: %w", err)
	}
	logKiroStopReason(account, model, stopReason, mappedStopReason, true)
	mu.Unlock()
	outputToks := kiroproto.EstimateOutputTokens(textBuf, thinkingBuf, toolUses)

	// Upstream succeeded but produced no content (enc.started still false):
	// emit message_start lazily here so the closing events form a valid stream.
	stopPreContentStreamKeepalive(c)
	enc.writeMessageStart()
	enc.closeOpenBlock()
	// Relay consumers merge terminal usage into the billing accumulator.
	enc.writeMessageDelta(inputTokens, outputToks, cacheReadTokens, cacheCreationTokens, mappedStopReason)
	enc.writeMessageStop()
	stashThinking := kiroproto.ResolveStashThinking(rawAssistant, thinkingBuf, thinkingSigBuf)
	publishKiroInternalThinkingSideChannel(c, w, nil, stashThinking, thinkingSigBuf)
	flusher.Flush()

	s.commitKiroCacheFingerprints(ctx, cacheSessionKey, req, rawBody, cacheBillingEnabled)

	return &ForwardResult{
		RequestID:     requestID,
		Usage:         kiroClaudeUsage(inputTokens, outputToks, cacheReadTokens, cacheCreationTokens),
		Model:         model,
		UpstreamModel: kiroproto.MapModel(model),
		Stream:        true,
		Duration:      time.Since(startTime),
		FirstTokenMs:  firstTokMs,
		BillingTier:   kiroBillingTier(cacheBillingEnabled),
	}, nil
}

func recordKiroStreamError(c *gin.Context, account *Account, message string) {
	setOpsUpstreamError(c, 0, message, "")
	MarkOpsStreamError(c, "upstream_error", message, http.StatusBadGateway)
	event := OpsUpstreamErrorEvent{
		Platform:           PlatformKiro,
		UpstreamStatusCode: 0,
		Kind:               "stream_error",
		Message:            message,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func writeKiroStreamError(c *gin.Context, flusher http.Flusher, errType, message string) {
	if c == nil || c.Writer == nil {
		return
	}
	if errType == "" {
		errType = "stream_read_error"
	}
	if message == "" {
		message = errType
	}
	body, err := json.Marshal(map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
	if err != nil {
		body = []byte(fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%q}}`, errType, message))
	}
	_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", body)
	if flusher != nil {
		flusher.Flush()
	}
	MarkResponseCommitted(c)
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
