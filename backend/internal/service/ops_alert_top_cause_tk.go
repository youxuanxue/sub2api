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
	if s == nil || s.opsRepo == nil || rule == nil {
		return ""
	}
	metricType := strings.TrimSpace(rule.MetricType)
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
