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

// OpsRoutingRejectionPlatform is one platform bucket of routing-phase capacity
// rejections on a routing_capacity_rejection_count P0 card: the platform whose
// pool ran out of capacity, its total rejection Count (ALL routing rows for the
// platform, including any with no attributable user), and the top contributing
// users nested under it. The nested shape answers, in one line, both "which pool
// is empty" AND "who inside it is driving the rejections" — the platform→user
// attribution two separate marginal queries (platform-only + user-only) could not
// express, because a user spanning two platforms smeared across both margins.
type OpsRoutingRejectionPlatform struct {
	Platform string
	Count    int64
	Users    []*OpsRoutingRejectionUser
}

// OpsRoutingRejectionUser is one (user, api-key) bucket of routing-phase capacity
// rejections nested under a platform. APIKeyName is the operator-assigned key
// label (resolved from api_keys.name, or the deleted-key snapshot for a
// hard-deleted key) so the on-call can pin the offending client; the key
// secret/prefix is NEVER surfaced. The label is user-controlled, so it is
// markdown-defanged before rendering (see sanitizeFeishuLabel).
type OpsRoutingRejectionUser struct {
	UserID     int64
	APIKeyName string
	Count      int64
}

// OpsRoutingRejectionModel is one requested-model bucket for routing-phase
// capacity rejections. It answers the operator question "which requested models
// make up the failed request volume?" on the no-available-accounts P0 card.
type OpsRoutingRejectionModel struct {
	Model string
	Count int64
}

// OpsUserVisibleFailureBreakdown is the first-screen payload for the "real user
// experience degraded" Feishu rules. It keeps the four operator questions
// separate so the card stays scannable: who, impact, user-visible surface, root.
type OpsUserVisibleFailureBreakdown struct {
	Failures  int64
	Successes int64
	Users     []*OpsUserVisibleFailureUser
	Surfaces  []*OpsUserVisibleFailureSurface
	Roots     []*OpsUserVisibleFailureRoot
}

type OpsUserVisibleFailureUser struct {
	UserID     int64
	UserEmail  string
	APIKeyName string
	GroupName  string
	Count      int64
}

type OpsUserVisibleFailureSurface struct {
	StatusCode         int
	UpstreamStatusCode int
	ErrorType          string
	Count              int64
}

