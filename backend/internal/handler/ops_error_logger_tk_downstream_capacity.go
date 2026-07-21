package handler

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// tkUpstreamDownstreamCapacity reports whether the captured upstream verdict is a
// TokenKey *downstream-capacity* signal rather than provider health: an upstream
// (typically a TokenKey edge reached via a cc-<edge> apikey mirror account) answered
// 429/503/5xx with a body whose text is one of TokenKey's own pool-empty envelopes
// ("no available accounts" / the gateway failover-terminal message).
//
// WHY (mirror-edge metric pollution, 2026-06-06 yace load test): prod relays to the
// edge via apikey mirror accounts (credentials.base_url=api-<edge>.tokenkey.dev);
// when the edge pool is empty it returns 429 with a "No available accounts" body
// (helper tkNoAvailableAccounts, PR #575). That 429 is OUR fleet capacity, not
// Anthropic rate-limiting — but because it carries a definitive upstream status,
// tkUpstreamClientCanceled deliberately skips it and classifyOpsErrorLog counted it
// as phase=upstream, so a dead single-account edge (us3: served_200=0,
// no_available_429=33748) and a healthy edge (us5: 2251x200, 77x429) both read
// ~1300 upstream-429 on prod. Folding this into routingCapacityLimited owns it as
// routing (out of upstream_error_rate) exactly like a LOCAL empty pool, and mirrors
// the cooldown-ladder skip already done in
// ratelimit_service_tk_downstream_no_available.go (slog
// anthropic_downstream_no_available_accounts_skip_penalty /
// anthropic_downstream_failover_exhausted_skip_penalty).
//
// Boundary (anthropic_amplifier_exemption_boundary): ONLY the TokenKey-generated
// phrases match. A real Anthropic 429 (rate_limit_error) or a raw edge-infra 5xx
// carries no such phrase and stays provider-owned, so genuine upstream health still
// counts toward upstream_error_rate.
func tkUpstreamDownstreamCapacity(c *gin.Context) bool {
	if c == nil {
		return false
	}
	// Only a relayed verdict carries a definitive upstream status. 429 is the
	// pool-empty fast-fail (PR #575); 5xx covers the legacy 503 and
	// failover-exhausted envelopes. A status of 0 (pure transport error) is owned by
	// tkUpstreamClientCanceled, not here.
	status := tkOpsUpstreamStatusCode(c)
	if status != 429 && status < 500 {
		return false
	}
	body, msg := tkOpsUpstreamErrorText(c)
	combined := strings.ToLower(strings.TrimSpace(msg + "\n" + body))
	if combined == "" {
		return false
	}
	// isOpsNoAvailableAccountMessage already lowercases; the second check uses the
	// service-owned exact matcher for current and rolling-version failover envelopes.
	return isOpsNoAvailableAccountMessage(combined) ||
		service.IsGatewayFailoverMessage(msg, []byte(body))
}
