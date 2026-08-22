package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

// Gin context keys used by Ops error logger for capturing upstream error details.
// These keys are set by gateway services and consumed by handler/ops_error_logger.go.
const (
	// OpsModelKey / OpsRequestBodyKey mirror the unexported constants in
	// handler/ops_error_logger.go so service-layer code can read the request
	// body size + model stashed at handler entry without a service→handler cycle.
	OpsModelKey       = "ops_model"
	OpsRequestBodyKey = "ops_request_body"

	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"
	OpsUpstreamModelKey        = "ops_upstream_model"

	// OpsUpstreamKindRequestNormalized marks Anthropic request-body normalize audit
	// events (change-kind list in Message). Recovered-200 ops logging must ignore
	// these; gateway.anthropic_request_normalized slog is the canonical audit path.
	OpsUpstreamKindRequestNormalized = "request_normalized"

	// Optional stage latencies (milliseconds) for troubleshooting and alerting.
	OpsAuthLatencyMsKey      = "ops_auth_latency_ms"
	OpsRoutingLatencyMsKey   = "ops_routing_latency_ms"
	OpsUpstreamLatencyMsKey  = "ops_upstream_latency_ms"
	OpsResponseLatencyMsKey  = "ops_response_latency_ms"
	OpsTimeToFirstTokenMsKey = "ops_time_to_first_token_ms"
	// OpenAI WS 关键观测字段
	OpsOpenAIWSQueueWaitMsKey = "ops_openai_ws_queue_wait_ms"
	OpsOpenAIWSConnPickMsKey  = "ops_openai_ws_conn_pick_ms"
	OpsOpenAIWSConnReusedKey  = "ops_openai_ws_conn_reused"
	OpsOpenAIWSConnIDKey      = "ops_openai_ws_conn_id"

	// OpsSkipPassthroughKey 由 applyErrorPassthroughRule 在命中 skip_monitoring=true 的规则时设置。
	// ops_error_logger 中间件检查此 key，为 true 时跳过错误记录。
	OpsSkipPassthroughKey = "ops_skip_passthrough"

	// OpsStreamErrorKey 保存 handleStreamingAwareError 在「响应已固化为 HTTP 200 的 SSE 流」
	// 上就地(in-band)补发错误帧时记录的 OpsStreamError。因为 wire 状态码停留在 200，
	// ops_error_logger 的 status>=400 采集路径永远不会触发，这类流内失败
	//（例如等待并发槽位超时后回退的限流、Wait 后二次计费校验失败）本会在错误看板里隐形。
	OpsStreamErrorKey  = "ops_stream_error"
	OpsStreamErrorsKey = "ops_stream_errors"
	OpsStreamTurnKey   = "ops_stream_turn"

	// Client-side configuration denials should remain visible in ops_error_logs,
	// but should be excluded from SLA/error-rate calculations.
	// ResponseCommittedKey 由 handleErrorResponse 系列函数在写完 HTTP 错误响应后设置。
	// ensureForwardErrorResponse 检查此 key，为 true 时跳过兜底写入，避免在已完成的 JSON 后追加 SSE。
	ResponseCommittedKey = "response_committed"

	OpsClientBusinessLimitedKey                           = "ops_client_business_limited"
	OpsClientBusinessLimitedReasonKey                     = "ops_client_business_limited_reason"
	OpsClientBusinessLimitedReasonIPRestriction           = "api_key_ip_restriction"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable  = "api_key_group_unavailable"
	OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned   = "api_key_group_unassigned"
	OpsClientBusinessLimitedReasonLocalFeatureGate        = "local_feature_gate"
	OpsClientBusinessLimitedReasonLocalPolicyDenied       = "local_policy_denied"
	OpsClientBusinessLimitedReasonLocalModelConfiguration = "local_model_configuration"

	// TK aliases kept after upstream renamed policy-denied → business-limited.
	OpsClientPolicyDeniedKey                          = OpsClientBusinessLimitedKey
	OpsClientPolicyDeniedReasonKey                    = OpsClientBusinessLimitedReasonKey
	OpsClientPolicyDeniedReasonIPRestriction          = OpsClientBusinessLimitedReasonIPRestriction
	OpsClientPolicyDeniedReasonAPIKeyGroupUnavailable = OpsClientBusinessLimitedReasonAPIKeyGroupUnavailable
	OpsClientPolicyDeniedReasonAPIKeyGroupUnassigned  = OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned
	OpsClientPolicyDeniedReasonLocalFeatureGate       = OpsClientBusinessLimitedReasonLocalFeatureGate
	OpsClientPolicyDeniedReasonLocalPolicyDenied      = OpsClientBusinessLimitedReasonLocalPolicyDenied

	// OpsInternalErrorDetailKey carries a sanitized, truncated detail string for
	// internal-phase errors that bubble up as 5xx from middleware.
	OpsInternalErrorDetailKey = "ops_internal_error_detail"

	// OpsClientClosedRequestKey marks local failures caused by the caller closing
	// the inbound request context before gateway auth/body handling completed.
	OpsClientClosedRequestKey = "ops_client_closed_request"

	// Best-effort capture of the current upstream request body so ops can
	// retry the specific upstream attempt (not just the client request).
	OpsUpstreamRequestBodyKey = "ops_upstream_request_body"

	OpsTLSFingerprintProfileIDKey   = "ops_tls_fingerprint_profile_id"
	OpsTLSFingerprintProfileNameKey = "ops_tls_fingerprint_profile_name"
	OpsClientContentFilteredKey     = "ops_client_content_filtered"
	OpsGatewayQueueWaitMsKey        = "ops_gateway_queue_wait_ms"

	// OpsUpstreamKindClientToolContextCorrupt marks a local Anthropic request
	// rejection before any upstream call because user/tool_result context no
	// longer matches the required immediately preceding assistant/tool_use turn.
	OpsUpstreamKindClientToolContextCorrupt = "client_tool_context_corrupt"
)

