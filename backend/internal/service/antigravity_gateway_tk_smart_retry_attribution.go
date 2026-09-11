package service

import (
	"net/http"
	"strings"
)

// appendAntigravitySmartRetryOpsEvent records the upstream verdict that made
// handleSmartRetry give up on the current account.
//
// WHY (antigravity image/opus 5xx misattributed as platform-owned, prod +
// edge-us4-ls 2026-09-10): every OTHER exit from the Antigravity retry loop
// records an ops upstream event (Kind="retry"/"http_error"/"failover"/
// "request_error"), but handleSmartRetry's terminal paths returned only an
// AntigravityAccountSwitchError or a bare *http.Response and emitted nothing.
//
// The account-switch signal is converted to an UpstreamFailoverError by
// ForwardGemini / forwardAntigravityCompat and then consumed by the handler's
// failover loop, so no upstream status ever reaches the gin context. With no
// event and no OpsUpstreamStatusCodeKey, hasOpsUpstreamErrorContext returns
// false, classifyOpsErrorLog keeps errType="api_error" at its default
// phase="internal", and classifyOpsErrorOwner maps internal => "platform".
//
// Consequence: a Google 503 MODEL_CAPACITY_EXHAUSTED (or 502) on
// gemini-2.5-flash-image / claude-opus-4-6 was booked as OUR bug. In
// daily_error_report.py that combination — owner=platform, phase=internal,
// status>=500 — is exactly `code_owned`, which at >=5 occurrences becomes
// confidence=high and repair_eligible=true. So provider capacity exhaustion
// was queued for an automated code-repair Draft PR against code that is not at
// fault, while the real signal (upstream capacity) never entered
// upstream_error_rate.
//
// Recording the event restores provider attribution: hasOpsUpstreamErrorContext
// becomes true, phase becomes "upstream", and errorOwner becomes "provider".
// The existing TK narrowing still applies on top — tkUpstreamDownstreamCapacity
// keeps a relayed edge's own pool-empty envelope owned as routing, and
// tkUpstreamClientCanceled keeps client cancels owned by the client — because
// both run off the same captured verdict.
//
// Kind is deliberately distinct from the "retry" events emitted mid-loop so the
// ops trail shows which exit was taken, and no rate-limit, penalty, or failover
// decision is changed by this function: it is attribution only.
func (s *AntigravityGatewayService) appendAntigravitySmartRetryOpsEvent(
	p antigravityRetryLoopParams,
	statusCode int,
	headers http.Header,
	body []byte,
	kind string,
) {
	if p.c == nil || p.account == nil {
		return
	}
	requestID := ""
	if headers != nil {
		requestID = headers.Get("x-request-id")
	}
	appendOpsUpstreamError(p.c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(p.account),
		ProxyName:          opsUpstreamProxyName(p.account),
		Platform:           p.account.Platform,
		AccountID:          p.account.ID,
		AccountName:        p.account.Name,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Message:            sanitizeUpstreamErrorMessage(strings.TrimSpace(extractAntigravityErrorMessage(body))),
		Detail:             s.getUpstreamErrorDetail(body),
	})
}
