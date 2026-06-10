package service

import (
	"context"
	"testing"
)

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
