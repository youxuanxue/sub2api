package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// Responses WebSocket terminal error frame (TK).
//
// When account failover is exhausted the handler used to close the client
// socket with 1013/1011 only. Codex CLI cannot tell that bare close from a
// network drop, so it reconnects and replays the turn (amplifying upstream
// load, same family as the OAuth 429 storm fixed in #3088). Codex parses a
// wrapped `{"type":"error","status":N,"error":{...},"headers":{...}}` frame
// (codex-rs/codex-api/src/endpoint/responses_websocket.rs
// parse_wrapped_websocket_error_event) and maps it like an HTTP response:
//
//   - 429 + error.type=usage_limit_reached → terminal UsageLimitReached, shows
//     the reset time from error.resets_at;
//   - other 429 → RetryLimit, retried only after headers.retry-after;
//   - 5xx → retried with backoff honoring headers.retry-after.
//
// The frame is written once per turn, before the existing close, so close
// codes keep their semantics. Public messages follow the busy-429 SSOT
// (client_facing_busy_429_tk.go); no account/pool detail reaches the wire.

const tkOpenAIWSTerminalErrorTurnKey = "tk_openai_ws_terminal_error_turn"

type tkOpenAIWSTerminalError struct {
	Status          int
	ErrType         string
	Code            string
	Message         string
	ResetsAt        time.Time
	RetryAfterValue string
}

func (e tkOpenAIWSTerminalError) payload(now time.Time) []byte {
	errBody := map[string]any{
		"type":    e.ErrType,
		"code":    e.Code,
		"message": e.Message,
	}
	if !e.ResetsAt.IsZero() && e.ResetsAt.After(now) {
		errBody["resets_at"] = e.ResetsAt.Unix()
		errBody["resets_in_seconds"] = int64(e.ResetsAt.Sub(now).Round(time.Second) / time.Second)
	}
	event := map[string]any{
		"type":   "error",
		"status": e.Status,
		"error":  errBody,
	}
	if e.RetryAfterValue != "" {
		headers := map[string]any{"retry-after": e.RetryAfterValue}
		errBody["headers"] = headers
		event["headers"] = headers
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return []byte(`{"type":"error","status":502,"error":{"type":"upstream_error","code":"upstream_error","message":"upstream request failed"}}`)
	}
	return payload
}

// tkOpenAIWSFailoverTerminalError maps an exhausted failover to the wire frame.
// status/errType/message are the values closeOpenAIWSFailoverExhausted already
// derived for ops so the frame and the ops row agree.
func tkOpenAIWSFailoverTerminalError(failoverErr *service.UpstreamFailoverError, status int, errType, message string, now time.Time) tkOpenAIWSTerminalError {
	frame := tkOpenAIWSTerminalError{Status: status, ErrType: errType, Code: "upstream_error", Message: message}
	if failoverErr == nil {
		return frame
	}
	retryAfter := tkSafeFailoverRetryAfter(failoverErr.ResponseHeaders)
	if failoverErr.Stage == service.GatewayFailureStageAccountAuth {
		frame.Code = "upstream_unavailable"
		return frame
	}
	switch {
	case status == http.StatusTooManyRequests:
		frame.Message = service.TkClientFacingBusy429Message
		if usageLimit, resetsAt := service.OpenAIUsageLimitReachedReset(failoverErr.ResponseBody, now); usageLimit {
			frame.ErrType = "usage_limit_reached"
			frame.Code = "usage_limit_reached"
			frame.ResetsAt = resetsAt
			if resetsAt.IsZero() {
				frame.RetryAfterValue = retryAfter
			}
			return frame
		}
		frame.ErrType = "rate_limit_error"
		frame.Code = "rate_limit_exceeded"
		frame.RetryAfterValue = retryAfter
	case tkOpenAIWSUpstreamCapacityStatus(failoverErr.StatusCode):
		// Codex treats server_is_overloaded/slow_down as fatal; server_error with
		// retry advice keeps its built-in backoff (same rewrite as the WS ingress
		// write path, sanitizeOpenAICapacityShedErrorCodeForClient).
		frame.Code = "server_error"
		frame.RetryAfterValue = retryAfter
		if frame.RetryAfterValue == "" {
			frame.RetryAfterValue = strconv.Itoa(service.OpenAICapacityShedClientRetryAfterSeconds)
		}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		// Upstream account credentials failed, not the caller's key. A 401/403
		// frame would make Codex treat its own login as expired; mirror the
		// HTTP path (mapUpstreamError) and report 502.
		frame.Status = http.StatusBadGateway
		frame.ErrType = "upstream_error"
		frame.Code = "upstream_authentication_failed"
	}
	return frame
}

// tkOpenAIWSSelectionTerminalError covers selection failure before any upstream
// attempt failed: empty/cooled pools are gateway capacity (HTTP parity:
// 429 api_error + Retry-After via tkNoAvailableAccounts); other scheduler
// faults stay a generic 503. Wire-only: ops attribution for these closes is
// unchanged.
func tkOpenAIWSSelectionTerminalError(err error) tkOpenAIWSTerminalError {
	if err == nil || isOpsNoAvailableAccountError(err) {
		return tkOpenAIWSTerminalError{
			Status:          http.StatusTooManyRequests,
			ErrType:         "api_error",
			Code:            "no_available_accounts",
			Message:         service.TkClientFacingBusy429Message,
			RetryAfterValue: service.NoAvailableAccountsRetryAfterSeconds,
		}
	}
	return tkOpenAIWSTerminalError{
		Status:  http.StatusServiceUnavailable,
		ErrType: "api_error",
		Code:    "service_unavailable",
		Message: "Service temporarily unavailable",
	}
}

// tkCloseOpenAIWSNoAvailableAccount sends the capacity frame, then keeps the
// existing 1013 "no available account" close.
func tkCloseOpenAIWSNoAvailableAccount(c *gin.Context, conn *coderws.Conn, err error) {
	tkWriteOpenAIWSTerminalError(c, conn, tkOpenAIWSSelectionTerminalError(err))
	closeOpenAIClientWS(conn, coderws.StatusTryAgainLater, "no available account")
}

func tkOpenAIWSUpstreamCapacityStatus(status int) bool {
	switch status {
	case 529, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

func tkSafeFailoverRetryAfter(headers http.Header) string {
	if headers == nil {
		return ""
	}
	value := strings.TrimSpace(headers.Get("Retry-After"))
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n") || !isSafeRetryAfter(value) {
		return ""
	}
	return value
}

// tkWriteOpenAIWSTerminalError writes the frame at most once per WS turn.
// Failover errors are only returned before any downstream write for the turn,
// so the per-turn guard is what prevents a second terminal frame when several
// exhaustion paths run for the same turn.
func tkWriteOpenAIWSTerminalError(c *gin.Context, conn *coderws.Conn, frame tkOpenAIWSTerminalError) {
	if c == nil || conn == nil {
		return
	}
	turn := 0
	if value, ok := c.Get(service.OpsStreamTurnKey); ok {
		turn, _ = value.(int)
	}
	if sent, ok := c.Get(tkOpenAIWSTerminalErrorTurnKey); ok {
		if sentTurn, _ := sent.(int); sentTurn == turn {
			return
		}
	}
	c.Set(tkOpenAIWSTerminalErrorTurnKey, turn)
	base := context.Background()
	if c.Request != nil {
		base = c.Request.Context()
	}
	writeCtx, cancel := context.WithTimeout(base, 2*time.Second)
	defer cancel()
	_ = conn.Write(writeCtx, coderws.MessageText, frame.payload(time.Now()))
}
