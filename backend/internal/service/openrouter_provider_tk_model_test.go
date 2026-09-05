//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestOpenRouterProviderConfig_InternalModelID(t *testing.T) {
	cfg := DefaultOpenRouterProviderConfig()
	if got, ok := cfg.InternalModelID("tokenkey/deepseek-v4-pro"); !ok || got != "deepseek-v4-pro" {
		t.Fatalf("got (%q, %v)", got, ok)
	}
	if _, ok := cfg.InternalModelID("deepseek-v4-pro"); ok {
		t.Fatal("bare model must not reverse-map")
	}
}

func TestOpenRouterProviderConfig_MonitorVsInferenceKeys(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		Enabled:          true,
		AllowedAPIKeyIDs: []int64{10},
		MonitorAPIKeyIDs: []int64{99},
		BillingUserID:    7,
	}
	if !cfg.CanAccessCatalog(99, 0, "") {
		t.Fatal("monitor key must read catalog")
	}
	if cfg.AllowsInferenceAPIKey(99, 0, "") {
		t.Fatal("monitor key must not be treated as inference key")
	}
	if !cfg.AllowsInferenceAPIKey(10, 0, "") {
		t.Fatal("inference key must pass")
	}
}

func TestOpenRouterProviderConfig_NameBasedKeysOnBillingUser(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		Enabled:       true,
		BillingUserID: 32,
	}
	if !cfg.AllowsInferenceAPIKey(1, 32, OpenRouterProviderInferenceKeyName) {
		t.Fatal("named inference key on billing user must pass")
	}
	if !cfg.AllowsMonitorAPIKey(2, 32, OpenRouterProviderMonitorKeyName) {
		t.Fatal("named monitor key on billing user must pass")
	}
	if cfg.AllowsInferenceAPIKey(2, 32, OpenRouterProviderMonitorKeyName) {
		t.Fatal("monitor-named key must not infer")
	}
	if cfg.AllowsInferenceAPIKey(1, 99, OpenRouterProviderInferenceKeyName) {
		t.Fatal("named inference key on other user must fail")
	}
	if !cfg.CanAccessCatalog(2, 32, OpenRouterProviderMonitorKeyName) {
		t.Fatal("monitor name must access catalog")
	}
	if cfg.AllowsInferenceAPIKey(3, 32, "scratch") {
		t.Fatal("non-conventional billing-user key must not infer after id-list removal")
	}
}

func TestNormalizeOpenRouterProviderChatBody_RewritesModel(t *testing.T) {
	rawCfg := OpenRouterProviderConfig{
		Enabled:          true,
		AllowedAPIKeyIDs: []int64{10},
		ModelIDPrefix:    "tokenkey/",
	}
	encoded, err := json.Marshal(rawCfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &SettingService{
		settingRepo: func() *stubSettingRepo {
			repo := newStubSettingRepo()
			repo.values[SettingKeyTKOpenRouterProviderConfig] = string(encoded)
			return repo
		}(),
	}
	body := []byte(`{"model":"tokenkey/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)
	newBody, internal, changed, err := svc.NormalizeOpenRouterProviderChatBody(context.Background(), 10, 1, "", body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if internal != "deepseek-v4-pro" {
		t.Fatalf("internal=%q", internal)
	}
	if got := string(newBody); got == string(body) {
		t.Fatal("body must be rewritten")
	}
}

func TestNormalizeOpenRouterProviderChatBody_MonitorKeyNoRewrite(t *testing.T) {
	rawCfg := OpenRouterProviderConfig{
		Enabled:          true,
		MonitorAPIKeyIDs: []int64{99},
	}
	encoded, err := json.Marshal(rawCfg)
	if err != nil {
		t.Fatal(err)
	}
	svc := &SettingService{
		settingRepo: func() *stubSettingRepo {
			repo := newStubSettingRepo()
			repo.values[SettingKeyTKOpenRouterProviderConfig] = string(encoded)
			return repo
		}(),
	}
	body := []byte(`{"model":"tokenkey/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`)
	_, _, changed, err := svc.NormalizeOpenRouterProviderChatBody(context.Background(), 99, 1, OpenRouterProviderMonitorKeyName, body)
	if err != nil || changed {
		t.Fatalf("monitor key must not rewrite body changed=%v err=%v", changed, err)
	}
}
