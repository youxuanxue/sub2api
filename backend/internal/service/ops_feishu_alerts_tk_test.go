//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingFeishuHTTPDoer struct {
	status int
	body   string
	err    error

	calls  int
	bodies []string
}

func (d *recordingFeishuHTTPDoer) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		d.bodies = append(d.bodies, string(body))
	}
	if d.err != nil {
		return nil, d.err
	}
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{},
	}, nil
}

func TestOpsFeishuEmailNotificationConfigDefaultsAndResponseMasking(t *testing.T) {
	t.Parallel()

	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}

	cfg, err := svc.GetEmailNotificationConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Feishu.Enabled)
	require.Empty(t, cfg.Feishu.WebhookURL)
	require.False(t, cfg.Feishu.WebhookURLConfigured)
	require.Equal(t, opsFeishuAlertRateLimitPerHourDefault, cfg.Feishu.RateLimitPerHour)
	require.Equal(t, opsFeishuAlertCooldownSecondsDefault, cfg.Feishu.CooldownSeconds)

	cfg.Feishu.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/token"
	cfg.Feishu.SigningSecret = "top-secret"
	masked := cfg.ForResponse()
	require.Empty(t, masked.Feishu.WebhookURL)
	require.Empty(t, masked.Feishu.SigningSecret)
	require.True(t, masked.Feishu.WebhookURLConfigured)
	require.True(t, masked.Feishu.SigningSecretConfigured)
	require.NotEmpty(t, cfg.Feishu.WebhookURL)
	require.NotEmpty(t, cfg.Feishu.SigningSecret)
}

func TestOpsFeishuEmailNotificationConfigBackfillsLegacyJSON(t *testing.T) {
	t.Parallel()

	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	legacy := map[string]any{
		"alert": map[string]any{
			"enabled":                 true,
			"recipients":              []string{},
			"min_severity":            "",
			"rate_limit_per_hour":     0,
			"batching_window_seconds": 0,
			"include_resolved_alerts": false,
		},
		"report": map[string]any{
			"enabled":                             false,
			"recipients":                          []string{},
			"daily_summary_schedule":              "0 9 * * *",
			"weekly_summary_schedule":             "0 9 * * 1",
			"error_digest_schedule":               "0 9 * * *",
			"error_digest_min_count":              10,
			"account_health_schedule":             "0 9 * * *",
			"account_health_error_rate_threshold": 10,
		},
	}
	raw, err := json.Marshal(legacy)
	require.NoError(t, err)
	repo.values[SettingKeyOpsEmailNotificationConfig] = string(raw)

	cfg, err := svc.GetEmailNotificationConfig(context.Background())
	require.NoError(t, err)
	require.False(t, cfg.Feishu.Enabled)
	require.Equal(t, opsFeishuAlertRateLimitPerHourDefault, cfg.Feishu.RateLimitPerHour)
	require.Equal(t, opsFeishuAlertCooldownSecondsDefault, cfg.Feishu.CooldownSeconds)
}

func TestUpdateOpsFeishuEmailNotificationConfigPreservesWriteOnlySecrets(t *testing.T) {
	t.Parallel()

	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	cfg := defaultOpsEmailNotificationConfig()
	cfg.Feishu.Enabled = true
	cfg.Feishu.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/old-token"
	cfg.Feishu.SigningSecret = "old-secret"
	normalizeOpsEmailNotificationConfig(cfg)
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo.values[SettingKeyOpsEmailNotificationConfig] = string(raw)

	updated, err := svc.UpdateEmailNotificationConfig(context.Background(), &OpsEmailNotificationConfigUpdateRequest{
		Feishu: &OpsFeishuAlertConfig{
			Enabled:          true,
			WebhookURL:       " ",
			SigningSecret:    "",
			RateLimitPerHour: 6,
			CooldownSeconds:  7200,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "https://open.feishu.cn/open-apis/bot/v2/hook/old-token", updated.Feishu.WebhookURL)
	require.Equal(t, "old-secret", updated.Feishu.SigningSecret)
	require.Equal(t, 6, updated.Feishu.RateLimitPerHour)
	require.Equal(t, 7200, updated.Feishu.CooldownSeconds)
}

func TestUpdateOpsFeishuEmailNotificationConfigValidatesWebhookWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	_, err := svc.UpdateEmailNotificationConfig(context.Background(), &OpsEmailNotificationConfigUpdateRequest{
		Feishu: &OpsFeishuAlertConfig{
			Enabled:          true,
			WebhookURL:       "http://open.feishu.cn/open-apis/bot/v2/hook/raw-token",
			SigningSecret:    "raw-secret",
			RateLimitPerHour: 3,
			CooldownSeconds:  3600,
		},
	})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "raw-token")
	require.NotContains(t, err.Error(), "raw-secret")
}

