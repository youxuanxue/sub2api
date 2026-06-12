package service

import (
	"context"
	"encoding/json"
	"testing"
)

// TestAccountIncidentDigestEnabled_DefaultOffThroughRealNormalizePath is the
// hardening regression for PR#730: its off-switch was gated on
// AccountIncidentDigestSeconds>0, but normalizeOpsFeishuAlertConfig refills
// seconds 0→600, so the gate was never false and the "default off" never
// worked. PR#730's unit test missed it because it injected a fixed config
// struct instead of going through the real
// GetEmailNotificationConfig → GetValue → normalize path. This test exercises
// the REAL path with an in-memory settingRepo holding stored JSON, so the
// "off-switch defeated by normalize" class can never ship green again.
func TestAccountIncidentDigestEnabled_DefaultOffThroughRealNormalizePath(t *testing.T) {
	ctx := context.Background()

	// Case 1 — legacy / default stored config WITHOUT the new bool key.
	// After GetEmailNotificationConfig + normalize the bool must be FALSE
	// (digest OFF) even though normalize refilled seconds 0→600.
	{
		repo := newRuntimeSettingRepoStub()
		svc := &OpsService{settingRepo: repo}
		// Stored JSON deliberately omits account_incident_digest_enabled and
		// account_incident_digest_seconds (legacy shape) → enabled decodes to
		// false, seconds decodes to 0.
		repo.values[SettingKeyOpsEmailNotificationConfig] = `{"feishu":{"enabled":true,"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/test"}}`

		cfg, err := svc.GetEmailNotificationConfig(ctx)
		if err != nil {
			t.Fatalf("GetEmailNotificationConfig error = %v", err)
		}
		if cfg.Feishu.AccountIncidentDigestEnabled {
			t.Fatal("legacy stored config (no enabled key) must default digest OFF after normalize")
		}
		// seconds 0→600 interval default still applied — interval is independent of enable.
		if cfg.Feishu.AccountIncidentDigestSeconds != opsFeishuAccountIncidentDigestSecondsDefault {
			t.Fatalf("interval default not applied: seconds = %d, want %d",
				cfg.Feishu.AccountIncidentDigestSeconds, opsFeishuAccountIncidentDigestSecondsDefault)
		}

		// The notifier reads through the SAME real config provider → digest is OFF.
		// This is the exact assertion that would have caught PR#730.
		n := newTKAccountIncidentNotifier(svc, "edge-test")
		if n.temporaryDigestEnabled() {
			t.Fatal("temporaryDigestEnabled must be FALSE for legacy/default config (digest OFF)")
		}
	}

	// Case 2 — stored config WITH account_incident_digest_enabled:true → digest ON.
	{
		repo := newRuntimeSettingRepoStub()
		svc := &OpsService{settingRepo: repo}
		repo.values[SettingKeyOpsEmailNotificationConfig] = `{"feishu":{"enabled":true,"webhook_url":"https://open.feishu.cn/open-apis/bot/v2/hook/test","account_incident_digest_enabled":true}}`

		cfg, err := svc.GetEmailNotificationConfig(ctx)
		if err != nil {
			t.Fatalf("GetEmailNotificationConfig error = %v", err)
		}
		if !cfg.Feishu.AccountIncidentDigestEnabled {
			t.Fatal("explicit account_incident_digest_enabled:true must survive normalize")
		}
		n := newTKAccountIncidentNotifier(svc, "edge-test")
		if !n.temporaryDigestEnabled() {
			t.Fatal("temporaryDigestEnabled must be TRUE when explicitly enabled")
		}
	}

	// Case 3 — enabling via the real Update merge path, then re-read, round-trips
	// the bool through marshal → store → GetValue → unmarshal → normalize.
	{
		repo := newRuntimeSettingRepoStub()
		svc := &OpsService{settingRepo: repo}
		feishu := defaultOpsFeishuAlertConfig()
		feishu.Enabled = true
		feishu.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/test"
		feishu.AccountIncidentDigestEnabled = true
		if _, err := svc.UpdateEmailNotificationConfig(ctx,
			&OpsEmailNotificationConfigUpdateRequest{Feishu: &feishu}); err != nil {
			t.Fatalf("update error = %v", err)
		}
		// Confirm the persisted JSON actually carries the key (not lost on marshal).
		var stored OpsEmailNotificationConfig
		if err := json.Unmarshal([]byte(repo.values[SettingKeyOpsEmailNotificationConfig]), &stored); err != nil {
			t.Fatalf("stored JSON unmarshal error = %v", err)
		}
		if !stored.Feishu.AccountIncidentDigestEnabled {
			t.Fatal("persisted config must carry account_incident_digest_enabled=true")
		}
		reread, err := svc.GetEmailNotificationConfig(ctx)
		if err != nil {
			t.Fatalf("re-read error = %v", err)
		}
		if !reread.Feishu.AccountIncidentDigestEnabled {
			t.Fatal("enabled bool must round-trip through update + re-read")
		}
	}
}

