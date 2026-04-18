// Package service / sticky_session_injector.go
//
// Unified upstream sticky-session key derivation and injection.
// Goal: maximize upstream prompt-cache hits by ensuring requests from the
// same logical client session carry a stable identifier the upstream LB /
// cache layer can key on (OpenAI prompt_cache_key, Anthropic metadata.user_id,
// GLM/X-Session-Id, etc.).
//
// See docs/approved/sticky-routing.md for the architectural rationale and
// the policy matrix per upstream.
//
// This file intentionally exposes stateless package-level functions so any
// existing forwarding path can wire it in with 1-3 lines without
// constructor-injection plumbing. The strategy (global on/off + per-group
// mode) is resolved by the caller via ResolveStickyStrategy and passed in as
// part of StickyInjectionRequest.
package service

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/cespare/xxhash/v2"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// StickyMode mirrors the per-group enum stored in DB (see
// backend/ent/schema/group.go::sticky_routing_mode). Re-exported as a typed
// alias so callers don't need to import the ent group package directly when
// they already use service.*.
type StickyMode = group.StickyRoutingMode

const (
	// StickyModeAuto: derive a key when client doesn't send one and inject it
	// into the upstream request. Default.
	StickyModeAuto StickyMode = group.StickyRoutingModeAuto
	// StickyModePassthrough: only forward what the client already sent; never
	// derive nor inject.
	StickyModePassthrough StickyMode = group.StickyRoutingModePassthrough
	// StickyModeOff: do not touch sticky fields at all (clear nothing,
	// inject nothing, derive nothing).
	StickyModeOff StickyMode = group.StickyRoutingModeOff
)

// StickyStrategy is the resolved per-request policy combining the global
// kill-switch and the group-level mode.
type StickyStrategy struct {
	GlobalEnabled bool       // false => effective mode forced to passthrough
	Mode          StickyMode // group-level mode (auto | passthrough | off)
}

// EffectiveMode returns the mode actually in force for this request.
// When the global switch is off, "auto" degrades to "passthrough" (never to
// "off", because we still want to forward client-sent keys).
func (s StickyStrategy) EffectiveMode() StickyMode {
	if !s.GlobalEnabled {
		if s.Mode == StickyModeOff {
			return StickyModeOff
		}
		return StickyModePassthrough
	}
	return s.Mode
}

// AllowsDerivation reports whether the strategy permits the gateway to
// fabricate a sticky key when the client didn't send one.
func (s StickyStrategy) AllowsDerivation() bool {
	return s.EffectiveMode() == StickyModeAuto
}

// AllowsInjection reports whether the strategy permits writing any sticky
// field into the upstream request body or headers.
func (s StickyStrategy) AllowsInjection() bool {
	return s.EffectiveMode() != StickyModeOff
}

// StickyAccountKind enumerates the upstream account flavors so the injector
// can pick the right field/header name. Caller resolves this from the
// account record before invoking.
type StickyAccountKind string

const (
	StickyAccountOpenAIOAuth     StickyAccountKind = "openai_oauth"
	StickyAccountOpenAIAPIKey    StickyAccountKind = "openai_apikey"
	StickyAccountAnthropicOAuth  StickyAccountKind = "anthropic_oauth"
	StickyAccountAnthropicAPIKey StickyAccountKind = "anthropic_apikey"
	StickyAccountGemini          StickyAccountKind = "gemini"
	StickyAccountAntigravity     StickyAccountKind = "antigravity"
	StickyAccountNewAPI          StickyAccountKind = "newapi"
)

// StickyKey is the resolved sticky identifier with provenance for logging.
type StickyKey struct {
	Value  string // already-normalized identifier; empty means "no sticky"
	Source string // see StickyKeySource* constants below
}

// StickyKey provenance tags. Pure metadata; safe to log.
const (
	StickyKeySourceClientSessionID      = "client_session_id"
	StickyKeySourceClientConversationID = "client_conversation_id"
	StickyKeySourceClientMetadataUserID = "client_metadata_user_id"
	StickyKeySourceClientPromptCacheKey = "client_prompt_cache_key"
	StickyKeySourceDerivedContentHash   = "derived_content_hash"
	StickyKeySourceNone                 = ""
)

// StickyDerivedKeyPrefix is prepended to all gateway-derived sticky keys so
// downstream observers (logs, upstream telemetry, debugging tools) can tell
// "tk_" keys apart from client-provided session ids and from existing
// "compat_cc_*" / "compat_cs_*" prefixes used by other modules.
const StickyDerivedKeyPrefix = "tk_"

// stickyDerivedSystemPromptCap caps how much of the system prompt feeds into
// the derived hash. Long-prompt agents won't make hashing slow; the first
// chunk is what defines a logical "task" 99% of the time.
const stickyDerivedSystemPromptCap = 2 * 1024 // 2 KiB

