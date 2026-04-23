// Package service / sticky_session_context.go
//
// Tiny gin-aware helpers that turn a request context into a fully resolved
// StickyInjectionRequest in 1-3 lines, so wiring sticky injection into an
// existing forwarding path doesn't bloat each call site.
//
// The helpers gracefully degrade when context fields are missing (returns a
// strategy whose AllowsInjection() / AllowsDerivation() can be checked).
package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// stickyGlobalEnabledProvider abstracts SettingService for tests.
type stickyGlobalEnabledProvider interface {
	IsStickyRoutingEnabled(ctx context.Context) bool
}

// resolveStickyStrategyFromGin reads:
//   - the global kill switch from settingService (defaulting to true if nil)
//   - the per-group sticky_routing_mode from c.Get("api_key").(*APIKey).Group
//
// and returns a fully composed StickyStrategy.
func resolveStickyStrategyFromGin(ctx context.Context, c *gin.Context, settingService stickyGlobalEnabledProvider) StickyStrategy {
	enabled := true
	if settingService != nil {
		enabled = settingService.IsStickyRoutingEnabled(ctx)
	}
	mode := StickyModeAuto
	if c != nil {
		if v, ok := c.Get("api_key"); ok {
			if ak, ok := v.(*APIKey); ok && ak != nil && ak.Group != nil {
				if m := strings.TrimSpace(string(ak.Group.StickyRoutingMode)); m != "" {
					mode = StickyMode(m)
				}
			}
		}
	}
	return StickyStrategy{GlobalEnabled: enabled, Mode: mode}
}

// applyStickyToNewAPIChatBridge derives + injects a sticky key into a
// /v1/chat/completions request body (writes prompt_cache_key) AND sets
// X-Session-Id on c.Request.Header so the underlying NewAPI adaptor can
// forward it to GLM-style upstreams.
//
// Returns the (possibly mutated) body. When the strategy disallows injection
// or no sticky key can be derived, the original body is returned untouched.
//
// upstreamModel is optional; when empty, body.model is read for derivation
// context.
//
// Bug B-6: previously a single applyStickyToNewAPIBridge served both
// /chat/completions and /v1/responses, hardcoded to InjectOpenAIChatCompletionsBody.
// Today InjectOpenAIChatCompletionsBody == InjectOpenAIResponsesBody by
// coincidence (both write prompt_cache_key at the body root); any future
// protocol drift in either direction would silently break the wrong endpoint.
// Splitting into two call sites localises the protocol per dispatch path.
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-6.
func applyStickyToNewAPIChatBridge(
	ctx context.Context,
	c *gin.Context,
	settingService stickyGlobalEnabledProvider,
	account *Account,
	body []byte,
	upstreamModel string,
) []byte {
	return applyStickyToNewAPIBridgeWith(ctx, c, settingService, account, body, upstreamModel, InjectOpenAIChatCompletionsBody)
}

// applyStickyToNewAPIResponsesBridge mirrors applyStickyToNewAPIChatBridge
// for the /v1/responses dispatch path, using InjectOpenAIResponsesBody.
// See applyStickyToNewAPIChatBridge for the rationale (Bug B-6).
func applyStickyToNewAPIResponsesBridge(
	ctx context.Context,
	c *gin.Context,
	settingService stickyGlobalEnabledProvider,
	account *Account,
	body []byte,
	upstreamModel string,
) []byte {
	return applyStickyToNewAPIBridgeWith(ctx, c, settingService, account, body, upstreamModel, InjectOpenAIResponsesBody)
}

// stickyBodyInjector is the function shape implemented by
// InjectOpenAIChatCompletionsBody and InjectOpenAIResponsesBody. The local
// alias avoids exposing the choice at every call site.
type stickyBodyInjector func(body []byte, key StickyKey, strategy StickyStrategy) ([]byte, bool, error)

// applyStickyToNewAPIBridgeWith is the shared implementation of the per-
// endpoint sticky helpers. Kept private so the two public wrappers (chat /
// responses) remain the only documented entry points; an injector pointer
// dispatched through a single function is a smaller surface than two near-
// identical copies.
func applyStickyToNewAPIBridgeWith(
	ctx context.Context,
	c *gin.Context,
	settingService stickyGlobalEnabledProvider,
	account *Account,
	body []byte,
	upstreamModel string,
	inject stickyBodyInjector,
) []byte {
	model := strings.TrimSpace(upstreamModel)
	stickyReq := buildStickyInjectionRequestFromGin(ctx, c, settingService, model, StickyAccountNewAPI, false)
	if !stickyReq.Strategy.AllowsInjection() {
		return body
	}
	key := DeriveStickyKey(stickyReq, body)
	if key.Value == "" {
		return body
	}
	if injected, mut, err := inject(body, key, stickyReq.Strategy); err == nil && mut {
		body = injected
	}
	if c != nil && c.Request != nil {
		_ = InjectXSessionIDHeader(c.Request.Header, key, stickyReq.Strategy)
	}
	return body
}

// openAIStickyAccountKind classifies the account flavor for the sticky
// injector.
func openAIStickyAccountKind(a *Account) StickyAccountKind {
	if a == nil {
		return StickyAccountOpenAIOAuth
	}
	if a.Type == AccountTypeAPIKey {
		return StickyAccountOpenAIAPIKey
	}
	return StickyAccountOpenAIOAuth
}

// anthropicStickyAccountKind classifies an Anthropic account.
func anthropicStickyAccountKind(a *Account) StickyAccountKind {
	if a == nil {
		return StickyAccountAnthropicOAuth
	}
	if a.Type == AccountTypeAPIKey {
		return StickyAccountAnthropicAPIKey
	}
	return StickyAccountAnthropicOAuth
}

// buildStickyInjectionRequestFromGin assembles a StickyInjectionRequest
// suitable for any of the OpenAI Responses / Chat Completions / Anthropic
// Messages forwarding paths.
//
// The caller passes the upstream model and account kind it has already
// resolved; everything else is read from the gin context.
func buildStickyInjectionRequestFromGin(
	ctx context.Context,
	c *gin.Context,
	settingService stickyGlobalEnabledProvider,
	upstreamModel string,
	accountKind StickyAccountKind,
	isClaudeCodeUA bool,
) StickyInjectionRequest {
	req := StickyInjectionRequest{
		APIKeyID:       getAPIKeyIDFromContext(c),
		UpstreamModel:  upstreamModel,
		AccountKind:    accountKind,
		IsClaudeCodeUA: isClaudeCodeUA,
		Strategy:       resolveStickyStrategyFromGin(ctx, c, settingService),
	}
	if c != nil {
		if v, ok := c.Get("api_key"); ok {
			if ak, ok := v.(*APIKey); ok && ak != nil && ak.Group != nil {
				req.GroupID = ak.Group.ID
			}
		}
		if c.Request != nil {
			req.Headers = c.Request.Header
		}
	}
	if req.Headers == nil {
		req.Headers = http.Header{}
	}
	return req
}