func TestOpsFeishuNotifierBuildsSignedPayload(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	notifier := &opsFeishuNotifier{now: func() time.Time { return now }}
	payload, err := notifier.buildPayload(OpsFeishuAlertConfig{SigningSecret: "signing-secret"}, "", testOpsFeishuRule(), testOpsFeishuEvent(1))
	require.NoError(t, err)
	require.Equal(t, "interactive", payload["msg_type"])
	require.Equal(t, "1700000000", payload["timestamp"])
	require.Equal(t, signFeishuWebhook("1700000000", "signing-secret"), payload["sign"])

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(body), "TokenKey P0")
	require.Contains(t, string(body), "group_available_accounts")
}

func TestOpsFeishuNotifierIncludesNodeIdentityAndLink(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000000, 0).UTC()
	notifier := &opsFeishuNotifier{now: func() time.Time { return now }}

	// Configured edge node: label derived from host, card carries the deep-link
	// and a node-suffixed header so it is distinguishable from other nodes.
	payload, err := notifier.buildPayload(OpsFeishuAlertConfig{}, "https://api-us1.tokenkey.dev", testOpsFeishuRule(), testOpsFeishuEvent(1))
	require.NoError(t, err)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(body), "TokenKey P0 告警 · us1")
	require.Contains(t, string(body), "**节点**")
	require.Contains(t, string(body), "us1")
	require.Contains(t, string(body), "https://api-us1.tokenkey.dev/admin/ops")

	// prod bare host -> "prod" label.
	prodPayload, err := notifier.buildPayload(OpsFeishuAlertConfig{}, "https://api.tokenkey.dev", testOpsFeishuRule(), testOpsFeishuEvent(1))
	require.NoError(t, err)
	prodBody, err := json.Marshal(prodPayload)
	require.NoError(t, err)
	require.Contains(t, string(prodBody), "TokenKey P0 告警 · prod")

	// Unconfigured node (empty frontend_url): degrade to "overall", no header
	// suffix, no link.
	bare, err := notifier.buildPayload(OpsFeishuAlertConfig{}, "", testOpsFeishuRule(), testOpsFeishuEvent(1))
	require.NoError(t, err)
	bareBody, err := json.Marshal(bare)
	require.NoError(t, err)
	require.NotContains(t, string(bareBody), "/admin/ops")
	require.NotContains(t, string(bareBody), "告警 · ")
}

func TestOpsFeishuNotifierSanitizesWebhookErrors(t *testing.T) {
	t.Parallel()

	fullURL := "https://open.feishu.cn/open-apis/bot/v2/hook/raw-token?debug=raw-secret"
	err := sanitizeFeishuWebhookError(errors.New("Post \""+fullURL+"\": dial tcp failed"), fullURL)
	require.NotContains(t, err, "raw-token")
	require.NotContains(t, err, "raw-secret")
	require.Contains(t, err, "https://open.feishu.cn/<redacted>")

	rootURL := "https://open.feishu.cn"
	rootErr := sanitizeFeishuWebhookError(errors.New("Post \""+rootURL+"\": dial tcp failed"), rootURL)
	require.Equal(t, "Post \"https://open.feishu.cn/<redacted>\": dial tcp failed", rootErr)
}

