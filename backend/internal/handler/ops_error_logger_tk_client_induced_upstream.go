package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkUpstreamClientInducedRejection reports whether the upstream error captured on
// the request context is a CLIENT-induced request rejection — the caller asked
// for something the upstream refuses on request-validation grounds (bad model,
// malformed params, oversized body) — rather than an upstream/account-health
// failure.
//
// Why this exists (prod P0 2026-06-05T14:21Z, upstream_error_rate=40.32% overall):
// Codex / ChatGPT-OAuth OpenAI accounts answer HTTP 400 invalid_request_error
// ("The 'gpt-4o' model is not supported when using Codex with a ChatGPT account.")
// whenever a client requests a model that account *type* cannot serve. Those are
// caller-fault request rejections, not provider outages — yet classifyOpsErrorLog
// blindly relabels every error carrying an upstream status code as phase=upstream
// / error_owner=provider, so a single client looping unsupported models floods
// upstream_error_rate (the provider-health P0 capacity alert) and pages on-call.
// The structured columns cannot disambiguate after the fact (provider_error_type
// is NULL and error_type is the api_error wrapper), so the fix has to live at the
// classification (write) layer where the upstream status + message are still on
// the context.
//
// This deliberately mirrors the amplifier-side boundary in
// ratelimit_service_tk_client_induced_400.go (#602 / upstream Wei-Shaw/sub2api#2608):
// invalid_request_error + request_too_large (and the 413 status) are client-induced;
// the account-level 4xx signals (organization disabled / credit balance exhausted /
// identity verification required) are NOT — they stay provider-owned because they
// genuinely report account health and SHOULD keep counting toward upstream_error_rate.
func tkUpstreamClientInducedRejection(c *gin.Context, clientErrType string) bool {
	status := tkOpsUpstreamStatusCode(c)
	// 413 request_too_large is always caller-fault: the body cleared TokenKey's
	// local body-limit middleware (handler.request_body_limit) but exceeded the
	// upstream's own cap, so reaching the account at all is a caller mistake.
	if status == 413 {
		return true
	}
	// TK (prod P0 2026-06-06, edge us5): an upstream 404 model-not-found is
	// caller-fault — the client asked for a model name that does not exist (e.g.
	// the bare alias "opus" on an empty-mapping passthrough account, forwarded and
	// rejected by Anthropic with not_found_error). The gateway now returns a 400
	// "Unsupported model" to the client (handleErrorResponse), but the captured
	// upstream status is still 404, so it must be classified client-owned HERE or
	// it keeps counting toward upstream_error_rate (the us5 P0 driver: 5×502 over a
	// tiny SLA denominator = 100%). Mirrors the 400 invalid_request case below.
	if status == 404 {
		body, msg := tkOpsUpstreamErrorText(c)
		combined := strings.ToLower(strings.TrimSpace(msg + "\n" + body))
		// Account-health 4xx phrases (org disabled / credit balance / identity)
		// keep provider ownership even when wrapped in a 404 envelope.
		if tkOpsIsAccountLevel4xx(combined) {
			return false
		}
		// Positive client-facing signal: the gateway already translated this
		// upstream 404 into a not_found_error for the caller (openai
		// service.handleErrorResponse case 404). A 404 is structurally
		// model/endpoint-not-found — the caller asked for something that does not
		// exist — so own it to the client even when the upstream returned NO error
		// body for the predicates below to re-confirm. (The anthropic
		// Unsupported-model path emits invalid_request_error, NOT not_found_error,
		// and is already owned via the IsAnthropicModelNotFound404 body predicate
		// below — it does not use this branch; this branch is the openai
		// /v1/responses empty-body case where that predicate sees "".)
		// Prod us3 2026-06-17 (upstream_error_rate=97% false P0): a
		// client hammered gpt-5.5-pro on /v1/responses against a ChatGPT-OAuth
		// (Codex) account that cannot serve it; the ChatGPT backend answered a bare
		// 404 with an empty body, so combined=="" and — under the old
		// `combined=="" -> return false` — every one of the 1604 requests defaulted
		// to phase=upstream / error_owner=provider. The client-facing type is the
		// drift-proof signal here: a masked 404 (ShouldHandleErrorCode=false ->
		// "upstream_error", or a passthrough rule) is NOT not_found_error and stays
		// provider, preserving the "generic 404 stays provider" contract.
		if clientErrType == "not_found_error" {
			return true
		}
		if combined == "" {
			return false
		}
		// Reuse the SAME predicates the gateway uses so this metric classification
		// can never drift from the client-facing decision: anthropic 404 (B-2,
		// service.handleErrorResponse "Unsupported model"); newapi/openai-compat 404
		// model-not-found (VolcEngine InvalidEndpointOrModel.NotFound — an
		// un-activated / retired model on a fifth-platform channel; 2026-06-10 false
		// P0). The newapi upstream status now reaches here via the bridge's
		// service.TkRecordBridgeUpstreamError (text/embedding/image/responses relays).
		return service.IsAnthropicModelNotFound404([]byte(body), msg) ||
			service.IsOpenAICompatModelNotFound404([]byte(body), msg)
	}
	// TK (PR #691 cc-only canary prep): a 403 carrying TokenKey's own
	// canonical-ingress rejection phrase is a downstream strict-mode edge
	// rejecting the END CLIENT's identity — caller-fault, not provider health.
	// Without this, a canary edge's strict rejections relayed through the prod
	// mirror stub would count toward upstream_error_rate and could fire the
	// provider-health P0 alert. Matches ONLY the TokenKey phrase; genuine
	// Anthropic 403s (org disabled, bot challenge) stay provider-owned below.
	if status == 403 {
		body, msg := tkOpsUpstreamErrorText(c)
		return service.IsCanonicalIngressUARejectMessage([]byte(body), msg)
	}
	// Only request-validation 4xx are caller-fault. 401 (and non-TK 403) and any
	// 5xx stay provider-owned (account auth / availability / genuine upstream
	// failure); 404 model-not-found is handled as caller-fault just above.
	if status != 400 && status != 422 {
		return false
	}
	body, msg := tkOpsUpstreamErrorText(c)
	combined := strings.ToLower(strings.TrimSpace(msg + "\n" + body))
	if combined == "" {
		return false
	}
	if tkOpsIsAccountLevel4xx(combined) {
		return false
	}
	// Structured signal first: the upstream error envelope type.
	if et := strings.ToLower(strings.TrimSpace(gjson.Get(body, "error.type").String())); et == "invalid_request_error" || et == "request_too_large" {
		return true
	}
	// Substring fallback for shapes where the structured type is wrapped or lost
	// (e.g. OpenAI /v1/responses surfaces an "upstream_error" envelope while the
	// real invalid_request_error message survives only in the upstream message).
	return strings.Contains(combined, "invalid_request_error") ||
		strings.Contains(combined, "request_too_large") ||
		strings.Contains(combined, "request too large") ||
		strings.Contains(combined, "is not supported when using")
}

