package service

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
)

// Gin context keys used by Ops error logger for capturing upstream error details.
// These keys are set by gateway services and consumed by handler/ops_error_logger.go.
const (
	// OpsModelKey / OpsRequestBodyKey mirror the unexported constants in
	// handler/ops_error_logger.go ("ops_model" / "ops_request_body"). They are
	// re-declared here so service-layer code (gateway_service_tk_upstream_error_msg.go
	// and friends) can read the request body size + model that setOpsRequestContext
	// stashed at handler entry, without creating a service→handler import cycle.
	// Keep the literal string values in sync with handler.opsModelKey /
	// handler.opsRequestBodyKey — they are a cross-package wire contract.
	OpsModelKey       = "ops_model"
	OpsRequestBodyKey = "ops_request_body"

	OpsUpstreamStatusCodeKey   = "ops_upstream_status_code"
	OpsUpstreamErrorMessageKey = "ops_upstream_error_message"
	OpsUpstreamErrorDetailKey  = "ops_upstream_error_detail"
	OpsUpstreamErrorsKey       = "ops_upstream_errors"
	OpsStreamErrorKey          = "ops_stream_error"

	// OpsUpstreamKindRequestNormalized marks Anthropic request-body normalize audit
	// events (change-kind list in Message). Recovered-200 ops logging must ignore
	// these; gateway.anthropic_request_normalized slog is the canonical audit path.
	OpsUpstreamKindRequestNormalized = "request_normalized"

	// OpsUpstreamKindClientToolContextCorrupt marks a local Anthropic request
	// rejection before any upstream call because user/tool_result context no
	// longer matches the required immediately preceding assistant/tool_use turn.
	OpsUpstreamKindClientToolContextCorrupt = "client_tool_context_corrupt"

	// OpsInternalErrorDetailKey carries a sanitized, truncated detail string for
	// internal-phase errors (cache/redis/db/context failures) that bubble up as
	// 5xx from middleware. Distinct from upstream-* keys so dashboards don't
	// confuse internal infra failures with provider errors. Consumed by
	// handler/ops_error_logger.go which appends it to ops_error_logs.error_body.
	OpsInternalErrorDetailKey = "ops_internal_error_detail"

	// Best-effort capture of the current upstream request body so ops can
	// retry the specific upstream attempt (not just the client request).
	// This value is sanitized+trimmed before being persisted.
	OpsUpstreamRequestBodyKey = "ops_upstream_request_body"

	OpsTLSFingerprintProfileIDKey   = "ops_tls_fingerprint_profile_id"
	OpsTLSFingerprintProfileNameKey = "ops_tls_fingerprint_profile_name"

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

	// Client-side configuration denials remain visible in ops_error_logs; phase/owner
	// classification routes them to error_owner=client (SLA denominator only).
	// ResponseCommittedKey 由 handleErrorResponse 系列函数在写完 HTTP 错误响应后设置。
	// ensureForwardErrorResponse 检查此 key，为 true 时跳过兜底写入，避免在已完成的 JSON 后追加 SSE。
	ResponseCommittedKey = "response_committed"

	OpsClientPolicyDeniedKey                          = "ops_client_policy_denied"
	OpsClientPolicyDeniedReasonKey                    = "ops_client_policy_denied_reason"
	OpsClientPolicyDeniedReasonIPRestriction          = "api_key_ip_restriction"
	OpsClientPolicyDeniedReasonAPIKeyGroupUnavailable = "api_key_group_unavailable"
	OpsClientPolicyDeniedReasonAPIKeyGroupUnassigned  = "api_key_group_unassigned"
	OpsClientPolicyDeniedReasonLocalFeatureGate       = "local_feature_gate"
	OpsClientPolicyDeniedReasonLocalPolicyDenied      = "local_policy_denied"

	// OpsClientClosedRequestKey marks local failures caused by the caller closing
	// the inbound request context before gateway auth/body handling completed.
	OpsClientClosedRequestKey = "ops_client_closed_request"
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

func setOpsUpstreamRequestBody(c *gin.Context, body []byte) {
	if c == nil || len(body) == 0 {
		return
	}
	// 热路径避免 string(body) 额外分配，按需在落库前再转换。
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

func MarkOpsClientPolicyDenied(c *gin.Context, reason string) {
	if c == nil {
		return
	}
	c.Set(OpsClientPolicyDeniedKey, true)
	if reason = strings.TrimSpace(reason); reason != "" {
		c.Set(OpsClientPolicyDeniedReasonKey, reason)
	}
}

func HasOpsClientPolicyDenied(c *gin.Context) bool {
	if c == nil {
		return false
	}
	v, ok := c.Get(OpsClientPolicyDeniedKey)
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

// OpsStreamError 描述网关在「响应状态已固化为 200」之后（keepalive ping 或部分数据
// 已 flush）就地以 SSE error 帧形式返回的错误。由于 HTTP 状态码停留在 200，
// 而 ops_error_logger 以 status>=400 为采集触发条件，这类流内失败
// （并发限流回退、Wait 后二次计费校验失败、流开始后才无可用账号等）本会在错误看板里
// 完全隐形。handler.handleStreamingAwareError 负责标记，ops_error_logger 中间件在
// status<400 分支消费它并补记一条错误日志。
type OpsStreamError struct {
	// ErrType 是写入 SSE 帧的对客错误类型（如 rate_limit_error / upstream_error / api_error）。
	ErrType string
	// Message 是写入 SSE 帧的对客错误消息。
	Message string
	// IntendedStatus 是流若未固化本应返回的 HTTP 状态码（如并发限流的 429）。
	// 仅用于错误分级(severity/classification)；实际 wire 状态码仍为 200。
	IntendedStatus int
}

// MarkOpsStreamError 记录一次就地 SSE 错误，供 ops 日志采集。
// 采用「首个标记生效」策略：同一请求若先后补发多帧（如上游透传错误后又追加通用兜底帧），
// 保留最先记录的根因错误，而不是被后续的 "Upstream request failed" 覆盖。
func MarkOpsStreamError(c *gin.Context, errType, message string, intendedStatus int) {
	if c == nil {
		return
	}
	if _, exists := c.Get(OpsStreamErrorKey); exists {
		return
	}
	c.Set(OpsStreamErrorKey, OpsStreamError{
		ErrType:        strings.TrimSpace(errType),
		Message:        strings.TrimSpace(message),
		IntendedStatus: intendedStatus,
	})
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
	// Required for retrying a specific upstream attempt.
	UpstreamRequestBody string `json:"upstream_request_body,omitempty"`

	// RequestBodyTruncated indicates UpstreamRequestBody was truncated for
	// storage (the stored value is smaller than the original upstream body).
	// Kept separate from Kind so the latter stays a clean categorical enum.
	RequestBodyTruncated bool `json:"request_body_truncated,omitempty"`

	// Best-effort upstream response capture (sanitized+trimmed).
	UpstreamResponseBody string `json:"upstream_response_body,omitempty"`

	// Kind is a short categorical label set by gateway services on each
	// upstream attempt. Common values currently emitted include:
	// http_error, request_error, retry, retry_exhausted,
	// retry_exhausted_failover, failover, failover_on_400, signature_error,
	// signature_retry, signature_retry_thinking,
	// signature_retry_tools_request_error, signature_retry_request_error,
	// budget_constraint_error, prompt_too_long, ws_error. Storage and
	// downstream aggregation treat it as an opaque short string; the
	// gateway emit sites are the authoritative source. Storage metadata
	// (e.g. body truncation) must NOT be appended onto this field — it has
	// its own dedicated columns/booleans.
	Kind string `json:"kind,omitempty"`
	// Stage/Scope/Reason distinguish credential acquisition from inference
	// without overloading upstream_status_code with a synthetic HTTP status.
	Stage  string `json:"stage,omitempty"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`

	Message string `json:"message,omitempty"`
	Detail  string `json:"detail,omitempty"`
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

	// If the caller didn't explicitly pass upstream request body but the gateway
	// stashed it on the context via setOpsUpstreamRequestBody, attach it so
	// downstream ops_error_logs JSON can carry per-attempt body context.
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

// checkSkipMonitoringForUpstreamEvent checks whether the upstream error event
// matches a passthrough rule with skip_monitoring=true and, if so, sets the
// OpsSkipPassthroughKey on the context.  This ensures intermediate retry /
// failover errors (which never go through the final applyErrorPassthroughRule
// path) can still suppress ops_error_logs recording.
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
		c.Set(OpsSkipPassthroughKey, true)
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
