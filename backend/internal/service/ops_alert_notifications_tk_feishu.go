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
	mu       sync.Mutex
	limiter  *slidingWindowLimiter
	notifier *opsFeishuNotifier
	sentAt   map[string]time.Time
}

func newOpsFeishuNotificationState() *opsFeishuNotificationState {
	return &opsFeishuNotificationState{
		limiter:  newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour),
		notifier: newOpsFeishuNotifier(),
		sentAt:   map[string]time.Time{},
	}
}

func (s *OpsAlertEvaluatorService) maybeSendAlertNotifications(ctx context.Context, runtimeCfg *OpsAlertRuntimeSettings, rule *OpsAlertRule, event *OpsAlertEvent) opsAlertNotificationResult {
	result := opsAlertNotificationResult{}
	if s == nil {
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

func (s *OpsAlertEvaluatorService) maybeSendAlertFeishu(ctx context.Context, runtimeCfg *OpsAlertRuntimeSettings, rule *OpsAlertRule, event *OpsAlertEvent) bool {
	if s == nil || s.opsService == nil || rule == nil || event == nil {
		return false
	}
	if !shouldSendOpsAlertToFeishu(rule, event) {
		return false
	}
	cfg, err := s.opsService.GetEmailNotificationConfig(ctx)
	if err != nil || cfg == nil || !cfg.Feishu.Enabled {
		return false
	}
	if strings.TrimSpace(cfg.Feishu.WebhookURL) == "" {
		return false
	}
	if runtimeCfg != nil && runtimeCfg.Silencing.Enabled {
		if isOpsAlertSilenced(time.Now().UTC(), rule, event, runtimeCfg.Silencing) {
			return false
		}
	}
	state := s.feishuState
	if state == nil {
		state = newOpsFeishuNotificationState()
		s.feishuState = state
	}
	if !state.shouldSend(rule, event, cfg.Feishu) {
		return false
	}
	notifier := state.notifier
	if notifier == nil {
		notifier = newOpsFeishuNotifier()
	}
	if err := notifier.sendAlert(ctx, cfg.Feishu, rule, event); err != nil {
		logger.LegacyPrintf("service.ops_alert_evaluator", "[OpsAlertEvaluator] feishu alert send failed event_id=%d rule_id=%d error=%s", event.ID, rule.ID, err.Error())
		return false
	}
	state.markSent(rule, event)
	return true
}

func shouldSendOpsAlertToFeishu(rule *OpsAlertRule, event *OpsAlertEvent) bool {
	return rule != nil && event != nil &&
		strings.EqualFold(strings.TrimSpace(event.Status), OpsAlertStatusFiring) &&
		strings.EqualFold(strings.TrimSpace(rule.Severity), "P0") &&
		strings.EqualFold(strings.TrimSpace(event.Severity), "P0") &&
		rule.NotifyEmail
}

func (s *opsFeishuNotificationState) shouldSend(rule *OpsAlertRule, event *OpsAlertEvent, cfg OpsFeishuAlertConfig) bool {
	if s == nil || rule == nil || event == nil {
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
	if s.limiter == nil {
		s.limiter = newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour)
	}
	s.limiter.SetLimit(cfg.RateLimitPerHour)
	return s.limiter.Allow(now)
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
