package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// TK (us7 P0 2026-06-13): self-diagnosing P0 alert cause.
//
// A fired error-rate alert (e.g. upstream_error_rate=48.57%) used to carry only
// the aggregate number — an operator had to drill the Ops Dashboard or SSH the
// box to learn WHICH model/account drove the spike (us7 turned out to be a
// client hammering an access-gated claude-fable-5). This computes the top
// offending row(s) over the SAME window/scope the metric used, so the Feishu
// card can answer "is this a real fire or noise?" at a glance.
//
// OpsTopErrorCause is one (model, owner, upstream_status) bucket with its count.
type OpsTopErrorCause struct {
	Model          string
	ErrorOwner     string
	UpstreamStatus int
	Count          int64
}

// OpsRoutingRejectionCause is one platform bucket of routing-phase capacity
// rejections, used to name the empty pool(s) on a
// routing_capacity_rejection_count P0 card.
type OpsRoutingRejectionCause struct {
	Platform string
	Count    int64
}

// OpsRoutingRejectionUser is one (user, api-key) bucket of routing-phase capacity
// rejections, used to name WHO is being rejected on a
// routing_capacity_rejection_count P0 card. APIKeyName is the operator-assigned
// key label (resolved from api_keys.name, or the deleted-key snapshot for a
// hard-deleted key); the key secret/prefix is NEVER surfaced in an alert.
type OpsRoutingRejectionUser struct {
	UserID     int64
	APIKeyName string
	Count      int64
}

// opsTopCauseMetricTypes are the rule metric types for which a top-cause
// breakdown is meaningful (rate metrics over ops_error_logs). Other rule types
// (system gauges, group-availability counts) get no breakdown — keeping the
// card lean and avoiding a needless query.
func opsTopCauseApplies(metricType string) bool {
	switch strings.TrimSpace(metricType) {
	case "upstream_error_rate", "error_rate":
		return true
	default:
		return false
	}
}

// computeTopCause returns a pre-formatted "主因" string for the card, or "" when
// the rule is not a rate metric, the query fails, or there is nothing to show.
// It is best-effort: a failure here must never block firing the alert.
func (s *OpsAlertEvaluatorService) computeTopCause(ctx context.Context, rule *OpsAlertRule, start, end time.Time, platform string, groupID *int64) string {
	if s == nil || rule == nil {
		return ""
	}
	metricType := strings.TrimSpace(rule.MetricType)
	// pool_load_rate 的主因来自实时池负载快照（哪几个调度池在饱和），而非
	// ops_error_logs，所以单独取一支；详见 ops_pool_load_rate_tk.go。
	if metricType == "pool_load_rate" {
		if s.opsService == nil {
			return ""
		}
		pools, err := s.opsService.ComputePoolLoadRates(ctx)
		if err != nil {
			return ""
		}
		return formatPoolLoadCause(pools, rule, platform, groupID)
	}
	if s.opsRepo == nil {
		return ""
	}
	// routing_capacity_rejection_count's cause has TWO dimensions, derived from the
	// routing-phase rows themselves (not the model/owner/upstream_status breakdown
	// the error-rate metrics use): WHICH platform pool(s) ran out of capacity, and
	// WHO got rejected. Together they answer the first on-call question — is this a
	// single user hammering (rate-limit them) or site-wide capacity exhaustion (add
	// accounts)? The top-N user list reveals the concentration on its own. Both
	// sub-queries are best-effort: a failure in one must not drop the other or block
	// the alert.
	if metricType == "routing_capacity_rejection_count" {
		filter := &OpsDashboardFilter{
			StartTime: start,
			EndTime:   end,
			Platform:  platform,
			GroupID:   groupID,
			QueryMode: OpsQueryModeRaw,
		}
		pools, _ := s.opsRepo.TopRoutingCapacityRejectionCauses(ctx, filter, 2)
		users, _ := s.opsRepo.TopRoutingCapacityRejectionUsers(ctx, filter, 3)
		segments := make([]string, 0, 2)
		if pool := formatRoutingRejectionCause(pools); pool != "" {
			segments = append(segments, pool)
		}
		if usr := formatRoutingRejectionUsers(users); usr != "" {
			segments = append(segments, "用户 "+usr)
		}
		return strings.Join(segments, " ｜ ")
	}
	if !opsTopCauseApplies(metricType) {
		return ""
	}
	// upstream_error_rate counts only provider-owned, non-429/529 final failures
	// (the upstream_excl set); error_rate counts all SLA errors. Mirror that so
	// the cause reflects exactly the rows behind the breached metric.
	upstreamOnly := metricType == "upstream_error_rate"
	causes, err := s.opsRepo.GetTopErrorCause(ctx, &OpsDashboardFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  platform,
		GroupID:   groupID,
		QueryMode: OpsQueryModeRaw,
	}, upstreamOnly, 2)
	if err != nil || len(causes) == 0 {
		return ""
	}
	return formatOpsTopCause(causes)
}

