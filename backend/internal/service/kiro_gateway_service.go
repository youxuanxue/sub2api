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
	"unicode"
	"unicode/utf8"

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
}

// maxClaudeCodeCompletionTurns bounds the number of Kiro model calls inside a
// single Claude Code HTTP request. The private completion protocol normally
// terminates on the first call; the remaining calls recover text-only model
// stops without creating an unbounded agent loop.
const maxClaudeCodeCompletionTurns = 3

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

func acceptsClaudeCodeCompletionSignal(raw string) bool {
	switch normalizeKiroStopReason(raw) {
	case "END_TURN", "TOOL_USE":
		return true
	default:
		return false
	}
}

func isKiroPolicyStopReason(raw string) bool {
	switch normalizeKiroStopReason(raw) {
	case "CONTENT_FILTERED", "GUARDRAIL_INTERVENED":
		return true
	default:
		return false
	}
}

func isAcceptedClaudeCodeCompletion(
	rawStopReason string,
	clientToolUses []kiroproto.KiroToolUse,
	signal *kiroproto.ClaudeCodeCompletionSignal,
) bool {
	return signal != nil &&
		len(clientToolUses) == 0 &&
		acceptsClaudeCodeCompletionSignal(rawStopReason) &&
		signal.Message != ""
}

func shouldContinueClaudeCodeCompletion(
	payload *kiroproto.KiroPayload,
	rawStopReason string,
	clientToolUses []kiroproto.KiroToolUse,
	completionAccepted bool,
) bool {
	return payload != nil &&
		payload.ClaudeCodeCompletionProtocol &&
		len(clientToolUses) == 0 &&
		!completionAccepted &&
		acceptsClaudeCodeCompletionSignal(rawStopReason)
}

func completionSignalTextDelta(visibleText string, signal *kiroproto.ClaudeCodeCompletionSignal) string {
	if signal == nil || strings.TrimSpace(signal.Message) == "" {
		return ""
	}
	message := strings.TrimSpace(signal.Message)
	visibleText = strings.TrimSpace(visibleText)
	if visibleText == "" {
		return message
	}
	if containsVisibleCompletionBlock(visibleText, message) {
		return ""
	}
	return "\n\n" + message
}

// continuationTextDelta removes ordinary text that a hidden completion
// continuation repeated verbatim from an earlier turn. Claude Code receives
// the first turn while the gateway keeps subsequent turns internal; allowing
// the model to restate that turn would make one assistant response appear two
// or three times in the client transcript.
func continuationTextDelta(visibleText, continuationText string) string {
	if strings.TrimSpace(continuationText) == "" {
		return ""
	}
	if strings.TrimSpace(visibleText) == "" {
		return continuationText
	}

	trimmed := strings.TrimSpace(continuationText)
	if containsVisibleCompletionBlock(visibleText, trimmed) {
		return ""
	}

	// Models sometimes add a short recap/header around the repeated answer.
	// Drop repeated paragraphs while retaining any genuinely new text.
	paragraphs := strings.Split(continuationText, "\n\n")
	kept := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		if strings.TrimSpace(paragraph) == "" {
			continue
		}
		block := strings.TrimSpace(paragraph)
		if len([]rune(block)) >= 4 && containsVisibleCompletionBlock(visibleText, block) {
			continue
		}
		kept = append(kept, paragraph)
	}
	if len(kept) == 0 {
		return ""
	}
	return separateVisibleTextDelta(visibleText, strings.Join(kept, "\n\n"))
}

// containsVisibleCompletionBlock also tolerates markdown/list decoration
// differences between repeated model turns (for example "• Done" versus
// "Done"), while the underlying boundary-aware matcher prevents substring
// false positives.
func containsVisibleCompletionBlock(text, block string) bool {
	if containsCompletionTextBlock(text, block) {
		return true
	}
	stripped, ok := stripCompletionMarkdownPrefix(block)
	return ok && containsCompletionTextBlock(text, stripped)
}