func MarkResponseCommitted(c *gin.Context) { c.Set(ResponseCommittedKey, true) }

func IsResponseCommitted(c *gin.Context) bool {
	v, ok := c.Get(ResponseCommittedKey)
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func SetOpsLatencyMs(c *gin.Context, key string, value int64) {
	if c == nil || strings.TrimSpace(key) == "" || value < 0 {
		return
	}
	c.Set(key, value)
}

func AddOpsLatencyMs(c *gin.Context, key string, delta int64) {
	if c == nil || strings.TrimSpace(key) == "" || delta <= 0 {
		return
	}
	existing, _ := contextLatencyMs(c, key)
	SetOpsLatencyMs(c, key, existing+delta)
}

func AddOpsGatewayQueueWaitMs(c *gin.Context, waitMs int64) {
	AddOpsLatencyMs(c, OpsGatewayQueueWaitMsKey, waitMs)
}

// SetOpsUpstreamModel stores only the effective model slug for final Ops
// attribution. Call it immediately before an upstream attempt is dispatched.
func SetOpsUpstreamModel(c *gin.Context, model string) {
	if c == nil {
		return
	}
	if model = strings.TrimSpace(model); model != "" {
		c.Set(OpsUpstreamModelKey, model)
	}
}

// ClearOpsUpstreamModel invalidates attempt-scoped model attribution before a
// newly selected account starts credential resolution or upstream dispatch.
func ClearOpsUpstreamModel(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsUpstreamModelKey, "")
}

func MarkOpsClientBusinessLimited(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientBusinessLimitedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientBusinessLimitedReasonKey, reason)
	}
}

func MarkOpsClientPolicyDenied(c *gin.Context, reason string) {
	MarkOpsClientBusinessLimited(c, reason)
}

func setOpsUpstreamRequestBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	c.Set(OpsUpstreamRequestBodyKey, body)
}

func setOpsTLSFingerprintProfile(c *gin.Context, account *Account, profile *tlsfingerprint.Profile) {
	if c == nil || account == nil || profile == nil {
		return
	}
	if id := account.GetTLSFingerprintProfileID(); id > 0 {
		c.Set(OpsTLSFingerprintProfileIDKey, id)
	}
	if name := strings.TrimSpace(profile.Name); name != "" {
		c.Set(OpsTLSFingerprintProfileNameKey, name)
	}
}

func resolveOpsTLSFingerprintProfile(c *gin.Context, svc *TLSFingerprintProfileService, account *Account) *tlsfingerprint.Profile {
	if svc == nil {
		return nil
	}
	profile := svc.ResolveTLSProfile(account)
	setOpsTLSFingerprintProfile(c, account, profile)
	return profile
}

