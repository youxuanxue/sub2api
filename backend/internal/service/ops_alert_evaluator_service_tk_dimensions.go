package service

import (
	"context"
	"strings"
	"time"
)

// tkAttachAlertCardDimensions merges the first-screen self-diagnosing breakdown
// into ops alert dimensions (user-visible failure dimensions, else top_cause*).
// Best-effort — never blocks firing. See ops_alert_top_cause_tk.go for the
// compute helpers.
func (s *OpsAlertEvaluatorService) tkAttachAlertCardDimensions(
	ctx context.Context,
	rule *OpsAlertRule,
	windowStart, windowEnd time.Time,
	scopePlatform string,
	scopeGroupID *int64,
	dimensions map[string]any,
) map[string]any {
	if extra := s.computeUserVisibleFailureDimensions(ctx, rule, windowStart, windowEnd, scopePlatform, scopeGroupID); len(extra) > 0 {
		if dimensions == nil {
			dimensions = map[string]any{}
		}
		for k, v := range extra {
			if strings.TrimSpace(v) != "" {
				dimensions[k] = v
			}
		}
		return dimensions
	}
	cause, users, models := s.computeTopCause(ctx, rule, windowStart, windowEnd, scopePlatform, scopeGroupID)
	if cause == "" && users == "" && models == "" {
		return dimensions
	}
	if dimensions == nil {
		dimensions = map[string]any{}
	}
	if cause != "" {
		dimensions["top_cause"] = cause
	}
	if users != "" {
		dimensions["top_cause_users"] = users
	}
	if models != "" {
		dimensions["top_cause_models"] = models
	}
	return dimensions
}