func stripCompletionMarkdownPrefix(block string) (string, bool) {
	trimmed := strings.TrimLeftFunc(block, isHorizontalSpace)
	if trimmed == "" {
		return "", false
	}

	if trimmed[0] == '#' {
		index := 0
		for index < len(trimmed) && trimmed[index] == '#' {
			index++
		}
		if index < len(trimmed) {
			next, _ := utf8.DecodeRuneInString(trimmed[index:])
			if isHorizontalSpace(next) {
				stripped := strings.TrimLeftFunc(trimmed[index:], isHorizontalSpace)
				return stripped, stripped != ""
			}
		}
	}

	marker, size := utf8.DecodeRuneInString(trimmed)
	if marker != '*' && marker != '-' && marker != '•' {
		return "", false
	}
	rest := trimmed[size:]
	next, _ := utf8.DecodeRuneInString(rest)
	if !isHorizontalSpace(next) {
		return "", false
	}
	stripped := strings.TrimLeftFunc(rest, isHorizontalSpace)
	return stripped, stripped != ""
}

func isHorizontalSpace(r rune) bool {
	return r == ' ' || r == '\t'
}

func separateVisibleTextDelta(visibleText, delta string) string {
	if delta == "" || strings.TrimSpace(visibleText) == "" {
		return delta
	}
	before, _ := utf8.DecodeLastRuneInString(visibleText)
	after, _ := utf8.DecodeRuneInString(delta)
	if unicode.IsSpace(before) || unicode.IsSpace(after) {
		return delta
	}
	return "\n\n" + delta
}

// containsCompletionTextBlock reports whether the private completion message
// is already present as a complete whitespace- or punctuation-delimited block
// in client-visible text. Boundary checks avoid treating short messages such as
// "OK" as present merely because they occur inside another word.
func containsCompletionTextBlock(text, block string) bool {
	for offset := 0; offset <= len(text)-len(block); {
		relative := strings.Index(text[offset:], block)
		if relative < 0 {
			return false
		}
		start := offset + relative
		end := start + len(block)
		beforeBoundary := start == 0 || isCompletionTextBoundaryBefore(text, start)
		afterBoundary := end == len(text) || isCompletionTextBoundaryAfter(text, end)
		if beforeBoundary && afterBoundary {
			return true
		}
		offset = start + 1
	}
	return false
}

func isCompletionTextBoundaryBefore(text string, index int) bool {
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	return unicode.IsSpace(r) || unicode.IsPunct(r)
}

func isCompletionTextBoundaryAfter(text string, index int) bool {
	r, _ := utf8.DecodeRuneInString(text[index:])
	return unicode.IsSpace(r) || unicode.IsPunct(r)
}

