//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroCacheBillingRepoStub struct {
	SettingRepository
	value string
	err   error
}

func (s kiroCacheBillingRepoStub) GetValue(context.Context, string) (string, error) {
	return s.value, s.err
}

type kiroFailUpstream struct {
	gotRequest bool
}

func (u *kiroFailUpstream) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected Do call")
}

func (u *kiroFailUpstream) DoWithTLS(*http.Request, string, int64, int, *tlsfingerprint.Profile) (*http.Response, error) {
	u.gotRequest = true
	return nil, fmt.Errorf("kiro upstream boom")
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
		kiroCacheBillingSetting: &SettingService{settingRepo: kiroCacheBillingRepoStub{value: "false"}},
		kiroCacheStore:          kiroproto.NewMemoryCacheFingerprintStore(),
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

func TestKiroBillingTier_SeparatesCacheEstimatedFromLegacy(t *testing.T) {
	if got := kiroBillingTier(true); got != kiroproto.KiroCacheEstimatedBillingTier {
		t.Fatalf("enabled tier=%q", got)
	}
	if got := kiroBillingTier(false); got != kiroproto.KiroEstimatedBillingTier {
		t.Fatalf("disabled tier=%q", got)
	}
}

func TestKiroPromptUsage_FlagOnWithoutConversationStillMarksEnabled(t *testing.T) {
	svc := &KiroGatewayService{
		kiroCacheStore: kiroproto.NewMemoryCacheFingerprintStore(),
	}
	req := &kiroproto.ClaudeRequest{
		Model: "claude-sonnet-4-6",
		Messages: []kiroproto.ClaudeMessage{
			{Role: "user", Content: "hello"},
		},
	}
	in, read, _, session, enabled := svc.kiroPromptUsage(context.Background(), &Account{ID: 9}, req, &kiroproto.KiroPayload{})
	if !enabled {
		t.Fatal("flag-on with empty conversation must still report enabled for billing_tier")
	}
	if session != "" || read != 0 {
		t.Fatalf("empty conversation must not split: session=%q read=%d", session, read)
	}
	if in != kiroproto.EstimateInputTokens(req) {
		t.Fatalf("input=%d", in)
	}
	if got := kiroBillingTier(enabled); got != kiroproto.KiroCacheEstimatedBillingTier {
		t.Fatalf("tier=%q", got)
	}
}

func TestKiroPromptUsage_DefaultOnSplitsWhenPrefixed(t *testing.T) {
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	store := kiroproto.NewMemoryCacheFingerprintStore()
	svc := &KiroGatewayService{
		kiroCacheBillingSetting: &SettingService{settingRepo: kiroCacheBillingRepoStub{err: ErrSettingNotFound}},
		kiroCacheStore:          store,
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

func TestKiroGatewayService_Forward_CacheBilling_SecondTurnReadsPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := kiroproto.NewMemoryCacheFingerprintStore()
	large := strings.Repeat("alpha beta gamma delta ", 200)
	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"ok","inputTokens":1,"outputTokens":1}`))
	frame = appendKiroTerminalStop(frame, "END_TURN")

	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)
	svc.SetKiroCacheFingerprintStore(store)

	body1, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"system":     large,
		"messages":   []map[string]any{{"role": "user", "content": large}},
		"max_tokens": 16,
		"stream":     false,
	})
	parsed1 := &ParsedRequest{Body: NewRequestBodyRef(body1), Model: "claude-sonnet-4", Stream: false}
	rec1 := httptest.NewRecorder()
	c1, _ := gin.CreateTestContext(rec1)
	result1, err := svc.Forward(context.Background(), c1, newKiroAccountForTest(), parsed1, time.Now())
	require.NoError(t, err)
	require.Equal(t, "kiro-cache-estimated", result1.BillingTier)
	require.Zero(t, result1.Usage.CacheReadInputTokens)

	body2, _ := json.Marshal(map[string]any{
		"model":  "claude-sonnet-4",
		"system": large,
		"messages": []map[string]any{
			{"role": "user", "content": large},
			{"role": "assistant", "content": "ok"},
			{"role": "user", "content": "continue " + large[:120]},
		},
		"max_tokens": 16,
		"stream":     false,
	})
	parsed2 := &ParsedRequest{Body: NewRequestBodyRef(body2), Model: "claude-sonnet-4", Stream: false}
	rec2 := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec2)
	result2, err := svc.Forward(context.Background(), c2, newKiroAccountForTest(), parsed2, time.Now())
	require.NoError(t, err)
	require.Equal(t, "kiro-cache-estimated", result2.BillingTier)
	require.Positive(t, result2.Usage.CacheReadInputTokens)
	require.Equal(t, result2.Usage.InputTokens+result2.Usage.CacheReadInputTokens+result2.Usage.CacheCreationInputTokens,
		kiroproto.EstimateInputTokens(&kiroproto.ClaudeRequest{
			Model:  "claude-sonnet-4",
			System: large,
			Messages: []kiroproto.ClaudeMessage{
				{Role: "user", Content: large},
				{Role: "assistant", Content: "ok"},
				{Role: "user", Content: "continue " + large[:120]},
			},
		}))
}

func TestKiroGatewayService_Forward_CacheBilling_UpstreamFailureDoesNotCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := kiroproto.NewMemoryCacheFingerprintStore()
	large := strings.Repeat("alpha beta gamma delta ", 200)

	failSvc := NewKiroGatewayService(&kiroFailUpstream{}, nil, nil)
	failSvc.SetKiroCacheFingerprintStore(store)
	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4",
		"system":     large,
		"messages":   []map[string]any{{"role": "user", "content": large}},
		"max_tokens": 16,
		"stream":     false,
	})
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4", Stream: false}
	recFail := httptest.NewRecorder()
	cFail, _ := gin.CreateTestContext(recFail)
	_, err := failSvc.Forward(context.Background(), cFail, newKiroAccountForTest(), parsed, time.Now())
	require.Error(t, err)

	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"ok"}`))
	frame = appendKiroTerminalStop(frame, "END_TURN")
	okSvc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)
	okSvc.SetKiroCacheFingerprintStore(store)
	recOK := httptest.NewRecorder()
	cOK, _ := gin.CreateTestContext(recOK)
	result, err := okSvc.Forward(context.Background(), cOK, newKiroAccountForTest(), parsed, time.Now())
	require.NoError(t, err)
	require.Zero(t, result.Usage.CacheReadInputTokens, "failed turn must not commit fingerprints")
}

func TestKiroGatewayService_Forward_CacheBilling_FlagOffLegacyTier(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kiroCacheBillingCache.Store((*tkOptOutFlagCacheEntry)(nil))
	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"hello"}`))
	frame = appendKiroTerminalStop(frame, "END_TURN")
	svc := NewKiroGatewayService(&kiroFakeUpstream{body: frame}, nil, nil)
	svc.SetKiroCacheBillingSetting(&SettingService{settingRepo: kiroCacheBillingRepoStub{value: "false"}})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(false), time.Now())
	require.NoError(t, err)
	require.Equal(t, "kiro-estimated", result.BillingTier)
	require.Zero(t, result.Usage.CacheReadInputTokens)
	require.Zero(t, result.Usage.CacheCreationInputTokens)
}
