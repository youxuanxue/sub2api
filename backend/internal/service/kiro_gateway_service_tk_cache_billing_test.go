//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
)

type kiroCacheBillingRepoStub struct {
	SettingRepository
	value string
	err   error
}

func (s kiroCacheBillingRepoStub) GetValue(context.Context, string) (string, error) {
	return s.value, s.err
}

func TestIsKiroCacheBillingEnabled_DefaultOn(t *testing.T) {
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	svc := &SettingService{settingRepo: kiroCacheBillingRepoStub{err: ErrSettingNotFound}}
	if !svc.IsKiroCacheBillingEnabled(context.Background()) {
		t.Fatal("absent key must default to true")
	}
	if !(*SettingService)(nil).IsKiroCacheBillingEnabled(context.Background()) {
		t.Fatal("nil SettingService must default to true")
	}
}

func TestIsKiroCacheBillingEnabled_FalseOnlyDisables(t *testing.T) {
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	svc := &SettingService{settingRepo: kiroCacheBillingRepoStub{value: "false"}}
	if svc.IsKiroCacheBillingEnabled(context.Background()) {
		t.Fatal(`"false" must disable`)
	}
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	svc = &SettingService{settingRepo: kiroCacheBillingRepoStub{value: "true"}}
	if !svc.IsKiroCacheBillingEnabled(context.Background()) {
		t.Fatal(`"true" must stay enabled`)
	}
}

func TestKiroPromptUsage_FlagOffKeepsFullInput(t *testing.T) {
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	svc := &KiroGatewayService{
		tkSettingService: &SettingService{settingRepo: kiroCacheBillingRepoStub{value: "false"}},
		kiroCacheStore:   kiroproto.NewMemoryCacheFingerprintStore(),
	}
	req := &kiroproto.ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []kiroproto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	payload := kiroproto.ClaudeToKiro(req, false)
	in, read, create, session, enabled := svc.kiroPromptUsage(context.Background(), &Account{ID: 9}, req, payload)
	if enabled || read != 0 || create != 0 || session != "" {
		t.Fatalf("flag-off: enabled=%v read=%d create=%d session=%q", enabled, read, create, session)
	}
	if in != kiroproto.EstimateInputTokens(req) {
		t.Fatalf("input=%d want full estimate", in)
	}
}

func TestKiroPromptUsage_DefaultOnSplitsWhenPrefixed(t *testing.T) {
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	store := kiroproto.NewMemoryCacheFingerprintStore()
	svc := &KiroGatewayService{
		tkSettingService: &SettingService{settingRepo: kiroCacheBillingRepoStub{err: ErrSettingNotFound}},
		kiroCacheStore:   store,
	}
	large := strings.Repeat("alpha beta gamma delta ", 200)
	turn1 := &kiroproto.ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: large,
		Messages: []kiroproto.ClaudeMessage{
			{Role: "user", Content: large},
			{Role: "assistant", Content: "ack"},
		},
	}
	payload1 := kiroproto.ClaudeToKiro(turn1, false)
	_, _, _, session, enabled := svc.kiroPromptUsage(context.Background(), &Account{ID: 9}, turn1, payload1)
	if !enabled || session == "" {
		t.Fatalf("default-on should enable cache billing, enabled=%v session=%q", enabled, session)
	}
	svc.commitKiroCacheFingerprints(context.Background(), session, turn1, nil, true)

	turn2 := &kiroproto.ClaudeRequest{
		Model:  "claude-sonnet-4-6",
		System: large,
		Messages: []kiroproto.ClaudeMessage{
			{Role: "user", Content: large},
			{Role: "assistant", Content: "ack"},
			{Role: "user", Content: "continue " + large[:200]},
		},
	}
	payload2 := kiroproto.ClaudeToKiro(turn2, false)
	in, read, _, _, _ := svc.kiroPromptUsage(context.Background(), &Account{ID: 9}, turn2, payload2)
	if read <= 0 {
		t.Fatalf("second turn should have cache_read, got input=%d read=%d", in, read)
	}
}
