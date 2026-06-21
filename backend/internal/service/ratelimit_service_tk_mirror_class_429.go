package service

import (
	"context"
	"log/slog"
	"time"
)

// TK — prod-side per-class 429 propagation + failover for header-less edge
// empty-pool 429s on Anthropic MIRROR (cc-<edge>) accounts.
//
// Topology (prod): client → prod gateway → a prod MIRROR account
// (platform=anthropic, type=apikey, name cc-<edge>, credentials.base_url =
// https://api-<edge>.tokenkey.dev) → an EDGE gateway (Lightsail) → the edge's
// real OAuth account → Anthropic.
//
// Anthropic enforces a per-MODEL-CLASS 5h/7d "unified window". When a class
// (e.g. sonnet) is exhausted on a single-account edge, the EDGE scheduler
// filters that account out for sonnet (account.extra.model_rate_limits
// ["anthropic:class:sonnet"] on the EDGE account) and returns prod a 429 "no
// available accounts". opus/haiku on that same edge still serve 200.
//
// THE BUG this closes: the edge's per-class window is INVISIBLE to prod. The
// prod MIRROR account shows model_rate_limits["anthropic:class:sonnet"]=null,
// so prod (a) keeps sticky-binding sonnet to the mirror, (b) records NO
// per-class cooldown on the mirror, and (c) does NOT fail over to sonnet-healthy
// sibling mirrors (cc-us5/cc-us6). Net: a universal key is 0/N on sonnet while
// healthy sonnet capacity sits idle next door, and shouldClearStickySession
// never clears because the mirror looks schedulable.
//
// Resolution (prod-only inference): the edge's relayed 429 carries NO
// anthropic-ratelimit-* headers and a class-agnostic body ("no available
// accounts"), so prod cannot learn the edge's true per-class reset. Prod
// therefore infers the class from the in-flight requested model (the class prod
// just failed to serve on that mirror) and writes a BOUNDED, self-renewing floor
// cooldown at the SAME class scope the read/filter/sticky-clear stack already
// consumes (anthropic:class:<class>, via SetModelRateLimit). The read side is
// already wired (model_rate_limit.go → tkAnthropicModelClassRateLimitActive;
// gateway_service.go shouldClearStickySession), so this is a write-only change.
//
// Amplifier-safety (the same invariant the skip branch was built for): this
// writes ONLY the class-scoped key — never SetRateLimited (whole-account), never
// the 3/3 ladder. opus/haiku on the same mirror stay schedulable, so a sonnet
// outage on one edge never collapses the whole mirror pool. It is gated on the
// EXISTING sustained saturation threshold (4-in-90s) so a single transient blip
// never cools a class.

const (
	// tkMirrorClassCooldownRewriteFloor caps scheduler-outbox churn: the
	// per-class cooldown is only (re)written when the account's current remaining
	// for that class has dropped below this floor. With a self-renewing ~90s
	// floor this keeps writes to roughly once-per-floor instead of once-per-
	// request while the edge stays dry.
	tkMirrorClassCooldownRewriteFloor = 15 * time.Second
)

// tkTryAnthropicMirrorClassCooldownOnDownstreamEmpty writes a class-scoped
// cooldown on a prod MIRROR account when an edge's header-less empty-pool 429 is
// SUSTAINED for the in-flight model's class. Prod-only inference: the edge
// cannot signal the class today, so we attribute the class prod just failed to
// serve. Bounded self-renewing floor (no authoritative reset exists).
//
// Returns true iff a class cooldown was written. The caller still returns true
// from the skip branch (the ladder / account-level path stays untouched)
// regardless of this result — this is a strictly additive scheduling hint.
//
// It is a no-op (returns false) for: non-Anthropic accounts, a not-yet-sustained
// count (count < threshold, incl. count==0 from an unwired/errored counter), an
// unknown/absent model class (never guess), an already-actively-cooled class
// with material remaining (outbox-churn guard), or a repo write failure.
func (s *RateLimitService) tkTryAnthropicMirrorClassCooldownOnDownstreamEmpty(
	ctx context.Context,
	account *Account,
	saturationCount int64,
	requestedModel string,
) bool {
	if s == nil || s.accountRepo == nil || account == nil {
		return false
	}
	if account.Platform != PlatformAnthropic {
		return false
	}
	// Sustained-only: same gate as the saturation preference. count==0 means the
	// counter is unwired or errored → cannot confirm sustained → do NOT cool.
	if saturationCount < anthropicSaturationThreshold {
		return false
	}
	scopeKey := tkAnthropicModelClassScopeKeyForModel(requestedModel)
	if scopeKey == "" {
		// Unknown/absent class → never guess which class the edge limited.
		return false
	}
	// Outbox-churn guard: skip the rewrite when this class is already actively
	// cooled with material remaining — keeps writes to ~once-per-floor.
	if rem := account.tkAnthropicModelClassRateLimitRemaining(requestedModel); rem > tkMirrorClassCooldownRewriteFloor {
		return false
	}
	resetAt := time.Now().Add(time.Duration(tkAnthropicMirrorClassCooldownSeconds) * time.Second)
	// detail = 模型类，让飞书摘要直接显示被限流的是哪个模型类（上游窗口对 prod 不可见，
	// 故不带窗口标签）。
	s.notifyAccountSchedulingBlocked(account, resetAt, "429_mirror_class_downstream_empty",
		tkAnthropicModelClass(requestedModel))
	if err := s.accountRepo.SetModelRateLimit(ctx, account.ID, scopeKey, resetAt, tkAnthropicModelCooldownReason); err != nil {
		slog.Warn("anthropic_mirror_class_cooldown_failed",
			"account_id", account.ID,
			"scope", scopeKey,
			"error", err)
		return false
	}
	slog.Info("anthropic_mirror_class_rate_limited",
		"account_id", account.ID,
		"scope", scopeKey,
		"requested_model", requestedModel,
		"reset_at", resetAt,
		"reset_in", time.Until(resetAt).Truncate(time.Second),
		"saturation_count", saturationCount)
	return true
}

// tkFirstRequestedModel returns the first requested-model variadic arg, or "".
// The HandleUpstreamError skip branches take requestedModel as a ...string, so
// the in-flight model name may be absent (e.g. a WS fast-path with no model
// context); an empty string then resolves to an unknown class and the mirror
// per-class cooldown safely declines.
func tkFirstRequestedModel(requestedModel []string) string {
	if len(requestedModel) > 0 {
		return requestedModel[0]
	}
	return ""
}
