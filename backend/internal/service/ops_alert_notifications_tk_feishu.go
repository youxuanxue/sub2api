package service

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

type opsAlertNotificationResult struct {
	EmailSent  bool
	FeishuSent bool
}

type opsFeishuNotificationState struct {
	mu              sync.Mutex
	limiter         *slidingWindowLimiter
	recoveryLimiter *slidingWindowLimiter
	notifier        *opsFeishuNotifier
	sentAt          map[string]time.Time
	// firedFeishuEventIDs records alert events whose P0 firing card was actually
	// delivered, so auto-resolve can send a paired green recovery card.
	firedFeishuEventIDs map[int64]struct{}
}

func newOpsFeishuNotificationState() *opsFeishuNotificationState {
	return &opsFeishuNotificationState{
		limiter:             newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour),
		recoveryLimiter:     newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour),
		notifier:            newOpsFeishuNotifier(),
		sentAt:              map[string]time.Time{},
		firedFeishuEventIDs: map[int64]struct{}{},
	}
}

func (s *OpsAlertEvaluatorService) maybeSendAlertNotifications(ctx context.Context, runtimeCfg *OpsAlertRuntimeSettings, rule *OpsAlertRule, event *OpsAlertEvent) opsAlertNotificationResult {
	result := opsAlertNotificationResult{}
	if s == nil {
		return result
	}
	// Edge nodes are prod→edge mirror-relay targets, so a routing_capacity_rejection
	// P0 there is just the prod relay being turned away — client-invisible (prod
	// fails over to another edge), and the relay-探测 failover smears it across every
	// thin edge it probes. Edge-local diagnostics are still persisted for the edge
	// dashboard / scan-edge-health trail, but human paging is centralized on prod:
	// prod is the relay terminus where the thin-pool-race blind spot is
	// client-visible and where platform-pool exhaustion Feishu cards are allowed to
	// page. So this rule is pure Feishu noise on an edge: suppress its notifications
	// there.
	if s.isEdgeNode() && isEdgeSuppressedAlertRule(rule) {
		return result
	}
	if s.maybeSendAlertEmail(ctx, runtimeCfg, rule, event) {
		result.EmailSent = true
	}
	if s.maybeSendAlertFeishu(ctx, runtimeCfg, rule, event) {
		result.FeishuSent = true
	}
	return result
}

// isEdgeSuppressedAlertRule reports whether a rule's notifications are pure noise
// on a prod→edge mirror-relay edge and should be silenced there. Real-user
// experience rules (P0 user_visible + P1 client_visible) are prod-only; edge
// local traffic must not page. Most prod-only rules are skipped earlier by the
// evaluator; this predicate is the defensive notification-layer gate for any
// event created by an older binary or manual path.
func isEdgeSuppressedAlertRule(rule *OpsAlertRule) bool {
	if rule == nil {
		return false
	}
	switch strings.TrimSpace(rule.MetricType) {
	case OpsAlertMetricRoutingCapacityRejectionCount,
		OpsAlertMetricUserVisibleFailureCount,
		OpsAlertMetricClientVisibleFailureCount:
		return true
	default:
		return false
	}
}

// isEdgeNode reports whether this node is a prod→edge mirror-relay edge
// (api-<id>.<domain>), derived from the configured frontend URL — the SAME source
// the alert card's node label uses (deriveOpsNodeIdentity / siteFromFrontendURL),
// so a card that would be titled "· us6" is exactly what this classifies as edge.
// Prod (api.<domain>), an empty/unparseable URL, and any non-edge custom host are
// treated as non-edge, so notifications are suppressed only where we positively
// identify a relay edge.
func (s *OpsAlertEvaluatorService) isEdgeNode() bool {
	if s == nil || s.cfg == nil {
		return false
	}
	return strings.HasPrefix(siteFromFrontendURL(s.cfg.Server.FrontendURL), "edge-")
}