// 两个 digest-seconds 字段晚于原始 feishu 契约加入。早于它们的 API 客户端
// （运维脚本 / 旧前端 bundle）发的"完整" feishu payload 不含这些键（解码为 0），
// 而 update 路径 validate 先于 normalize 执行——若无条件拷贝，0 会覆盖存量值并让
// 整个设置更新 400。本测试钉住"0=未提供，保留存量值"的兼容语义。
func TestUpdateEmailNotificationConfig_LegacyFeishuPayloadKeepsDigestSeconds(t *testing.T) {
	repo := newRuntimeSettingRepoStub()
	svc := &OpsService{settingRepo: repo}
	ctx := context.Background()

	seed := defaultOpsFeishuAlertConfig()
	seed.Enabled = true
	seed.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/test"
	seed.AccountIncidentDigestSeconds = 900
	seed.PricingMissingDigestSeconds = 3600
	if _, err := svc.UpdateEmailNotificationConfig(ctx,
		&OpsEmailNotificationConfigUpdateRequest{Feishu: &seed}); err != nil {
		t.Fatalf("seed update error = %v", err)
	}

	// 旧客户端形态：完整 feishu 对象但不含两个 digest 字段（零值）。
	legacy := defaultOpsFeishuAlertConfig()
	legacy.Enabled = true
	legacy.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/test"
	legacy.AccountIncidentDigestSeconds = 0
	legacy.PricingMissingDigestSeconds = 0
	updated, err := svc.UpdateEmailNotificationConfig(ctx,
		&OpsEmailNotificationConfigUpdateRequest{Feishu: &legacy})
	if err != nil {
		t.Fatalf("legacy-shaped update must not fail validation, got %v", err)
	}
	if updated.Feishu.AccountIncidentDigestSeconds != 900 {
		t.Fatalf("AccountIncidentDigestSeconds = %d, want stored 900 preserved",
			updated.Feishu.AccountIncidentDigestSeconds)
	}
	if updated.Feishu.PricingMissingDigestSeconds != 3600 {
		t.Fatalf("PricingMissingDigestSeconds = %d, want stored 3600 preserved",
			updated.Feishu.PricingMissingDigestSeconds)
	}

	// 显式值仍可更新。
	explicit := defaultOpsFeishuAlertConfig()
	explicit.Enabled = true
	explicit.WebhookURL = "https://open.feishu.cn/open-apis/bot/v2/hook/test"
	explicit.AccountIncidentDigestSeconds = 120
	explicit.PricingMissingDigestSeconds = 240
	updated, err = svc.UpdateEmailNotificationConfig(ctx,
		&OpsEmailNotificationConfigUpdateRequest{Feishu: &explicit})
	if err != nil {
		t.Fatalf("explicit update error = %v", err)
	}
	if updated.Feishu.AccountIncidentDigestSeconds != 120 || updated.Feishu.PricingMissingDigestSeconds != 240 {
		t.Fatalf("explicit values not applied: got (%d, %d), want (120, 240)",
			updated.Feishu.AccountIncidentDigestSeconds, updated.Feishu.PricingMissingDigestSeconds)
	}
}
