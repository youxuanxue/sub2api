package service

// TK: single owner for buffered Anthropic SSE terminal failures.
//
// Four non-streaming OpenAI-compatible paths force Anthropic upstreams to SSE
// and then assemble the events into one JSON response. A HTTP 200 stream can
// still fail through an `error` event, a read error, an idle timeout, or EOF
// before message completion. Before any content exists those failures must
// return UpstreamFailoverError so the existing handler loop can switch account.
// Once a content block exists, replay would risk duplicate billing and answer
// drift, so the partial response is preserved and marked as a truncated SLA
// failure instead.

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const statusAnthropicOverloaded = 529

type tkAnthropicBufferedUpstreamError struct {
	ErrType        string
	Message        string
	Detail         string
	Payload        []byte
	UpstreamStatus int
	Kind           string
}

func tkParseAnthropicBufferedSSEError(payload []byte, cfg *config.Config) (*tkAnthropicBufferedUpstreamError, bool) {
	if len(payload) == 0 || strings.TrimSpace(gjson.GetBytes(payload, "type").String()) != "error" {
		return nil, false
	}
	errType := strings.TrimSpace(gjson.GetBytes(payload, "error.type").String())
	if errType == "" {
		errType = "api_error"
	}
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(payload)))
	if message == "" {
		message = "Upstream returned an error before response completion"
	}
	return &tkAnthropicBufferedUpstreamError{
		ErrType:        errType,
		Message:        message,
		Detail:         tkAnthropicBufferedErrorDetail(payload, cfg),
		Payload:        append([]byte(nil), payload...),
		UpstreamStatus: tkAnthropicErrorTypeUpstreamStatus(errType),
		Kind:           "stream_error",
	}, true
}

func tkAnthropicBufferedSyntheticFailure(kind, message string) *tkAnthropicBufferedUpstreamError {
	message = sanitizeUpstreamErrorMessage(strings.TrimSpace(message))
	if message == "" {
		message = "Upstream stream ended before response completion"
	}
	payload, _ := json.Marshal(gin.H{
		"type": "error",
		"error": gin.H{
			"type":    "api_error",
			"message": message,
		},
	})
	return &tkAnthropicBufferedUpstreamError{
		ErrType:        "api_error",
		Message:        message,
		Payload:        payload,
		UpstreamStatus: http.StatusBadGateway,
		Kind:           kind,
	}
}

func tkAnthropicBufferedErrorDetail(payload []byte, cfg *config.Config) string {
	if cfg == nil || !cfg.Gateway.LogUpstreamErrorBody {
		return ""
	}
	maxBytes := cfg.Gateway.LogUpstreamErrorBodyMaxBytes
	if maxBytes <= 0 {
		maxBytes = 2048
	}
	return truncateString(string(payload), maxBytes)
}

func tkAnthropicErrorTypeUpstreamStatus(errType string) int {
	switch strings.TrimSpace(strings.ToLower(errType)) {
	case "invalid_request_error":
		return http.StatusBadRequest
	case "authentication_error":
		return http.StatusUnauthorized
	case "permission_error":
		return http.StatusForbidden
	case "not_found_error":
		return http.StatusNotFound
	case "request_too_large":
		return http.StatusRequestEntityTooLarge
	case "rate_limit_error":
		return http.StatusTooManyRequests
	case "overloaded_error":
		return statusAnthropicOverloaded
	default:
		return http.StatusInternalServerError
	}
}

func tkAnthropicBufferedHasUsableContent(finalResp *apicompat.AnthropicResponse) bool {
	return finalResp != nil && len(finalResp.Content) > 0
}

func tkAnthropicBufferedEventCompletesMessage(event *apicompat.AnthropicStreamEvent) bool {
	if event == nil {
		return false
	}
	if event.Type == "message_stop" {
		return true
	}
	return event.Type == "message_delta" && event.Delta != nil && strings.TrimSpace(event.Delta.StopReason) != ""
}

func tkAnthropicBufferedFailure(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	requestID string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) *UpstreamFailoverError {
	if upstreamErr == nil {
		upstreamErr = tkAnthropicBufferedSyntheticFailure(
			"stream_incomplete",
			"Upstream stream ended without a response",
		)
	}
	upstreamErr = tkNormalizeAnthropicBufferedUpstreamError(upstreamErr)
	tkRecordAnthropicBufferedUpstreamError(c, account, requestID, upstreamErr.Kind, upstreamErr)

	var headers http.Header
	if resp != nil && resp.Header != nil {
		headers = resp.Header.Clone()
	}
	body := append([]byte(nil), upstreamErr.Payload...)
	if len(body) == 0 {
		body = tkAnthropicBufferedSyntheticFailure(upstreamErr.Kind, upstreamErr.Message).Payload
	}
	return &UpstreamFailoverError{
		StatusCode:        upstreamErr.UpstreamStatus,
		ResponseBody:      body,
		ResponseHeaders:   headers,
		ClientStatusCode:  tkAnthropicBufferedClientStatus(upstreamErr.UpstreamStatus),
		ClientErrorType:   tkAnthropicBufferedClientErrType(upstreamErr.ErrType),
		ClientMessage:     upstreamErr.Message,
		NextAccountAction: NextAccountRetry,
	}
}