// formatOpsTopCause renders up to two causes as a compact one-line string, e.g.
//
//	claude-fable-5 ×34（upstream 404 / provider） · claude-opus-4-8 ×3（upstream 529 / provider）
//
// Kept deliberately to top-2 — the card answers "what" not "everything".
func formatOpsTopCause(causes []*OpsTopErrorCause) string {
	parts := make([]string, 0, 2)
	for _, c := range causes {
		if c == nil {
			continue
		}
		model := strings.TrimSpace(c.Model)
		if model == "" {
			model = "(unknown)"
		}
		owner := strings.TrimSpace(c.ErrorOwner)
		if owner == "" {
			owner = "unknown"
		}
		seg := fmt.Sprintf("%s ×%d", model, c.Count)
		if c.UpstreamStatus > 0 {
			seg += fmt.Sprintf("（upstream %d / %s）", c.UpstreamStatus, owner)
		} else {
			seg += fmt.Sprintf("（%s）", owner)
		}
		parts = append(parts, seg)
		if len(parts) >= 2 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// formatRoutingRejectionCause renders the top routing-rejection platforms as a
// compact one-line cause, e.g. "anthropic ×40 · openai ×15". The on-call reads
// it as "these pools are out of capacity — add accounts / check that edge".
func formatRoutingRejectionCause(causes []*OpsRoutingRejectionCause) string {
	parts := make([]string, 0, 2)
	for _, c := range causes {
		if c == nil || c.Count <= 0 {
			continue
		}
		platform := strings.TrimSpace(c.Platform)
		if platform == "" {
			platform = "(unknown)"
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", platform, c.Count))
		if len(parts) >= 2 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// formatRoutingRejectionUsers renders the top rejected users as a compact
// one-line cause, e.g. `#42 "eval-harness" ×30 · #17 "mobile-app" ×12 · #9 ×8`.
// Shows the internal user id + the operator-assigned api-key NAME (NEVER the key
// secret) so the on-call can tell a single user hammering from a site-wide
// shortage and, if needed, pin the offending key by its label. Capped at 3 — the
// card answers "who", not "everyone".
func formatRoutingRejectionUsers(users []*OpsRoutingRejectionUser) string {
	parts := make([]string, 0, 3)
	for _, u := range users {
		if u == nil || u.Count <= 0 {
			continue
		}
		seg := fmt.Sprintf("#%d", u.UserID)
		if name := strings.TrimSpace(u.APIKeyName); name != "" {
			seg += fmt.Sprintf(" %q", truncateRunes(name, 24))
		}
		seg += fmt.Sprintf(" ×%d", u.Count)
		parts = append(parts, seg)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// truncateRunes shortens s to at most max runes (rune-safe for multibyte key
// names), appending an ellipsis when truncated. Keeps a long key name from
// bloating the alert card's 主因 line.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