func MarkOpsClientContentFiltered(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsClientContentFilteredKey, true)
}

func HasOpsClientContentFiltered(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientContentFilteredKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

func MarkOpsClientClosedRequest(c *gin.Context) {
	if c == nil {
		return
	}
	c.Set(OpsClientClosedRequestKey, true)
}

func HasOpsClientClosedRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientClosedRequestKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

func HasOpsClientPolicyDenied(c *gin.Context) bool {
	return HasOpsClientBusinessLimited(c)
}

func HasOpsClientBusinessLimited(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientBusinessLimitedKey)
	if !ok {
		return false
	}
	marked, _ := v.(bool)
	return marked
}

func OpsClientBusinessLimitedReason(c *gin.Context) string {
	if c == nil {
		return ""
	}
	v, ok := c.Get(OpsClientBusinessLimitedReasonKey)
	if !ok {
		return ""
	}
	reason, _ := v.(string)
	return strings.TrimSpace(reason)
}

// OpsStreamError 描述网关在「响应状态已固化为 200」之后（keepalive ping 或部分数据
// 已 flush）就地以 SSE error 帧形式返回的错误。由于 HTTP 状态码停留在 200，
// 而 ops_error_logger 以 status>=400 为采集触发条件，这类流内失败
// （并发限流回退、Wait 后二次计费校验失败、流开始后才无可用账号等）本会在错误看板里
// 完全隐形。handler.handleStreamingAwareError 负责标记，ops_error_logger 中间件在
// status<400 分支消费它并补记一条错误日志。
type OpsStreamError struct {
	// ErrType 是写入 SSE 帧的对客错误类型（如 rate_limit_error / upstream_error / api_error）。
	ErrType string
	// Code 是可选的稳定错误分类；用于既保留通用 OpenAI error.type，又向客户端和 Ops
	// 暴露可编程判断的细分类（如 upstream_http2_stream_error）。
	Code string
	// Message 是写入 SSE 帧的对客错误消息。
	Message string
	// IntendedStatus 是流若未固化本应返回的 HTTP 状态码（如并发限流的 429）。
	// 默认仅用于错误分级；CountTowardsSLA=true 时也作为 Ops 的逻辑状态码。
	IntendedStatus int
	// CountTowardsSLA 表示虽然 wire 状态已固化为 200，请求在应用语义上仍然失败，
	// Ops 应使用 IntendedStatus 计入错误率/SLA。
	CountTowardsSLA bool
	// Turn identifies a WebSocket turn. HTTP/SSE requests leave it at zero.
	Turn int
	// SkipMonitoring snapshots the rule decision for this visible failure.
	SkipMonitoring  bool
	AccountID       int64
	UpstreamModel   string
	UpstreamStatus  int
	UpstreamMessage string
	UpstreamDetail  string
	UpstreamErrors  []*OpsUpstreamErrorEvent
}

const maxOpsStreamErrorsPerRequest = 64

// BeginOpsStreamTurn scopes first-wins deduplication to one WebSocket turn.
func BeginOpsStreamTurn(c *gin.Context, turn int) {
	if c == nil || turn <= 0 {
		return
	}
	c.Set(OpsStreamTurnKey, turn)
	// Rule and attempt state is turn-scoped on a long-lived WS connection.
	c.Set(OpsSkipPassthroughKey, false)
	c.Set(OpsUpstreamErrorsKey, []*OpsUpstreamErrorEvent{})
	c.Set(OpsUpstreamStatusCodeKey, 0)
	c.Set(OpsUpstreamErrorMessageKey, "")
	c.Set(OpsUpstreamErrorDetailKey, "")
}

// MarkOpsStreamError 记录一次就地 SSE 错误，供 ops 日志采集。
// 采用「首个标记生效」策略：同一请求若先后补发多帧（如上游透传错误后又追加通用兜底帧），
// 保留最先记录的根因错误，而不是被后续的 "Upstream request failed" 覆盖。
func MarkOpsStreamError(c *gin.Context, errType, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:        errType,
		Message:        message,
		IntendedStatus: intendedStatus,
	})
}