// StickyInjectionRequest is the input bundle for both Derive and Inject*.
// Callers populate only the fields relevant to their forwarding path.
type StickyInjectionRequest struct {
	APIKeyID       int64
	GroupID        int64
	UpstreamModel  string
	AccountKind    StickyAccountKind
	IsClaudeCodeUA bool // true = real Claude Code client; suppresses metadata.user_id rewrite
	Strategy       StickyStrategy
	Headers        http.Header // case-insensitive accessor
}

// ---------------------------------------------------------------------------
// Derivation
// ---------------------------------------------------------------------------

// DeriveStickyKey returns the sticky key the gateway should use for this
// request. It walks a fixed priority order:
//
//  1. headers["session_id"] / headers["conversation_id"]
//  2. body.metadata.user_id (parsed; prefer the inner session_id when present)
//  3. body.prompt_cache_key
//  4. (only if Strategy.AllowsDerivation) hash(api_key_id || system_first_2k || tools_signature)
//
// An empty StickyKey.Value means "no sticky" (caller should not inject).
func DeriveStickyKey(req StickyInjectionRequest, body []byte) StickyKey {
	if !req.Strategy.AllowsInjection() {
		return StickyKey{Source: StickyKeySourceNone}
	}

	if req.Headers != nil {
		if v := strings.TrimSpace(req.Headers.Get("session_id")); v != "" {
			return StickyKey{Value: v, Source: StickyKeySourceClientSessionID}
		}
		if v := strings.TrimSpace(req.Headers.Get("conversation_id")); v != "" {
			return StickyKey{Value: v, Source: StickyKeySourceClientConversationID}
		}
	}

	if len(body) > 0 {
		if uid := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); uid != "" {
			if parsed := ParseMetadataUserID(uid); parsed != nil && parsed.SessionID != "" {
				return StickyKey{Value: parsed.SessionID, Source: StickyKeySourceClientMetadataUserID}
			}
			return StickyKey{Value: uid, Source: StickyKeySourceClientMetadataUserID}
		}
		if v := strings.TrimSpace(gjson.GetBytes(body, "prompt_cache_key").String()); v != "" {
			return StickyKey{Value: v, Source: StickyKeySourceClientPromptCacheKey}
		}
	}

	if !req.Strategy.AllowsDerivation() {
		return StickyKey{Source: StickyKeySourceNone}
	}

	derived := deriveStickyContentHash(req, body)
	if derived == "" {
		return StickyKey{Source: StickyKeySourceNone}
	}
	return StickyKey{Value: derived, Source: StickyKeySourceDerivedContentHash}
}

// deriveStickyContentHash builds a stable identifier from
// (api_key_id, system_first_2k, tools_signature). It deliberately does NOT
// include the user message, so multi-turn requests within the same task
// hash to the same key.
//
// Output: "tk_" + 16-hex (xxHash64). Same shape as DeriveSessionHashFromSeed
// to make logs and Redis keys uniform.
func deriveStickyContentHash(req StickyInjectionRequest, body []byte) string {
	var sb strings.Builder
	sb.Grow(stickyDerivedSystemPromptCap + 256)
	fmt.Fprintf(&sb, "ak=%d|m=%s|", req.APIKeyID, strings.TrimSpace(req.UpstreamModel))

	if len(body) > 0 {
		// system: try Anthropic Messages "system" (string or array), then OpenAI
		// "system" message in messages[], then "instructions" (Responses API).
		sys := extractSystemPromptForSticky(body)
		if sys != "" {
			if len(sys) > stickyDerivedSystemPromptCap {
				sys = sys[:stickyDerivedSystemPromptCap]
			}
			_, _ = sb.WriteString("sys=")
			_, _ = sb.WriteString(sys)
			_ = sb.WriteByte('|')
		}

		// tools signature: name+description only (avoid full schema → too volatile).
		toolsSig := extractToolsSignatureForSticky(body)
		if toolsSig != "" {
			_, _ = sb.WriteString("tools=")
			_, _ = sb.WriteString(toolsSig)
			_ = sb.WriteByte('|')
		}
	}

	if sb.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("%s%016x", StickyDerivedKeyPrefix, xxhash.Sum64String(sb.String()))
}