func TestMaybeSendAlertFeishuP0AndClientVisibleP1Only(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(rule *OpsAlertRule, event *OpsAlertEvent)
	}{
		{
			name: "ordinary P1 rule never sends",
			mutate: func(rule *OpsAlertRule, event *OpsAlertEvent) {
				rule.Severity = "P1"
				event.Severity = "P1"
			},
		},
		{
			name: "resolved event does not send firing card",
			mutate: func(rule *OpsAlertRule, event *OpsAlertEvent) {
				event.Status = OpsAlertStatusResolved
			},
		},
		{
			name: "notify email disabled never sends",
			mutate: func(rule *OpsAlertRule, event *OpsAlertEvent) {
				rule.NotifyEmail = false
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
			svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
			rule := testOpsFeishuRule()
			event := testOpsFeishuEvent(1)
			tt.mutate(rule, event)

			sent := svc.maybeSendAlertFeishu(context.Background(), nil, rule, event)
			require.False(t, sent)
			require.Equal(t, 0, doer.calls)
		})
	}
}

func TestMaybeSendAlertFeishuAllowsClientVisibleP1(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	rule.Name = "真实用户客户端失败增多"
	rule.Severity = "P1"
	rule.MetricType = OpsAlertMetricClientVisibleFailureCount
	rule.Operator = ">="
	rule.Threshold = 20
	event := testOpsFeishuEvent(1)
	event.Severity = "P1"
	event.Dimensions = map[string]any{
		"user_visible_affected": "#16 compute@tk.com ×21",
		"user_visible_impact":   "失败 21 / 成功 17336 / 失败率 0.12% / 5m",
		"user_visible_surface":  "final 400 / invalid_request_error ×21",
		"user_visible_root":     "request/client / openai / gpt-5.5 / prompt too long ×21",
	}

	require.True(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, event))
	require.Equal(t, 1, doer.calls)
	require.Contains(t, doer.bodies[0], "TokenKey P1 告警")
	require.Contains(t, doer.bodies[0], "orange")
	require.Contains(t, doer.bodies[0], "**谁受影响**")
	require.Contains(t, doer.bodies[0], "**影响多大**")
	require.Contains(t, doer.bodies[0], "**用户看到什么**")
	require.Contains(t, doer.bodies[0], "**根因在哪**")
}

func TestBuildOpsFeishuClientVisibleTextOmitsScopeAndWrapsBreakdowns(t *testing.T) {
	t.Parallel()

	rule := testOpsFeishuRule()
	rule.Name = "真实用户客户端失败增多"
	rule.Severity = "P1"
	rule.MetricType = OpsAlertMetricClientVisibleFailureCount
	rule.Operator = ">="
	rule.Threshold = 20
	event := testOpsFeishuEvent(100)
	event.Severity = "P1"
	triggerValue := 171.0
	threshold := 20.0
	event.MetricValue = &triggerValue
	event.ThresholdValue = &threshold
	resolvedAt := event.FiredAt.Add(31*time.Minute + 26*time.Second)
	event.ResolvedAt = &resolvedAt
	event.Dimensions = map[string]any{
		"user_visible_affected": `#16 compute@tk.com ×171（key "benchmark组-赵欣宇"） · #17 ops@tk.com ×20（key "benchmark组-李雷"）`,
		"user_visible_impact":   "失败 171 / 成功 751 / 失败率 18.55% / 5m",
		"user_visible_surface":  "final 403 / invalid request error ×171",
		"user_visible_root":     `auth/client / openai / No platform in your plan can serve model "gpt-5-mini". ×88 · auth/client / openai / No platform in your plan can serve model "gpt-5.1". ×83`,
	}
	currentValue := 1.0

	firing := buildOpsFeishuAlertText(rule, event, "prod", "")
	recovery := buildOpsFeishuRecoveryText(rule, event, "prod", "", &currentValue)

	for _, text := range []string{firing, recovery} {
		require.NotContains(t, text, "**范围**")
		require.Contains(t, text, "**谁受影响**：\n#16 compute@tk.com ×171")
		require.Contains(t, text, "\n#17 ops@tk.com ×20")
		require.Contains(t, text, "**根因在哪**：\nauth/client / openai / No platform in your plan can serve model \"gpt-5-mini\". ×88")
		require.Contains(t, text, "\nauth/client / openai / No platform in your plan can serve model \"gpt-5.1\". ×83")
	}
}