// MarkOpsStreamFailure records an in-band stream error that represents a failed
// request and therefore must count towards Ops error rate/SLA despite HTTP 200
// already being committed on the wire.
func MarkOpsStreamFailure(c *gin.Context, errType, code, message string, intendedStatus int) {
	markOpsStreamError(c, OpsStreamError{
		ErrType:         errType,
		Code:            code,
		Message:         message,
		IntendedStatus:  intendedStatus,
		CountTowardsSLA: true,
	})
}

func markOpsStreamError(c *gin.Context, streamErr OpsStreamError) {
	if c == nil {
		return
	}
	streamErr.ErrType = strings.TrimSpace(streamErr.ErrType)
	streamErr.Code = strings.TrimSpace(streamErr.Code)
	streamErr.Message = strings.TrimSpace(streamErr.Message)
	streamErr.SkipMonitoring = currentOpsFailureSkipMonitoring(c)
	snapshotOpsStreamErrorContext(c, &streamErr)
	if GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		if value, ok := c.Get(OpsStreamTurnKey); ok {
			streamErr.Turn, _ = value.(int)
		}
		var errorsForRequest []OpsStreamError
		if value, ok := c.Get(OpsStreamErrorsKey); ok {
			errorsForRequest, _ = value.([]OpsStreamError)
		}
		if len(errorsForRequest) > 0 && errorsForRequest[len(errorsForRequest)-1].Turn == streamErr.Turn {
			return
		}
		errorsForRequest = append(errorsForRequest, streamErr)
		if len(errorsForRequest) > maxOpsStreamErrorsPerRequest {
			errorsForRequest = append([]OpsStreamError(nil), errorsForRequest[len(errorsForRequest)-maxOpsStreamErrorsPerRequest:]...)
		}
		c.Set(OpsStreamErrorsKey, errorsForRequest)
		c.Set(OpsStreamErrorKey, streamErr)
		return
	}
	if _, exists := c.Get(OpsStreamErrorKey); exists {
		return
	}
	c.Set(OpsStreamErrorKey, streamErr)
}

func snapshotOpsStreamErrorContext(c *gin.Context, streamErr *OpsStreamError) {
	if c == nil || streamErr == nil {
		return
	}
	if c.Request != nil {
		if accountID, ok := c.Request.Context().Value(ctxkey.AccountID).(int64); ok && accountID > 0 {
			streamErr.AccountID = accountID
		}
	}
	if value, ok := c.Get(OpsUpstreamModelKey); ok {
		streamErr.UpstreamModel, _ = value.(string)
		streamErr.UpstreamModel = strings.TrimSpace(streamErr.UpstreamModel)
	}
	if value, ok := c.Get(OpsUpstreamStatusCodeKey); ok {
		switch status := value.(type) {
		case int:
			streamErr.UpstreamStatus = status
		case int64:
			streamErr.UpstreamStatus = int(status)
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorMessageKey); ok {
		streamErr.UpstreamMessage, _ = value.(string)
		streamErr.UpstreamMessage = strings.TrimSpace(streamErr.UpstreamMessage)
	}
	if value, ok := c.Get(OpsUpstreamErrorDetailKey); ok {
		streamErr.UpstreamDetail, _ = value.(string)
		streamErr.UpstreamDetail = strings.TrimSpace(streamErr.UpstreamDetail)
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*OpsUpstreamErrorEvent); ok {
			streamErr.UpstreamErrors = make([]*OpsUpstreamErrorEvent, 0, len(events))
			for _, event := range events {
				if event == nil {
					streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, nil)
					continue
				}
				copyOfEvent := *event
				streamErr.UpstreamErrors = append(streamErr.UpstreamErrors, &copyOfEvent)
			}
		}
	}
}

func currentOpsFailureSkipMonitoring(c *gin.Context) bool {
	if c == nil {
		return false
	}
	if value, ok := c.Get(OpsSkipPassthroughKey); ok {
		if skip, _ := value.(bool); skip {
			return true
		}
	}
	if value, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if events, ok := value.([]*OpsUpstreamErrorEvent); ok {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i] != nil {
					return events[i].SkipMonitoring
				}
			}
		}
	}
	return false
}

