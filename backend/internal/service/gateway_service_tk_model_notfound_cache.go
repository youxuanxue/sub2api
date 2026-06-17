package service

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// TK: Anthropic "model not found" negative cache — stop re-forwarding a model
// name the upstream has already rejected as not-found.
//
// Problem (prod 2026-06-17): a client repeatedly requests a non-existent
// Anthropic model name (e.g. the typo "claude-haiku-4-6"). Each request is
// forwarded all the way to api.anthropic.com, which returns 404
// not_found_error, which TokenKey translates to a 400 "Unsupported model: X".
// The model catalog is GLOBAL per platform, so once the upstream has confirmed
// a name does not exist, re-forwarding the same name is a wasted upstream
// round-trip (and an abuse-detection fingerprint surface) for an answer we
// already know.
//
// Why NOT cool the account (the obvious-but-wrong fix): cooling (account ×
// model) on a client-controlled model name drains a thin pool into "No
// available accounts" 429s — this was prod P0 2026-06-06 and was deliberately
// removed (handle404 skips the penalty). This cache NEVER touches account
// schedulability and NEVER triggers failover; on a hit it only short-circuits
// the forward with the SAME 400 contract the upstream 404 would have produced.
//
// Why a SHORT TTL and not a static allowlist: the key is client-controlled and
// a name that 404s today may be a real model Anthropic ships tomorrow. A 60s
// TTL bounds the staleness — the cache only ever holds names that are CURRENTLY
// 404ing and self-heals within one TTL of a name going live (the next populate
// only happens on a fresh upstream 404). A static allowlist would instead
// reject a newly launched model until someone edits the list; this learns from
// upstream ground truth.
//
// Scope: Anthropic only (other platforms' 404 semantics differ). In-memory,
// per-replica (an optimization cache, not correctness-critical — each replica
// learns independently within the TTL; no Redis, no Wire change). Keyed by
// (platform, post-mapping model name): a 404 not_found_error means the NAME is
// globally unknown, whereas per-account access gating returns 403
// permission_error (NOT 404) — so a platform-wide key is safe because
// IsAnthropicModelNotFound404 only captures the former, never a model that some
// sibling account could actually serve. Complementary to
// tkIsForwardableAnthropicModelName (the cross-vendor guard blocks
// deepseek-/gpt-/gemini- names at account selection, before Forward); this
// cache handles the in-namespace claude-* typos that guard intentionally lets
// through to the upstream.

// tkModelNotFoundNegativeCacheTTL bounds how long a confirmed not-found verdict
// is trusted. Short on purpose (see file doc). Const, not a runtime setting:
// the feature is safe by construction (never cools an account, self-heals in
// <=TTL) so it needs no kill switch.
const tkModelNotFoundNegativeCacheTTL = 60 * time.Second

// tkModelNotFoundNegativeCache is an in-memory, per-replica negative cache
// keyed by (platform, lower(trim(model))) -> expiry time.
type tkModelNotFoundNegativeCache struct {
	m sync.Map // string key -> time.Time (expiry instant)
}

// tkModelNotFoundCacheKey builds the (platform, model) key. Lower/trim the
// model so case/whitespace variants share an entry; empty model -> "" (never a
// hittable key — callers treat it as a miss / no-op).
func tkModelNotFoundCacheKey(platform, model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ""
	}
	return platform + "\x00" + model
}

// get reports whether (platform, model) currently has a live not-found verdict,
// lazily evicting an expired entry. nil-receiver safe.
func (cstore *tkModelNotFoundNegativeCache) get(platform, model string) bool {
	if cstore == nil {
		return false
	}
	key := tkModelNotFoundCacheKey(platform, model)
	if key == "" {
		return false
	}
	v, ok := cstore.m.Load(key)
	if !ok {
		return false
	}
	expiry, _ := v.(time.Time)
	if time.Now().After(expiry) {
		cstore.m.Delete(key)
		return false
	}
	return true
}

// put records/refreshes a not-found verdict with a fresh TTL. nil-receiver safe.
func (cstore *tkModelNotFoundNegativeCache) put(platform, model string) {
	if cstore == nil {
		return
	}
	key := tkModelNotFoundCacheKey(platform, model)
	if key == "" {
		return
	}
	cstore.m.Store(key, time.Now().Add(tkModelNotFoundNegativeCacheTTL))
}

// tkModelNotFoundShortCircuit is the pre-forward gate (read side). On a cache
// hit it writes the SAME 400 "Unsupported model: X" contract the upstream 404
// would have produced and returns (true, err) so Forward() returns early
// WITHOUT contacting the upstream. The error is a plain error — NOT an
// *UpstreamFailoverError (so it never triggers failover) and the written body
// makes the handler's gatewayForwardErrorAlreadyCommunicated suppress any
// double write. Mirrors the deprecated-model gate
// (gateway_anthropic_deprecated_model_tk.go). Anthropic-only; nil-safe on
// cache / c / account.
func (s *GatewayService) tkModelNotFoundShortCircuit(c *gin.Context, account *Account, mappedModel string) (bool, error) {
	if c == nil || account == nil || account.Platform != PlatformAnthropic {
		return false, nil
	}
	if !s.tkModelNotFoundCache.get(account.Platform, mappedModel) {
		return false, nil
	}
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"type": "error",
		"error": gin.H{
			"type":    TkUnsupportedModelErrType,
			"message": TkUnsupportedModelMessage(mappedModel),
		},
	})
	slog.Info("tk_model_notfound_negative_cache_short_circuit",
		"platform", account.Platform,
		"model", mappedModel,
		"account_id", account.ID,
		"ttl", tkModelNotFoundNegativeCacheTTL.String())
	return true, fmt.Errorf("model not found (negative-cache short-circuit): %s", mappedModel)
}

// tkModelNotFoundRecordUpstream404 is the populate side: records a confirmed
// Anthropic upstream model-not-found so subsequent identical requests
// short-circuit. Called from handleErrorResponse's case 404 only after
// IsAnthropicModelNotFound404 has matched (i.e. a real not_found_error, not a
// 403 permission gate). Anthropic-only; nil-safe.
func (s *GatewayService) tkModelNotFoundRecordUpstream404(platform, model string) {
	if platform != PlatformAnthropic || strings.TrimSpace(model) == "" {
		return
	}
	s.tkModelNotFoundCache.put(platform, model)
	slog.Info("tk_model_notfound_negative_cache_populate",
		"platform", platform,
		"model", strings.ToLower(strings.TrimSpace(model)),
		"ttl", tkModelNotFoundNegativeCacheTTL.String())
}