func TestMaybeSendAlertFeishuSendsAndDedupesByCooldown(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	event := testOpsFeishuEvent(1)

	require.True(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, event))
	require.False(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, event))
	require.Equal(t, 1, doer.calls)
}

func TestMaybeSendAlertFeishuRateLimitsAcrossDistinctDimensions(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 1, CooldownSeconds: 60}, doer)
	rule := testOpsFeishuRule()

	require.True(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, testOpsFeishuEvent(1)))
	require.False(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, testOpsFeishuEvent(2)))
	require.Equal(t, 1, doer.calls)
}

func TestOpsFeishuNotifierBuildsRecoveryPayload(t *testing.T) {
	t.Parallel()

	now := time.Unix(1700000060, 0).UTC()
	firedAt := time.Unix(1700000000, 0).UTC()
	resolvedAt := now
	notifier := &opsFeishuNotifier{now: func() time.Time { return now }}
	event := testOpsFeishuEvent(42)
	event.Status = OpsAlertStatusResolved
	event.ResolvedAt = &resolvedAt
	event.FiredAt = firedAt
	current := 0.0
	event.Dimensions = map[string]any{
		"top_cause":        `newapi ×128（#16 "Agent-陈乐晗-qwen" ×128）`,
		"top_cause_models": "gpt5.4-mini ×128",
	}

	payload, err := notifier.buildRecoveryPayload(OpsFeishuAlertConfig{}, "https://api.tokenkey.dev", testOpsFeishuRule(), event, &current)
	require.NoError(t, err)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	text := string(body)
	require.Contains(t, text, "TokenKey P0 告警已恢复 · prod")
	require.Contains(t, text, "green")
	require.Contains(t, text, "**触发值**")
	require.Contains(t, text, "**当前值**")
	require.Contains(t, text, "**持续时长**")
	require.Contains(t, text, "1分")
	require.Contains(t, text, "**主因**")
	require.Contains(t, text, "newapi ×128")
	require.Contains(t, text, "**模型**")
	require.Contains(t, text, "gpt5.4-mini ×128")
}

func TestMaybeSendAlertFeishuRecoverySendsPairedGreenCard(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	firing := testOpsFeishuEvent(99)

	require.True(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, firing))
	require.Equal(t, 1, doer.calls)
	require.Contains(t, doer.bodies[0], "TokenKey P0 告警")

	resolvedAt := firing.FiredAt.Add(time.Minute)
	resolved := *firing
	resolved.Status = OpsAlertStatusResolved
	resolved.ResolvedAt = &resolvedAt

	require.True(t, svc.maybeSendAlertFeishuRecovery(context.Background(), nil, rule, &resolved, 0))
	require.Equal(t, 2, doer.calls)
	require.Contains(t, doer.bodies[1], "TokenKey P0 告警已恢复")
	require.Contains(t, doer.bodies[1], "green")

	require.False(t, svc.maybeSendAlertFeishuRecovery(context.Background(), nil, rule, &resolved, 0))
	require.Equal(t, 2, doer.calls, "recovery must dedupe per event")
}

func TestMaybeSendAlertFeishuRecoverySendsClientVisibleP1GreenCard(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	rule.Name = "真实用户客户端失败增多"
	rule.Severity = "P1"
	rule.MetricType = OpsAlertMetricClientVisibleFailureCount
	rule.Operator = ">="
	rule.Threshold = 20
	firing := testOpsFeishuEvent(100)
	firing.Severity = "P1"
	firing.Dimensions = map[string]any{
		"user_visible_affected": "#16 compute@tk.com ×21",
		"user_visible_impact":   "失败 21 / 成功 17336 / 失败率 0.12% / 5m",
		"user_visible_surface":  "final 400 / invalid_request_error ×21",
		"user_visible_root":     "request/client / openai / gpt-5.5 / prompt too long ×21",
	}

	require.True(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, firing))
	require.Equal(t, 1, doer.calls)
	require.Contains(t, doer.bodies[0], "TokenKey P1 告警")

	resolvedAt := firing.FiredAt.Add(time.Minute)
	resolved := *firing
	resolved.Status = OpsAlertStatusResolved
	resolved.ResolvedAt = &resolvedAt

	require.True(t, svc.maybeSendAlertFeishuRecovery(context.Background(), nil, rule, &resolved, 0))
	require.Equal(t, 2, doer.calls)
	require.Contains(t, doer.bodies[1], "TokenKey P1 告警已恢复")
	require.Contains(t, doer.bodies[1], "green")
	require.Contains(t, doer.bodies[1], "**谁受影响**")
}