// GetOpsStreamError 返回本请求记录的就地 SSE 错误（若有）。
func GetOpsStreamError(c *gin.Context) (OpsStreamError, bool) {
	if c == nil {
		return OpsStreamError{}, false
	}
	v, ok := c.Get(OpsStreamErrorKey)
	if !ok {
		return OpsStreamError{}, false
	}
	se, ok := v.(OpsStreamError)
	return se, ok
}

func GetOpsStreamErrors(c *gin.Context) []OpsStreamError {
	if c == nil {
		return nil
	}
	if value, ok := c.Get(OpsStreamErrorsKey); ok {
		if errorsForRequest, ok := value.([]OpsStreamError); ok && len(errorsForRequest) > 0 {
			return append([]OpsStreamError(nil), errorsForRequest...)
		}
	}
	if streamErr, ok := GetOpsStreamError(c); ok {
		return []OpsStreamError{streamErr}
	}
	return nil
}

// SetOpsUpstreamError is the exported wrapper for setOpsUpstreamError, used by
// handler-layer code (e.g. failover-exhausted paths) that needs to record the
// original upstream status code before mapping it to a client-facing code.
func SetOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	setOpsUpstreamError(c, upstreamStatusCode, upstreamMessage, upstreamDetail)
}

func setOpsUpstreamError(c *gin.Context, upstreamStatusCode int, upstreamMessage, upstreamDetail string) {
	if c == nil {
		return
	}
	if upstreamStatusCode > 0 {
		c.Set(OpsUpstreamStatusCodeKey, upstreamStatusCode)
	}
	if msg := strings.TrimSpace(upstreamMessage); msg != "" {
		c.Set(OpsUpstreamErrorMessageKey, msg)
	}
	if detail := strings.TrimSpace(upstreamDetail); detail != "" {
		c.Set(OpsUpstreamErrorDetailKey, detail)
	}
}