// extractSystemPromptForSticky pulls the "system" prompt out of a request
// body across the three protocols we forward (OpenAI Chat Completions,
// OpenAI Responses, Anthropic Messages). Returns "" when none present.
func extractSystemPromptForSticky(body []byte) string {
	// Anthropic Messages: top-level "system" (string or array of content blocks)
	if v := gjson.GetBytes(body, "system"); v.Exists() {
		switch v.Type {
		case gjson.String:
			return v.String()
		case gjson.JSON:
			// array of {type, text}
			var sb strings.Builder
			v.ForEach(func(_, item gjson.Result) bool {
				if t := item.Get("text"); t.Exists() {
					_, _ = sb.WriteString(t.String())
					_ = sb.WriteByte('\n')
				}
				return true
			})
			if s := sb.String(); s != "" {
				return s
			}
		}
	}
	// OpenAI Responses API: top-level "instructions"
	if v := gjson.GetBytes(body, "instructions"); v.Exists() && v.Type == gjson.String {
		if s := v.String(); s != "" {
			return s
		}
	}
	// OpenAI Chat Completions: messages[].role == "system"
	if v := gjson.GetBytes(body, `messages.#(role="system").content`); v.Exists() {
		switch v.Type {
		case gjson.String:
			return v.String()
		case gjson.JSON:
			var sb strings.Builder
			v.ForEach(func(_, item gjson.Result) bool {
				if t := item.Get("text"); t.Exists() {
					_, _ = sb.WriteString(t.String())
					_ = sb.WriteByte('\n')
				}
				return true
			})
			return sb.String()
		}
	}
	return ""
}

// extractToolsSignatureForSticky returns a deterministic short fingerprint
// of the tools array: "name1|name2|...|nameN" sorted lexicographically.
// This avoids hashing volatile schema fields while still letting different
// tool sets fall into different cache buckets.
func extractToolsSignatureForSticky(body []byte) string {
	tools := gjson.GetBytes(body, "tools")
	if !tools.Exists() || tools.Type != gjson.JSON {
		return ""
	}
	var names []string
	tools.ForEach(func(_, t gjson.Result) bool {
		// Anthropic shape: tools[].name
		if n := t.Get("name"); n.Exists() && n.String() != "" {
			names = append(names, n.String())
			return true
		}
		// OpenAI shape: tools[].function.name
		if n := t.Get("function.name"); n.Exists() && n.String() != "" {
			names = append(names, n.String())
		}
		return true
	})
	if len(names) == 0 {
		return ""
	}
	// Sort in-place; small N (typically <30), no perf concern.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j-1] > names[j]; j-- {
			names[j-1], names[j] = names[j], names[j-1]
		}
	}
	return strings.Join(names, "|")
}

// ---------------------------------------------------------------------------
// Injection
// ---------------------------------------------------------------------------

// InjectOpenAIResponsesBody writes prompt_cache_key into the body if
// (a) the strategy permits injection, (b) the body doesn't already have one,
// (c) the key is non-empty. Returns the (possibly mutated) body. Original
// slice is not modified when no change is needed.
func InjectOpenAIResponsesBody(body []byte, key StickyKey, strategy StickyStrategy) ([]byte, bool, error) {
	if !strategy.AllowsInjection() || key.Value == "" || len(body) == 0 {
		return body, false, nil
	}
	if existing := gjson.GetBytes(body, "prompt_cache_key"); existing.Exists() && strings.TrimSpace(existing.String()) != "" {
		return body, false, nil
	}
	out, err := sjson.SetBytes(body, "prompt_cache_key", key.Value)
	if err != nil {
		return body, false, err
	}
	return out, true, nil
}

// InjectOpenAIChatCompletionsBody is currently identical to the Responses
// shape (chat-completions also accepts prompt_cache_key). Kept as a separate
// function so future protocol drift is local.
func InjectOpenAIChatCompletionsBody(body []byte, key StickyKey, strategy StickyStrategy) ([]byte, bool, error) {
	return InjectOpenAIResponsesBody(body, key, strategy)
}

// InjectAnthropicMessagesBody writes metadata.user_id into the body when
// allowed. For real Claude Code clients (req.IsClaudeCodeUA == true) it is
// a no-op: the client owns its session identity and we must not race it.
//
// When the metadata field is absent we add `{"user_id":"<value>"}`; when it
// exists but lacks user_id we patch in user_id; when user_id is already
// present we leave it.
func InjectAnthropicMessagesBody(body []byte, key StickyKey, req StickyInjectionRequest) ([]byte, bool, error) {
	if !req.Strategy.AllowsInjection() || key.Value == "" || len(body) == 0 {
		return body, false, nil
	}
	if req.IsClaudeCodeUA {
		return body, false, nil
	}
	if existing := gjson.GetBytes(body, "metadata.user_id"); existing.Exists() && strings.TrimSpace(existing.String()) != "" {
		return body, false, nil
	}
	out, err := sjson.SetBytes(body, "metadata.user_id", key.Value)
	if err != nil {
		return body, false, err
	}
	return out, true, nil
}

// InjectXSessionIDHeader sets X-Session-Id on the upstream request headers
// (used by GLM-style upstreams via newapi adaptors). It does NOT overwrite an
// existing value provided by the client.
func InjectXSessionIDHeader(headers http.Header, key StickyKey, strategy StickyStrategy) bool {
	if !strategy.AllowsInjection() || key.Value == "" || headers == nil {
		return false
	}
	if existing := strings.TrimSpace(headers.Get("X-Session-Id")); existing != "" {
		return false
	}
	headers.Set("X-Session-Id", key.Value)
	return true
}