func (s *OpsAlertEvaluatorService) maybeSendAlertFeishu(ctx context.Context, runtimeCfg *OpsAlertRuntimeSettings, rule *OpsAlertRule, event *OpsAlertEvent) bool {
	if s == nil || s.opsService == nil || rule == nil || event == nil {
		return false
	}
	if !shouldSendOpsAlertToFeishu(rule, event) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, "skip_severity_or_status", "", nil)
		return false
	}
	cfg, err := s.opsService.GetEmailNotificationConfig(ctx)
	if err != nil {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, OpsAlertFeishuStatusFailed, err.Error(), nil)
		return false
	}
	if cfg == nil || !cfg.Feishu.Enabled {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, "skip_config_disabled", "", nil)
		return false
	}
	if strings.TrimSpace(cfg.Feishu.WebhookURL) == "" {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, "skip_webhook_missing", "", nil)
		return false
	}
	if runtimeCfg != nil && runtimeCfg.Silencing.Enabled {
		if isOpsAlertSilenced(time.Now().UTC(), rule, event, runtimeCfg.Silencing) {
			s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, "skip_silenced", "", nil)
			return false
		}
	}
	state := s.feishuState
	if state == nil {
		state = newOpsFeishuNotificationState()
		s.feishuState = state
	}
	if !state.shouldSend(rule, event, cfg.Feishu) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, "skip_cooldown_or_rate_limited", "", nil)
		return false
	}
	notifier := state.notifier
	if notifier == nil {
		notifier = newOpsFeishuNotifier()
	}
	// Per-node public base URL → card node label + ops dashboard deep-link.
	// Every node posts to the same Feishu group, so this is what tells prod
	// apart from each edge. Empty when frontend_url is unset (graceful fallback
	// to "overall" / no link in deriveOpsNodeIdentity).
	frontendURL := ""
	if s.cfg != nil {
		frontendURL = s.cfg.Server.FrontendURL
	}
	if err := notifier.sendAlert(ctx, cfg.Feishu, frontendURL, rule, event); err != nil {
		errMessage := sanitizeFeishuWebhookError(err, cfg.Feishu.WebhookURL)
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, false, OpsAlertFeishuStatusFailed, errMessage, nil)
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] feishu alert send failed event_id=%d rule_id=%d error=%s", event.ID, rule.ID, errMessage)
		return false
	}
	sentAt := time.Now().UTC()
	s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseFiring, true, OpsAlertFeishuStatusSent, "", &sentAt)
	event.FeishuFiringSent = true
	event.FeishuFiringSentAt = &sentAt
	event.FeishuFiringStatus = OpsAlertFeishuStatusSent
	event.FeishuFiringError = ""
	state.markSent(rule, event)
	state.markFiringDelivered(event)
	return true
}

func (s *OpsAlertEvaluatorService) maybeSendAlertFeishuRecovery(ctx context.Context, runtimeCfg *OpsAlertRuntimeSettings, rule *OpsAlertRule, event *OpsAlertEvent, currentMetricValue float64) bool {
	if s == nil || s.opsService == nil || rule == nil || event == nil {
		return false
	}
	if s.isEdgeNode() && isEdgeSuppressedAlertRule(rule) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_severity_or_status", "", nil)
		return false
	}
	if !shouldSendOpsAlertFeishuRecovery(rule, event) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_severity_or_status", "", nil)
		return false
	}
	cfg, err := s.opsService.GetEmailNotificationConfig(ctx)
	if err != nil {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, OpsAlertFeishuStatusFailed, err.Error(), nil)
		return false
	}
	if cfg == nil || !cfg.Feishu.Enabled {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_config_disabled", "", nil)
		return false
	}
	if strings.TrimSpace(cfg.Feishu.WebhookURL) == "" {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_webhook_missing", "", nil)
		return false
	}
	if runtimeCfg != nil && runtimeCfg.Silencing.Enabled {
		if isOpsAlertSilenced(time.Now().UTC(), rule, event, runtimeCfg.Silencing) {
			s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_silenced", "", nil)
			return false
		}
	}
	state := s.feishuState
	if state == nil {
		state = newOpsFeishuNotificationState()
		s.feishuState = state
	}
	if !state.hasFiringDelivered(event) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_no_firing_card", "", nil)
		return false
	}
	if !state.shouldSendRecovery(rule, event, cfg.Feishu) {
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, "skip_cooldown_or_rate_limited", "", nil)
		return false
	}
	notifier := state.notifier
	if notifier == nil {
		notifier = newOpsFeishuNotifier()
	}
	frontendURL := ""
	if s.cfg != nil {
		frontendURL = s.cfg.Server.FrontendURL
	}
	current := currentMetricValue
	if err := notifier.sendRecovery(ctx, cfg.Feishu, frontendURL, rule, event, &current); err != nil {
		errMessage := sanitizeFeishuWebhookError(err, cfg.Feishu.WebhookURL)
		s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, false, OpsAlertFeishuStatusFailed, errMessage, nil)
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] feishu recovery send failed event_id=%d rule_id=%d error=%s", event.ID, rule.ID, errMessage)
		return false
	}
	sentAt := time.Now().UTC()
	s.recordAlertFeishuDelivery(ctx, event, OpsAlertFeishuPhaseRecovery, true, OpsAlertFeishuStatusSent, "", &sentAt)
	event.FeishuRecoverySent = true
	event.FeishuRecoverySentAt = &sentAt
	event.FeishuRecoveryStatus = OpsAlertFeishuStatusSent
	event.FeishuRecoveryError = ""
	state.markRecoverySent(rule, event)
	return true
}