// OpsUpstreamErrorEvent describes one upstream error attempt during a single gateway request.
// It is stored in ops_error_logs.upstream_errors as a JSON array.
type OpsUpstreamErrorEvent struct {
	AtUnixMs int64 `json:"at_unix_ms,omitempty"`

	// Passthrough 表示本次请求是否命中“原样透传（仅替换认证）”分支。
	// 该字段用于排障与灰度评估；存入 JSON，不涉及 DB schema 变更。
	Passthrough bool `json:"passthrough,omitempty"`

	// Context
	Platform                  string `json:"platform,omitempty"`
	AccountID                 int64  `json:"account_id,omitempty"`
	AccountName               string `json:"account_name,omitempty"`
	TLSFingerprintProfileID   int64  `json:"tls_fingerprint_profile_id,omitempty"`
	TLSFingerprintProfileName string `json:"tls_fingerprint_profile_name,omitempty"`

	// Outcome
	UpstreamStatusCode int    `json:"upstream_status_code,omitempty"`
	UpstreamRequestID  string `json:"upstream_request_id,omitempty"`

	// UpstreamURL is the actual upstream URL that was called (host + path, query/fragment stripped).
	// Helps debug 404/routing errors by showing which endpoint was targeted.
	UpstreamURL string `json:"upstream_url,omitempty"`

	// Best-effort upstream request capture (sanitized+trimmed).
	UpstreamRequestBody string `json:"upstream_request_body,omitempty"`
	// RequestBodyTruncated indicates UpstreamRequestBody was truncated for storage.
	RequestBodyTruncated bool `json:"request_body_truncated,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind: http_error | request_error | retry_exhausted | failover
	Kind string `json:"kind,omitempty"`
	// Stage/Scope/Reason distinguish credential acquisition from inference
	// without overloading upstream_status_code with a synthetic HTTP status.
	Stage  string `json:"stage,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`

	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`

	// SkipMonitoring is request-local rule state. It is intentionally excluded
	// from persisted attempt JSON. The logger consults it only when this event is
	// the final client-visible failure; recovered attempts remain provider-health
	// telemetry and do not count as failed requests.
	SkipMonitoring bool `json:"-"`
}

func appendOpsUpstreamError(c *gin.Context, ev OpsUpstreamErrorEvent) {
	if c == nil {
		return
	}
	if ev.AtUnixMs <= 0 {
		ev.AtUnixMs = time.Now().UnixMilli()
	}
	ev.Platform = strings.TrimSpace(ev.Platform)
	ev.TLSFingerprintProfileName = strings.TrimSpace(ev.TLSFingerprintProfileName)
	if ev.TLSFingerprintProfileID <= 0 {
		if v, ok := c.Get(OpsTLSFingerprintProfileIDKey); ok {
			switch t := v.(type) {
			case int64:
				ev.TLSFingerprintProfileID = t
			case int:
				ev.TLSFingerprintProfileID = int64(t)
			case float64:
				ev.TLSFingerprintProfileID = int64(t)
			}
		}
	}
	if ev.TLSFingerprintProfileName == "" {
		if v, ok := c.Get(OpsTLSFingerprintProfileNameKey); ok {
			if s, ok := v.(string); ok {
				ev.TLSFingerprintProfileName = strings.TrimSpace(s)
			}
		}
	}
	ev.UpstreamRequestID = strings.TrimSpace(ev.UpstreamRequestID)
	ev.UpstreamRequestBody = strings.TrimSpace(ev.UpstreamRequestBody)
	ev.UpstreamResponseBody = strings.TrimSpace(ev.UpstreamResponseBody)
	ev.Kind = strings.TrimSpace(ev.Kind)
	ev.Stage = strings.TrimSpace(ev.Stage)
	ev.Scope = strings.TrimSpace(ev.Scope)
	ev.Reason = strings.TrimSpace(ev.Reason)
	ev.UpstreamURL = strings.TrimSpace(ev.UpstreamURL)
	ev.Message = strings.TrimSpace(ev.Message)
	ev.Detail = strings.TrimSpace(ev.Detail)
	if ev.Message != "" {
		ev.Message = sanitizeUpstreamErrorMessage(ev.Message)
	}

	if ev.UpstreamRequestBody == "" {
		if v, ok := c.Get(OpsUpstreamRequestBodyKey); ok {
			switch raw := v.(type) {
			case string:
				ev.UpstreamRequestBody = strings.TrimSpace(raw)
			case []byte:
				ev.UpstreamRequestBody = strings.TrimSpace(string(raw))
			}
		}
	}

	var existing []*OpsUpstreamErrorEvent
	if v, ok := c.Get(OpsUpstreamErrorsKey); ok {
		if arr, ok := v.([]*OpsUpstreamErrorEvent); ok {
			existing = arr
		}
	}

	evCopy := ev
	existing = append(existing, &evCopy)
	c.Set(OpsUpstreamErrorsKey, existing)

	checkSkipMonitoringForUpstreamEvent(c, &evCopy)
}

// checkSkipMonitoringForUpstreamEvent snapshots whether this attempt matches a
// skip_monitoring passthrough rule. The final failure decides whether the
// request error is hidden; an intermediate recovered attempt cannot suppress a
// later client-visible failure.
func checkSkipMonitoringForUpstreamEvent(c *gin.Context, ev *OpsUpstreamErrorEvent) {
	if ev.UpstreamStatusCode == 0 {
		return
	}

	svc := getBoundErrorPassthroughService(c)
	if svc == nil {
		return
	}

	// Use the best available body representation for keyword matching.
	// Even when body is empty, MatchRule can still match rules that only
	// specify ErrorCodes (no Keywords), so we always call it.
	body := ev.Detail
	if body == "" {
		body = ev.Message
	}

	rule := svc.MatchRule(ev.Platform, ev.UpstreamStatusCode, []byte(body))
	if rule != nil && rule.SkipMonitoring {
		ev.SkipMonitoring = true
	}
}

func marshalOpsUpstreamErrors(events []*OpsUpstreamErrorEvent) *string {
	if len(events) == 0 {
		return nil
	}
	// Ensure we always store a valid JSON value.
	raw, err := json.Marshal(events)
	if err != nil || len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}

func ParseOpsUpstreamErrors(raw string) ([]*OpsUpstreamErrorEvent, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []*OpsUpstreamErrorEvent{}, nil
	}
	var out []*OpsUpstreamErrorEvent
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// safeUpstreamURL returns scheme + host + path from a URL, stripping query/fragment
// to avoid leaking sensitive query parameters (e.g. OAuth tokens).
func safeUpstreamURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	if idx := strings.IndexByte(rawURL, '#'); idx >= 0 {
		rawURL = rawURL[:idx]
	}
	return rawURL
}