type OpsUserVisibleFailureRoot struct {
	Phase     string
	Owner     string
	Platform  string
	Model     string
	AccountID int64
	Message   string
	Count     int64
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

// computeTopCause returns the pre-formatted "主因" string(s) for the card — both
// "" when the rule is not a rate metric, the query fails, or there is nothing to
// show. `cause` is the primary line: the model/owner breakdown (error-rate
// rules), the saturated pool(s) (pool_load_rate), or the empty platform pool(s)
// (routing_capacity_rejection_count). `models` is the failed-request top model
// breakdown for routing_capacity_rejection_count, rendered on its own line under
// 主因. `users` is retained for already-stored historical event dimensions.
// Best-effort: a failure here must never block firing the alert.
//
// This function is node-agnostic. Edge nodes suppress the WHOLE
// routing_capacity_rejection_count alert at the notification layer
// (maybeSendAlertNotifications → isEdgeNode), so there is no per-field edge
// branching here.
func (s *OpsAlertEvaluatorService) computeTopCause(ctx context.Context, rule *OpsAlertRule, start, end time.Time, platform string, groupID *int64) (cause string, users string, models string) {
	if s == nil || rule == nil {
		return "", "", ""
	}
	metricType := strings.TrimSpace(rule.MetricType)
	// pool_load_rate 的主因来自实时池负载快照（哪几个调度池在饱和），而非
	// ops_error_logs，所以单独取一支；详见 ops_pool_load_rate_tk.go。
	if metricType == "pool_load_rate" {
		if s.opsService == nil {
			return "", "", ""
		}
		pools, err := s.opsService.ComputePoolLoadRates(ctx)
		if err != nil {
			return "", "", ""
		}
		return formatPoolLoadCause(pools, rule, platform, groupID), "", ""
	}
	if s.opsRepo == nil {
		return "", "", ""
	}
	// routing_capacity_rejection_count's cause is a single JOINT breakdown over the
	// routing-phase rows themselves (not the model/owner/upstream_status breakdown
	// the error-rate metrics use): the top platform pool(s) that ran out of
	// capacity, each with its top contributing users nested inline (internal user id
	// + api-key name, never the secret). One line answers the first
	// on-call question WITH attribution — is the shortage one user hammering
	// (rate-limit them) or spread across many (add accounts)? — which the old two
	// separate marginal queries (platform-only + user-only) could not, since a user
	// spanning two platforms smeared across both. `users` is left empty (the
	// standalone 用户 line is retired); the notifier keeps its reader for
	// already-stored historical events. Best-effort: a failure must not block the
	// alert.
	if metricType == OpsAlertMetricRoutingCapacityRejectionCount {
		filter := &OpsDashboardFilter{
			StartTime: start,
			EndTime:   end,
			Platform:  platform,
			GroupID:   groupID,
			QueryMode: OpsQueryModeRaw,
		}
		platforms, _ := s.opsRepo.TopRoutingCapacityRejectionByPlatform(ctx, filter, 2, 3)
		modelBuckets, _ := s.opsRepo.TopRoutingCapacityRejectionByModel(ctx, filter, 3)
		return formatRoutingRejectionByPlatform(platforms), "", formatRoutingRejectionByModel(modelBuckets)
	}
	if !opsTopCauseApplies(metricType) {
		return "", "", ""
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
		return "", "", ""
	}
	return formatOpsTopCause(causes), "", ""
}

func userVisibleFailureOwnerScopeForMetric(metricType string) string {
	switch strings.TrimSpace(metricType) {
	case OpsAlertMetricClientVisibleFailureCount:
		return "client"
	default:
		return "system"
	}
}

func (s *OpsAlertEvaluatorService) computeUserVisibleFailureDimensions(ctx context.Context, rule *OpsAlertRule, start, end time.Time, platform string, groupID *int64) map[string]string {
	if s == nil || s.opsRepo == nil || rule == nil {
		return nil
	}
	metricType := strings.TrimSpace(rule.MetricType)
	if metricType != OpsAlertMetricUserVisibleFailureCount && metricType != OpsAlertMetricClientVisibleFailureCount {
		return nil
	}
	breakdown, err := s.opsRepo.GetUserVisibleFailureBreakdown(ctx, &OpsDashboardFilter{
		StartTime: start,
		EndTime:   end,
		Platform:  platform,
		GroupID:   groupID,
		QueryMode: OpsQueryModeRaw,
	}, userVisibleFailureOwnerScopeForMetric(metricType), 3)
	if err != nil || breakdown == nil {
		return nil
	}
	out := map[string]string{}
	if affected := formatUserVisibleFailureAffected(breakdown.Users); affected != "" {
		out["user_visible_affected"] = affected
	}
	if impact := formatUserVisibleFailureImpact(breakdown, start, end); impact != "" {
		out["user_visible_impact"] = impact
	}
	if surface := formatUserVisibleFailureSurface(breakdown.Surfaces); surface != "" {
		out["user_visible_surface"] = surface
	}
	if root := formatUserVisibleFailureRoot(breakdown.Roots); root != "" {
		out["user_visible_root"] = root
	}
	return out
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
		model := sanitizeFeishuModelLabel(c.Model)
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

// formatRoutingRejectionByPlatform renders the joint platform×user breakdown as a
// single compact line, e.g.
//
//	anthropic ×40（#1 "eval-harness" ×30 · #2 "mobile-app" ×10） · openai ×8（#5 "ci-runner" ×8）
//
// Each platform shows its total rejection count and, in full-width parens, its top
// contributing users (internal user id + the operator-assigned api-key NAME, never
// the key secret). A platform with no attributable user renders bare
// ("anthropic ×40"). The on-call reads it as "these pools are out of capacity, and
// within each pool these clients are driving it", telling a single user hammering
// from a site-wide shortage while keeping per-platform attribution. Capped at the
// top 2 platforms (the repo already limits; the cap here is defensive). Returns ""
// when there is nothing to show.
func formatRoutingRejectionByPlatform(platforms []*OpsRoutingRejectionPlatform) string {
	parts := make([]string, 0, 2)
	for _, p := range platforms {
		if p == nil || p.Count <= 0 {
			continue
		}
		name := strings.TrimSpace(p.Platform)
		if name == "" {
			name = "(unknown)"
		}
		seg := fmt.Sprintf("%s ×%d", name, p.Count)
		if nested := formatRejectionUserSegments(p.Users); nested != "" {
			seg += "（" + nested + "）"
		}
		parts = append(parts, seg)
		if len(parts) >= 2 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// formatRejectionUserSegments renders up to 3 nested users joined by " · ". Skips
// nil/non-positive entries; returns "" when none remain.
func formatRejectionUserSegments(users []*OpsRoutingRejectionUser) string {
	parts := make([]string, 0, 3)
	for _, u := range users {
		if u == nil || u.Count <= 0 {
			continue
		}
		parts = append(parts, formatRejectionUserSegment(u))
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

// formatRejectionUserSegment renders one nested user as `#<id> "<name>" ×<n>`,
// falling back to `#<id> ×<n>` when the api-key name is blank. The name is
// user-controlled, so it is markdown-defanged and rune-truncated before it lands
// in the operator's lark_md card.
func formatRejectionUserSegment(u *OpsRoutingRejectionUser) string {
	seg := fmt.Sprintf("#%d", u.UserID)
	if name := sanitizeFeishuLabel(u.APIKeyName); name != "" {
		seg += fmt.Sprintf(" %q", truncateRunes(name, 24))
	}
	seg += fmt.Sprintf(" ×%d", u.Count)
	return seg
}

// formatRoutingRejectionByModel renders up to 3 requested-model buckets as a
// compact line for routing-capacity P0 cards, e.g.
//
//	claude-sonnet-4-5 ×120 · claude-opus-4-8 ×32 · (unknown) ×4
func formatRoutingRejectionByModel(models []*OpsRoutingRejectionModel) string {
	parts := make([]string, 0, 3)
	for _, m := range models {
		if m == nil || m.Count <= 0 {
			continue
		}
		name := sanitizeFeishuModelLabel(m.Model)
		parts = append(parts, fmt.Sprintf("%s ×%d", name, m.Count))
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

func formatUserVisibleFailureAffected(users []*OpsUserVisibleFailureUser) string {
	parts := make([]string, 0, 3)
	for _, u := range users {
		if u == nil || u.Count <= 0 {
			continue
		}
		user := fmt.Sprintf("#%d", u.UserID)
		if email := sanitizeFeishuLabel(u.UserEmail); email != "" {
			user += " " + email
		}
		seg := fmt.Sprintf("%s ×%d", user, u.Count)
		details := make([]string, 0, 2)
		if key := sanitizeFeishuLabel(u.APIKeyName); key != "" {
			details = append(details, `key "`+key+`"`)
		}
		if group := sanitizeFeishuLabel(u.GroupName); group != "" {
			details = append(details, "group "+group)
		}
		if len(details) > 0 {
			seg += "（" + strings.Join(details, " / ") + "）"
		}
		parts = append(parts, seg)
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

func formatUserVisibleFailureImpact(b *OpsUserVisibleFailureBreakdown, start, end time.Time) string {
	if b == nil {
		return ""
	}
	windowMinutes := int(end.Sub(start).Round(time.Minute).Minutes())
	if windowMinutes <= 0 {
		windowMinutes = 1
	}
	total := b.Successes + b.Failures
	rate := 0.0
	if total > 0 {
		rate = float64(b.Failures) * 100 / float64(total)
	}
	return fmt.Sprintf("失败 %d / 成功 %d / 失败率 %.2f%% / %dm", b.Failures, b.Successes, rate, windowMinutes)
}

func formatUserVisibleFailureSurface(surfaces []*OpsUserVisibleFailureSurface) string {
	parts := make([]string, 0, 3)
	for _, s := range surfaces {
		if s == nil || s.Count <= 0 {
			continue
		}
		status := fmt.Sprintf("final %d", s.StatusCode)
		if s.UpstreamStatusCode > 0 {
			status += fmt.Sprintf(" / upstream %d", s.UpstreamStatusCode)
		}
		typ := sanitizeFeishuLabel(s.ErrorType)
		if typ != "" {
			status += " / " + typ
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", status, s.Count))
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

func formatUserVisibleFailureRoot(roots []*OpsUserVisibleFailureRoot) string {
	parts := make([]string, 0, 3)
	for _, r := range roots {
		if r == nil || r.Count <= 0 {
			continue
		}
		phase := sanitizeFeishuLabel(r.Phase)
		owner := sanitizeFeishuLabel(r.Owner)
		platform := sanitizeFeishuLabel(r.Platform)
		model := sanitizeFeishuModelLabel(r.Model)
		if phase == "" {
			phase = "unknown"
		}
		if owner == "" {
			owner = "unknown"
		}
		seg := fmt.Sprintf("%s/%s", phase, owner)
		if platform != "" {
			seg += " / " + platform
		}
		if model != "" && model != "(unknown)" {
			seg += " / " + model
		}
		if r.AccountID > 0 {
			seg += fmt.Sprintf(" / account #%d", r.AccountID)
		}
		if msg := sanitizeFeishuLabel(r.Message); msg != "" {
			seg += " / " + msg
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", seg, r.Count))
		if len(parts) >= 3 {
			break
		}
	}
	return strings.Join(parts, " · ")
}

func sanitizeFeishuModelLabel(model string) string {
	raw := strings.TrimSpace(model)
	if raw == "" || raw == "(unknown)" {
		return "(unknown)"
	}
	name := sanitizeFeishuLabel(model)
	if name == "" {
		return "(unknown)"
	}
	name = strings.ReplaceAll(name, "://", ": / /")
	return truncateRunes(name, 64)
}

// feishuLabelSanitizer defangs lark_md control characters in a user-controlled
// label (the api-key name) before it is rendered in an operator-facing alert
// card. The downstream escapeFeishuText only handles & < >, so without this a
// user could name their key "[free credits](http://evil)" and inject a clickable
// phishing link — or an unbalanced * / _ that bleeds emphasis into the rest of
// the card — into the ops P0 message. Link / emphasis / code / table / newline
// markers are replaced with a space; the name stays recognizable, the markdown is
// neutralized.
var feishuLabelSanitizer = strings.NewReplacer(
	"[", " ", "]", " ", "(", " ", ")", " ", "`", " ",
	"*", " ", "_", " ", "~", " ", "|", " ", "\n", " ", "\r", " ",
)

// sanitizeFeishuLabel neutralizes markdown in a user-controlled card label and
// trims surrounding whitespace. Returns "" for an all-blank/empty name.
func sanitizeFeishuLabel(s string) string {
	return strings.TrimSpace(feishuLabelSanitizer.Replace(s))
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