func (s *OpsAlertEvaluatorService) recordAlertFeishuDelivery(ctx context.Context, event *OpsAlertEvent, phase string, sent bool, status string, errMessage string, sentAt *time.Time) {
	if s == nil || s.opsService == nil || s.opsService.opsRepo == nil || event == nil || event.ID <= 0 {
		return
	}
	status = truncateString(strings.TrimSpace(status), 128)
	if status == "" {
		return
	}
	errMessage = truncateString(strings.TrimSpace(errMessage), 2048)
	if err := s.opsService.UpdateAlertEventFeishuDelivery(ctx, event.ID, phase, sent, status, errMessage, sentAt); err != nil {
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] feishu delivery status update failed event_id=%d phase=%s status=%s error=%s", event.ID, phase, status, err.Error())
	}
}

func shouldSendOpsAlertToFeishu(rule *OpsAlertRule, event *OpsAlertEvent) bool {
	return rule != nil && event != nil &&
		strings.EqualFold(strings.TrimSpace(event.Status), OpsAlertStatusFiring) &&
		opsAlertFeishuSeverityAllowed(rule, event) &&
		rule.NotifyEmail
}

func shouldSendOpsAlertFeishuRecovery(rule *OpsAlertRule, event *OpsAlertEvent) bool {
	if rule == nil || event == nil {
		return false
	}
	status := strings.TrimSpace(event.Status)
	if !strings.EqualFold(status, OpsAlertStatusResolved) && !strings.EqualFold(status, OpsAlertStatusManualResolved) {
		return false
	}
	return opsAlertFeishuSeverityAllowed(rule, event) &&
		rule.NotifyEmail
}

func opsAlertFeishuSeverityAllowed(rule *OpsAlertRule, event *OpsAlertEvent) bool {
	if rule == nil || event == nil {
		return false
	}
	ruleSeverity := strings.ToUpper(strings.TrimSpace(rule.Severity))
	eventSeverity := strings.ToUpper(strings.TrimSpace(event.Severity))
	if ruleSeverity == "P0" && eventSeverity == "P0" {
		return true
	}
	return ruleSeverity == "P1" &&
		eventSeverity == "P1" &&
		strings.TrimSpace(rule.MetricType) == OpsAlertMetricClientVisibleFailureCount
}

func (s *opsFeishuNotificationState) shouldSend(rule *OpsAlertRule, event *OpsAlertEvent, cfg OpsFeishuAlertConfig) bool {
	if s == nil || rule == nil || event == nil {
		return false
	}
	if event.FeishuFiringSent {
		return false
	}
	now := time.Now().UTC()
	cooldown := time.Duration(cfg.CooldownSeconds) * time.Second
	if cooldown <= 0 {
		cooldown = time.Duration(opsFeishuAlertCooldownSecondsDefault) * time.Second
	}
	key := opsFeishuDedupeKey(rule, event)
	s.mu.Lock()
	last, seen := s.sentAt[key]
	if seen && now.Sub(last) < cooldown {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()
	if opsFeishuBypassFiringRateLimit(rule, event) {
		return true
	}
	if s.limiter == nil {
		s.limiter = newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour)
	}
	s.limiter.SetLimit(cfg.RateLimitPerHour)
	return s.limiter.Allow(now)
}