// tkOpsIsAccountLevel4xx matches the upstream 4xx phrases that report account
// health rather than a caller mistake. These keep error_owner=provider so they
// still drive upstream_error_rate / the capacity alert.
func tkOpsIsAccountLevel4xx(lower string) bool {
	return strings.Contains(lower, "organization has been disabled") ||
		strings.Contains(lower, "organization disabled") ||
		strings.Contains(lower, "credit balance") ||
		strings.Contains(lower, "identity verification") ||
		strings.Contains(lower, "verify your identity")
}

// tkOpsUpstreamStatusCode returns the upstream HTTP status captured on the
// context (single-field key first, then the most recent upstream error event),
// or 0 when none is present.
func tkOpsUpstreamStatusCode(c *gin.Context) int {
	if c == nil {
		return 0
	}
	if v, ok := c.Get(service.OpsUpstreamStatusCodeKey); ok {
		switch t := v.(type) {
		case int:
			if t > 0 {
				return t
			}
		case int64:
			if t > 0 {
				return int(t)
			}
		}
	}
	if v, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*service.OpsUpstreamErrorEvent); ok {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i] != nil && events[i].UpstreamStatusCode > 0 {
					return events[i].UpstreamStatusCode
				}
			}
		}
	}
	return 0
}

// tkOpsUpstreamErrorText gathers the upstream error body and message captured on
// the context. The single-field message key is preferred for msg; the most recent
// upstream error event supplies the response body (and a message fallback).
func tkOpsUpstreamErrorText(c *gin.Context) (body string, msg string) {
	if c == nil {
		return "", ""
	}
	if v, ok := c.Get(service.OpsUpstreamErrorMessageKey); ok {
		if s, ok := v.(string); ok {
			msg = s
		}
	}
	if v, ok := c.Get(service.OpsUpstreamErrorsKey); ok {
		if events, ok := v.([]*service.OpsUpstreamErrorEvent); ok {
			for i := len(events) - 1; i >= 0; i-- {
				if events[i] == nil {
					continue
				}
				if body == "" && strings.TrimSpace(events[i].UpstreamResponseBody) != "" {
					body = events[i].UpstreamResponseBody
				}
				// TK (us7 P0 2026-06-13, upstream_error_rate=48.57% false page): the
				// Anthropic forward path (gateway_service.handleErrorResponse /
				// handleRetryExhaustedError) records the upstream error body in
				// Detail, NOT UpstreamResponseBody. Anthropic's availability-gating
				// 404 ("Claude Fable 5 is not available. Please use Opus 4.8",
				// envelope error.type=not_found_error) therefore reached this
				// classifier with only the human Message — no error.type signal — so
				// IsAnthropicModelNotFound404 returned false, the 404 was owned to the
				// provider, and it counted toward upstream_error_rate. The gateway
				// RESPONSE path matched fine because it holds the full body locally;
				// this is the metric-layer half of the same predicate, and it must
				// read the same body to "never drift". Detail carries the identical
				// truncated body (gateway.log_upstream_error_body, default on), so
				// fall back to it when the dedicated field was not populated.
				if body == "" && strings.TrimSpace(events[i].Detail) != "" {
					body = events[i].Detail
				}
				if msg == "" && strings.TrimSpace(events[i].Message) != "" {
					msg = events[i].Message
				}
				if body != "" && msg != "" {
					break
				}
			}
		}
	}
	return body, msg
}