func logKiroCompletionProtocol(account *Account, model string, turn int, action, status string) {
	accountID := int64(0)
	if account != nil {
		accountID = account.ID
	}
	slog.Info("gateway.kiro_completion_protocol",
		slog.Int64("account_id", accountID),
		slog.String("model", model),
		slog.Int("turn", turn),
		slog.String("action", action),
		slog.String("status", status),
	)
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
		result, err := s.forwardStreaming(ctx, c, account, doer, kiroAcct, payload, &req, requestID, model, startTime)
		if err == nil {
			PersistKiroProfileArnIfChanged(ctx, s.accountRepo, account, kiroAcct)
		}
		return result, err
	}
	result, err := s.forwardNonStreaming(ctx, c, account, doer, kiroAcct, payload, &req, requestID, model, startTime)
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
	requestID, model string,
	startTime time.Time,
) (*ForwardResult, error) {
	var (
		textBuf          string // client-visible text across all turns
		billingTextBuf   string // all model text, including hidden continuations
		thinkingBuf      string
		clientToolUses   []kiroproto.KiroToolUse
		billingToolUses  []kiroproto.KiroToolUse
		mappedStopReason string
	)
	inputTokens := kiroproto.EstimateInputTokens(req)

	for turn := 1; turn <= maxClaudeCodeCompletionTurns; turn++ {
		var (
			turnText     string
			turnThinking string
			turnToolUses []kiroproto.KiroToolUse
			callbackErr  error
			stopReason   string
			redactor     kiroproto.InlineThinkingRedactor
		)

		callback := &kiroproto.KiroStreamCallback{
			OnText: func(text string, isThinking bool) {
				if isThinking {
					turnThinking += text
					return
				}
				visible, inlineThinking := redactor.Push(text)
				turnText += visible
				turnThinking += inlineThinking
			},
			OnToolUse: func(toolUse kiroproto.KiroToolUse) {
				turnToolUses = append(turnToolUses, toolUse)
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
				turnText = ""
				turnThinking = ""
				turnToolUses = nil
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
			turnText += visible
			turnThinking += inlineThinking
		}

		visibleToolUses := turnToolUses
		var completionSignal *kiroproto.ClaudeCodeCompletionSignal
		if payload.ClaudeCodeCompletionProtocol {
			visibleToolUses, completionSignal = kiroproto.ConsumeClaudeCodeCompletionSignal(turnToolUses)
		}
		completionAccepted := isAcceptedClaudeCodeCompletion(stopReason, visibleToolUses, completionSignal)
		visibleTurnText := turnText
		if turn > 1 {
			visibleTurnText = continuationTextDelta(textBuf, turnText)
		}
		if completionAccepted {
			visibleTurnText += completionSignalTextDelta(textBuf+visibleTurnText, completionSignal)
		}
		if visibleTurnText == "" && turnThinking == "" && len(turnToolUses) == 0 && textBuf == "" && thinkingBuf == "" {
			if isKiroPolicyStopReason(stopReason) {
				return nil, classifyAndRecordKiroForwardError(c, account, &KiroContentFilteredError{}, model)
			}
			return nil, classifyAndRecordKiroForwardError(c, account, errKiroEmptyResponse, model)
		}

		textBuf += visibleTurnText
		billingTextBuf += turnText
		thinkingBuf += turnThinking
		billingToolUses = append(billingToolUses, turnToolUses...)

		if shouldContinueClaudeCodeCompletion(payload, stopReason, visibleToolUses, completionAccepted) {
			if turn < maxClaudeCodeCompletionTurns {
				logKiroCompletionProtocol(account, model, turn, "continue", "missing_signal")
				kiroproto.PrepareClaudeCodeCompletionContinuation(payload, turnText)
				inputTokens += kiroproto.EstimatePayloadInputTokens(payload)
				continue
			}
			logKiroCompletionProtocol(account, model, turn, "exhausted", "missing_signal")
			err := fmt.Errorf("%w after %d turns", errKiroCompletionExhausted, maxClaudeCodeCompletionTurns)
			return nil, classifyAndRecordKiroForwardError(c, account, err, model)
		}

		clientToolUses = visibleToolUses
		if completionAccepted {
			mappedStopReason = "end_turn"
			logKiroCompletionProtocol(account, model, turn, "finish", completionSignal.Status)
		} else {
			var err error
			mappedStopReason, err = mapKiroStopReason(stopReason, len(clientToolUses) > 0)
			if err != nil {
				return nil, classifyAndRecordKiroForwardError(c, account, err, model)
			}
		}
		logKiroStopReason(account, model, stopReason, mappedStopReason, false)
		break
	}

	// Estimate output across every hidden completion turn, including the private
	// completion tool call that is intentionally absent from the client response.
	outputToks := kiroproto.EstimateOutputTokens(billingTextBuf, thinkingBuf, billingToolUses)

	resp := kiroproto.KiroToClaudeResponse(
		textBuf, thinkingBuf, false, clientToolUses, inputTokens, outputToks, model, mappedStopReason,
	)
	resp.ID = requestID

	if c != nil {
		c.Header("x-request-id", requestID)
		publishKiroInternalThinkingSideChannel(c, nil, c.Writer.Header(), thinkingBuf)
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
	account *Account,
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
		mu               sync.Mutex
		textBuf          string // client-visible text across all turns
		billingTextBuf   string // all model text, including hidden continuations
		thinkingBuf      string
		clientToolUses   []kiroproto.KiroToolUse
		billingToolUses  []kiroproto.KiroToolUse
		mappedStopReason string
		firstTokMs       *int
	)

	markFirstToken := func() {
		if firstTokMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokMs = &ms
		}
	}

	// message_start is emitted lazily on first client-visible content (see
	// kiroSSEEncoder ensureBlock / writeToolUse). The transport-private
	// completion tool is never committed to the Anthropic stream.
	inputTokens := enc.inputTokens
	for turn := 1; turn <= maxClaudeCodeCompletionTurns; turn++ {
		firstTokBeforeTurn := firstTokMs
		var (
			turnText            string
			turnThinking        string
			turnToolUses        []kiroproto.KiroToolUse
			callbackErr         error
			stopReason          string
			redactor            kiroproto.InlineThinkingRedactor
			callOutputCommitted bool
			bufferedTextOffset  int
			visibleTurnText     string
		)
		flushContinuationText := func() {
			if turn == 1 || bufferedTextOffset >= len(turnText) {
				return
			}
			pendingText := turnText[bufferedTextOffset:]
			bufferedTextOffset = len(turnText)
			delta := continuationTextDelta(textBuf+visibleTurnText, pendingText)
			if delta == "" {
				return
			}
			visibleTurnText += delta
			markFirstToken()
			enc.writeTextDelta(delta)
			callOutputCommitted = true
		}

		callback := &kiroproto.KiroStreamCallback{
			OnText: func(text string, isThinking bool) {
				mu.Lock()
				defer mu.Unlock()
				markFirstToken()
				if isThinking {
					turnThinking += text
					return
				}
				visible, inlineThinking := redactor.Push(text)
				turnThinking += inlineThinking
				if visible != "" {
					turnText += visible
					// Continuation turns are hidden until their terminal outcome is
					// known, so repeated model text can be removed before it reaches
					// the client. The first turn remains fully streamed.
					if turn == 1 {
						enc.writeTextDelta(visible)
						callOutputCommitted = true
					}
				}
			},
			OnToolUse: func(toolUse kiroproto.KiroToolUse) {
				mu.Lock()
				defer mu.Unlock()
				turnToolUses = append(turnToolUses, toolUse)
				if payload.ClaudeCodeCompletionProtocol && kiroproto.IsClaudeCodeCompletionToolUse(toolUse) {
					return
				}
				flushContinuationText()
				markFirstToken()
				enc.writeToolUse(toolUse)
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
				turnText = ""
				turnThinking = ""
				turnToolUses = nil
				callbackErr = nil
				stopReason = ""
				redactor = kiroproto.InlineThinkingRedactor{}
				firstTokMs = firstTokBeforeTurn
				bufferedTextOffset = 0
				visibleTurnText = ""
				return true
			},
		}

		callErr := kiroproto.CallKiroAPIWithDoerContext(ctx, doer, kiroAcct, payload, callback)

		mu.Lock()
		// If the upstream failed before producing any client-visible content,
		// surface the error for account failover. Once any prior/current turn was
		// committed, SSE has no replay point and must end with an error event.
		if callErr != nil && !enc.started {
			mu.Unlock()
			return nil, classifyAndRecordKiroForwardError(c, account, callErr, model)
		}
		if callErr != nil {
			msg := "upstream stream disconnected: " + sanitizeStreamError(callErr)
			recordKiroStreamError(c, account, msg)
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
			writeKiroStreamError(c, flusher, "stream_read_error", msg)
			mu.Unlock()
			return nil, classifyKiroPostOutputStreamError("callback", callbackErr)
		}
		if visible, inlineThinking := redactor.Flush(); visible != "" || inlineThinking != "" {
			turnThinking += inlineThinking
			if visible != "" {
				turnText += visible
				if turn == 1 {
					enc.writeTextDelta(visible)
					callOutputCommitted = true
				}
			}
		}

		visibleToolUses := turnToolUses
		var completionSignal *kiroproto.ClaudeCodeCompletionSignal
		if payload.ClaudeCodeCompletionProtocol {
			visibleToolUses, completionSignal = kiroproto.ConsumeClaudeCodeCompletionSignal(turnToolUses)
		}
		completionAccepted := isAcceptedClaudeCodeCompletion(stopReason, visibleToolUses, completionSignal)
		if turn == 1 {
			visibleTurnText = turnText
		} else {
			flushContinuationText()
		}
		if completionAccepted {
			completionDelta := completionSignalTextDelta(textBuf+visibleTurnText, completionSignal)
			if completionDelta != "" {
				visibleTurnText += completionDelta
				markFirstToken()
				enc.writeTextDelta(completionDelta)
				callOutputCommitted = true
			}
		}
		if visibleTurnText == "" && turnThinking == "" && len(turnToolUses) == 0 && textBuf == "" && thinkingBuf == "" {
			if isKiroPolicyStopReason(stopReason) {
				mu.Unlock()
				return nil, classifyAndRecordKiroForwardError(c, account, &KiroContentFilteredError{}, model)
			}
			mu.Unlock()
			return nil, classifyAndRecordKiroForwardError(c, account, errKiroEmptyResponse, model)
		}

		textBuf += visibleTurnText
		billingTextBuf += turnText
		thinkingBuf += turnThinking
		billingToolUses = append(billingToolUses, turnToolUses...)

		if shouldContinueClaudeCodeCompletion(payload, stopReason, visibleToolUses, completionAccepted) {
			if turn < maxClaudeCodeCompletionTurns {
				logKiroCompletionProtocol(account, model, turn, "continue", "missing_signal")
				kiroproto.PrepareClaudeCodeCompletionContinuation(payload, turnText)
				inputTokens += kiroproto.EstimatePayloadInputTokens(payload)
				mu.Unlock()
				continue
			}
			logKiroCompletionProtocol(account, model, turn, "exhausted", "missing_signal")
			err := fmt.Errorf("%w after %d turns", errKiroCompletionExhausted, maxClaudeCodeCompletionTurns)
			msg := sanitizeStreamError(err)
			recordKiroStreamError(c, account, msg)
			writeKiroStreamError(c, flusher, "completion_exhausted", msg)
			mu.Unlock()
			return nil, fmt.Errorf("kiro stream completion error: %w", err)
		}

		clientToolUses = visibleToolUses
		if completionAccepted {
			mappedStopReason = "end_turn"
			logKiroCompletionProtocol(account, model, turn, "finish", completionSignal.Status)
		} else {
			var err error
			mappedStopReason, err = mapKiroStopReason(stopReason, len(clientToolUses) > 0)
			if err != nil {
				msg := sanitizeStreamError(err)
				recordKiroStreamError(c, account, msg)
				writeKiroStreamError(c, flusher, "unsupported_stop_reason", msg)
				mu.Unlock()
				return nil, fmt.Errorf("kiro stream stop reason error: %w", err)
			}
		}
		logKiroStopReason(account, model, stopReason, mappedStopReason, true)
		mu.Unlock()
		break
	}

	outputToks := kiroproto.EstimateOutputTokens(billingTextBuf, thinkingBuf, billingToolUses)

	// Upstream succeeded but produced no content (enc.started still false):
	// emit message_start lazily here so the closing events form a valid stream.
	enc.writeMessageStart()
	enc.closeOpenBlock()
	// Repeat the final input total because hidden completion continuations add
	// prompt tokens after message_start. Relay consumers merge this terminal
	// usage into the same accumulator used for billing.
	enc.writeMessageDelta(inputTokens, outputToks, mappedStopReason)
	enc.writeMessageStop()
	publishKiroInternalThinkingSideChannel(c, w, nil, thinkingBuf)
	flusher.Flush()

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