func tkNormalizeAnthropicBufferedUpstreamError(
	upstreamErr *tkAnthropicBufferedUpstreamError,
) *tkAnthropicBufferedUpstreamError {
	if upstreamErr == nil || !cnProviderResponseIndicatesInsufficientBalance(upstreamErr.Payload) {
		return upstreamErr
	}
	semanticErr := *upstreamErr
	semanticErr.UpstreamStatus = http.StatusPaymentRequired
	return &semanticErr
}

func (s *GatewayService) tkAnthropicBufferedFailoverError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	requestID string,
	requestedModel string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) *UpstreamFailoverError {
	failoverErr := tkAnthropicBufferedFailure(c, account, resp, requestID, upstreamErr)
	if s == nil || account == nil || s.rateLimitService == nil {
		return failoverErr
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	shouldDisable := s.rateLimitService.HandleUpstreamError(
		ctx,
		account,
		failoverErr.StatusCode,
		failoverErr.ResponseHeaders,
		failoverErr.ResponseBody,
		requestedModel,
	)
	failoverErr.RetryableOnSameAccount = !shouldDisable && account.IsPoolMode() && account.IsPoolModeRetryableStatus(failoverErr.StatusCode)
	return failoverErr
}

func (s *OpenAIGatewayService) tkAnthropicBufferedFailoverError(
	c *gin.Context,
	account *Account,
	resp *http.Response,
	requestID string,
	requestedModel string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) *UpstreamFailoverError {
	base := tkAnthropicBufferedFailure(c, account, resp, requestID, upstreamErr)
	if s == nil || account == nil {
		return base
	}
	ctx := context.Background()
	if c != nil && c.Request != nil {
		ctx = c.Request.Context()
	}
	shouldDisable := s.handleOpenAIAccountUpstreamError(
		ctx,
		account,
		base.StatusCode,
		base.ResponseHeaders,
		base.ResponseBody,
		requestedModel,
	)
	failoverErr := s.newOpenAIAccountFailoverError(
		account,
		base.StatusCode,
		base.ResponseHeaders,
		base.ResponseBody,
		base.ClientMessage,
		shouldDisable,
		account.IsPoolMode() && account.IsPoolModeRetryableStatus(base.StatusCode),
	)
	failoverErr.ClientStatusCode = base.ClientStatusCode
	failoverErr.ClientErrorType = base.ClientErrorType
	failoverErr.ClientMessage = base.ClientMessage
	failoverErr.NextAccountAction = NextAccountRetry
	return failoverErr
}

func tkAnthropicBufferedPartialFailure(
	c *gin.Context,
	account *Account,
	requestID string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) {
	if upstreamErr == nil {
		return
	}
	tkRecordAnthropicBufferedUpstreamError(c, account, requestID, "stream_truncated", upstreamErr)
	MarkOpsStreamFailure(
		c,
		"upstream_error",
		"upstream_stream_truncated",
		upstreamErr.Message,
		tkAnthropicBufferedClientStatus(upstreamErr.UpstreamStatus),
	)
	logger.L().Warn("buffered anthropic assembly: upstream failed after partial content",
		zap.String("request_id", requestID),
		zap.String("upstream_error_type", upstreamErr.ErrType),
		zap.Int("upstream_status", upstreamErr.UpstreamStatus),
	)
}

func tkRecordAnthropicBufferedUpstreamError(
	c *gin.Context,
	account *Account,
	requestID string,
	kind string,
	upstreamErr *tkAnthropicBufferedUpstreamError,
) {
	setOpsUpstreamError(c, upstreamErr.UpstreamStatus, upstreamErr.Message, upstreamErr.Detail)
	event := OpsUpstreamErrorEvent{
		UpstreamStatusCode: upstreamErr.UpstreamStatus,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Reason:             upstreamErr.ErrType,
		Message:            upstreamErr.Message,
		Detail:             upstreamErr.Detail,
	}
	if account != nil {
		event.Platform = account.Platform
		event.AccountID = account.ID
		event.AccountName = account.Name
	}
	appendOpsUpstreamError(c, event)
}

func tkAnthropicBufferedClientStatus(upstreamStatus int) int {
	switch upstreamStatus {
	case http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusBadGateway
	case statusAnthropicOverloaded:
		return http.StatusServiceUnavailable
	}
	return mapUpstreamStatusCode(upstreamStatus)
}

func tkAnthropicBufferedClientErrType(errType string) string {
	switch strings.TrimSpace(strings.ToLower(errType)) {
	case "invalid_request_error":
		return "invalid_request_error"
	case "not_found_error":
		return "not_found_error"
	case "request_too_large":
		return "request_too_large"
	case "rate_limit_error":
		return "rate_limit_error"
	case "overloaded_error":
		return "overloaded_error"
	case "authentication_error", "permission_error":
		return "upstream_error"
	default:
		return "server_error"
	}
}