func TestMaybeSendAlertFeishuRecoverySkipsWithoutPriorFiringCard(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{body: `{"code":0}`}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	resolvedAt := time.Unix(1700000060, 0).UTC()
	resolved := testOpsFeishuEvent(7)
	resolved.Status = OpsAlertStatusResolved
	resolved.ResolvedAt = &resolvedAt

	require.False(t, svc.maybeSendAlertFeishuRecovery(context.Background(), nil, rule, resolved, 0))
	require.Equal(t, 0, doer.calls)
}

func TestMaybeSendAlertFeishuFailureDoesNotMarkCooldown(t *testing.T) {
	t.Parallel()

	doer := &recordingFeishuHTTPDoer{err: errors.New("Post \"https://open.feishu.cn/open-apis/bot/v2/hook/raw-token\": dial tcp failed")}
	svc := newOpsFeishuAlertEvaluatorForTest(t, OpsFeishuAlertConfig{Enabled: true, WebhookURL: "https://open.feishu.cn/open-apis/bot/v2/hook/raw-token", RateLimitPerHour: 3, CooldownSeconds: 3600}, doer)
	rule := testOpsFeishuRule()
	event := testOpsFeishuEvent(1)

	require.False(t, svc.maybeSendAlertFeishu(context.Background(), nil, rule, event))
	require.Equal(t, 1, doer.calls)
	_, seen := svc.feishuState.sentAt[opsFeishuDedupeKey(rule, event)]
	require.False(t, seen)
}

func newOpsFeishuAlertEvaluatorForTest(t *testing.T, feishu OpsFeishuAlertConfig, doer *recordingFeishuHTTPDoer) *OpsAlertEvaluatorService {
	t.Helper()

	repo := newRuntimeSettingRepoStub()
	cfg := defaultOpsEmailNotificationConfig()
	cfg.Feishu = feishu
	normalizeOpsEmailNotificationConfig(cfg)
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo.values[SettingKeyOpsEmailNotificationConfig] = string(raw)

	return &OpsAlertEvaluatorService{
		opsService: &OpsService{settingRepo: repo},
		feishuState: &opsFeishuNotificationState{
			limiter:             newSlidingWindowLimiter(opsFeishuAlertRateLimitPerHourDefault, time.Hour),
			notifier:            &opsFeishuNotifier{httpClient: doer},
			sentAt:              map[string]time.Time{},
			firedFeishuEventIDs: map[int64]struct{}{},
		},
	}
}

func testOpsFeishuRule() *OpsAlertRule {
	return &OpsAlertRule{
		ID:          101,
		Name:        "核心分组可用账号耗尽",
		Severity:    "P0",
		MetricType:  "group_available_accounts",
		Operator:    "<=",
		Threshold:   0,
		NotifyEmail: true,
	}
}

func testOpsFeishuEvent(groupID int64) *OpsAlertEvent {
	value := 0.0
	threshold := 0.0
	return &OpsAlertEvent{
		ID:             groupID,
		RuleID:         101,
		Severity:       "P0",
		Status:         OpsAlertStatusFiring,
		MetricValue:    &value,
		ThresholdValue: &threshold,
		Dimensions: map[string]any{
			"platform": "openai",
			"group_id": groupID,
		},
		FiredAt: time.Unix(1700000000, 0).UTC(),
	}
}
