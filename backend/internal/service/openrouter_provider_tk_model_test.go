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

func TestOpenRouterProviderConfig_SellerAuthBillingUser(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		Enabled:       true,
		BillingUserID: 32,
	}
	if !cfg.AllowsSellerAPIKey(1, 32) {
		t.Fatal("any billing-user key must pass seller surface")
	}
	if !cfg.AllowsInferenceAPIKey(1, 32) {
		t.Fatal("billing-user key must infer")
	}
	if !cfg.CanAccessCatalog(2, 32) {
		t.Fatal("billing-user key must read catalog")
	}
	if cfg.AllowsSellerAPIKey(1, 99) {
		t.Fatal("other user must fail")
	}
}

func TestOpenRouterProviderConfig_LegacyIDAllowlist(t *testing.T) {
	cfg := OpenRouterProviderConfig{
		Enabled:          true,
		AllowedAPIKeyIDs: []int64{10},
		MonitorAPIKeyIDs: []int64{99},
		BillingUserID:    7,
	}
	if !cfg.CanAccessCatalog(99, 0) {
		t.Fatal("legacy monitor id must access full seller surface")
	}
	if !cfg.AllowsInferenceAPIKey(99, 0) {
		t.Fatal("legacy monitor id must also infer after dual-key removal")
	}
	if !cfg.AllowsInferenceAPIKey(10, 0) {
		t.Fatal("legacy inference id must pass")
	}
	if cfg.AllowsSellerAPIKey(11, 0) {
		t.Fatal("unknown id outside billing user must fail")
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
	newBody, internal, changed, err := svc.NormalizeOpenRouterProviderChatBody(context.Background(), 10, 1, body)
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

func TestNormalizeOpenRouterProviderChatBody_NonSellerNoRewrite(t *testing.T) {
	rawCfg := OpenRouterProviderConfig{
		Enabled:       true,
		BillingUserID: 32,
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
	_, _, changed, err := svc.NormalizeOpenRouterProviderChatBody(context.Background(), 99, 1, body)
	if err != nil || changed {
		t.Fatalf("non-billing key must not rewrite changed=%v err=%v", changed, err)
	}
}
