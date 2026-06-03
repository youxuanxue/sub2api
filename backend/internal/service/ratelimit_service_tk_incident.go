package service

import "time"

// TK: 账号失效事件 → 飞书告警的挂钩 glue。核心实现在 account_incident_notifier_tk.go。
//
// notifyAccountSchedulingBlocked（ratelimit_service.go）是所有账号失效/冷却的唯一汇聚点;
// 在其内部并排调用 notifyAccountIncident 上报。kind 传 Unknown,由 classifyIncident 依据
// reason 字符串精确派生（auth_error/custom/stream_timeout_error → 永久,其余 → 临时聚合）。

// SetAccountIncidentNotifier 注入账号失效通知器（由 wire sentinel provider 在构造后回填）。
func (s *RateLimitService) SetAccountIncidentNotifier(n AccountIncidentNotifier) {
	if s == nil {
		return
	}
	s.incidentNotifier = n
}

func (s *RateLimitService) notifyAccountIncident(account *Account, until time.Time, reason string, kind AccountIncidentKind) {
	if s == nil || s.incidentNotifier == nil || account == nil {
		return
	}
	s.incidentNotifier.NotifyAccountIncident(account, until, reason, kind)
}