func opsFeishuBypassFiringRateLimit(rule *OpsAlertRule, event *OpsAlertEvent) bool {
	if rule == nil || event == nil {
		return false
	}
	return strings.TrimSpace(rule.MetricType) == OpsAlertMetricUserVisibleFailureCount &&
		strings.EqualFold(strings.TrimSpace(rule.Severity), "P0") &&
		strings.EqualFold(strings.TrimSpace(event.Severity), "P0") &&
		strings.EqualFold(strings.TrimSpace(event.Status), OpsAlertStatusFiring)
}

func (s *opsFeishuNotificationState) markSent(rule *OpsAlertRule, event *OpsAlertEvent) {
	if s == nil || rule == nil || event == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentAt == nil {
		s.sentAt = map[string]time.Time{}
	}
	s.sentAt[opsFeishuDedupeKey(rule, event)] = time.Now().UTC()
}

func (s *opsFeishuNotificationState) markFiringDelivered(event *OpsAlertEvent) {
	if s == nil || event == nil || event.ID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firedFeishuEventIDs == nil {
		s.firedFeishuEventIDs = map[int64]struct{}{}
	}
	s.firedFeishuEventIDs[event.ID] = struct{}{}
}

func (s *opsFeishuNotificationState) shouldSendRecovery(rule *OpsAlertRule, event *OpsAlertEvent, cfg OpsFeishuAlertConfig) bool {
	if s == nil || rule == nil || event == nil || event.ID <= 0 {
		return false
	}
	if event.FeishuRecoverySent {
		return false
	}
	s.mu.Lock()
	_, paired := s.firedFeishuEventIDs[event.ID]
	paired = paired || event.FeishuFiringSent
	if !paired {
		s.mu.Unlock()
		return false
	}
	key := opsFeishuRecoveryDedupeKey(rule, event)
	_, seen := s.sentAt[key]
	s.mu.Unlock()
	if seen {
		return false
	}
	if s.recoveryLimiter == nil {
		s.recoveryLimiter = newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour)
	}
	s.recoveryLimiter.SetLimit(cfg.RateLimitPerHour)
	return s.recoveryLimiter.Allow(time.Now().UTC())
}

func (s *opsFeishuNotificationState) hasFiringDelivered(event *OpsAlertEvent) bool {
	if s == nil || event == nil || event.ID <= 0 {
		return false
	}
	if event.FeishuFiringSent {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, paired := s.firedFeishuEventIDs[event.ID]
	return paired
}

func (s *opsFeishuNotificationState) markRecoverySent(rule *OpsAlertRule, event *OpsAlertEvent) {
	if s == nil || rule == nil || event == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentAt == nil {
		s.sentAt = map[string]time.Time{}
	}
	s.sentAt[opsFeishuRecoveryDedupeKey(rule, event)] = time.Now().UTC()
	delete(s.firedFeishuEventIDs, event.ID)
}

func opsFeishuDedupeKey(rule *OpsAlertRule, event *OpsAlertEvent) string {
	if rule == nil || event == nil {
		return ""
	}
	parts := []string{
		fmt.Sprintf("rule:%d", rule.ID),
		"severity:" + strings.ToUpper(strings.TrimSpace(rule.Severity)),
		"metric:" + strings.TrimSpace(rule.MetricType),
	}
	keys := make([]string, 0, len(event.Dimensions))
	for k := range event.Dimensions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%v", k, event.Dimensions[k]))
	}
	return strings.Join(parts, "|")
}

func opsFeishuRecoveryDedupeKey(rule *OpsAlertRule, event *OpsAlertEvent) string {
	if rule == nil || event == nil {
		return ""
	}
	return fmt.Sprintf("recovery:event:%d|rule:%d", event.ID, rule.ID)
}
