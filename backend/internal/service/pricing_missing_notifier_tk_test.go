//go:build unit

package service

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Reuses fakeIncidentConfigProvider / enabledFeishuConfig / blockingFeishuDoer
// from account_incident_notifier_tk_test.go (same package, same unit tag).

func newTestPricingMissingNotifier(provider opsFeishuConfigProvider, doer opsFeishuHTTPDoer, fixedNow time.Time) *TKPricingMissingNotifier {
	n := newTKPricingMissingNotifier(provider, "edge-test")
	n.httpClient = doer
	n.now = func() time.Time { return fixedNow }
	return n
}

func waitFeishuCalls(t *testing.T, done chan struct{}, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for feishu send %d/%d", i+1, want)
		}
	}
}

func samplePricingMissingEvent() PricingMissingEvent {
	return PricingMissingEvent{
		Platform:       "newapi",
		BillingModel:   "doubao-seedream-9",
		RequestedModel: "doubao-seedream-9",
		UpstreamModel:  "doubao-seedream-9-250901",
		GroupID:        3,
		GroupName:      "vertex-pool",
		APIKeyID:       42,
		Tokens:         1500,
	}
}

func TestPricingMissingNotifier_FirstSeenSendsOnce_ThenAggregates(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 8)}
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, now)

	ev := samplePricingMissingEvent()
	n.NotifyPricingMissing(ev)
	waitFeishuCalls(t, doer.done, 1)
	body := doer.lastBody()
	require.Contains(t, body, "doubao-seedream-9", "first-seen card must name the model")
	require.Contains(t, body, "newapi", "first-seen card must name the platform")
	require.Contains(t, body, "已照常服务", "card must state service was NOT refused")
	require.Contains(t, body, "apply-pricing-hotfix.py", "card must point at the hot-update runbook")

	// 同 (platform, model) 第二次:不再发即时卡,只进聚合。
	n.NotifyPricingMissing(ev)
	require.Equal(t, 1, doer.callCount(), "second event within dedupe window must not send another immediate card")

	n.mu.Lock()
	require.Len(t, n.digest, 1)
	for _, e := range n.digest {
		require.Equal(t, 2, e.count)
		require.Equal(t, int64(3000), e.tokens)
		require.Len(t, e.groupIDs, 1)
		require.Len(t, e.apiKeyIDs, 1)
	}
	n.mu.Unlock()
}

func TestPricingMissingNotifier_FlushDigest_SendsAndClears(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 8)}
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, now)

	ev := samplePricingMissingEvent()
	n.NotifyPricingMissing(ev) // 1 immediate card
	n.NotifyPricingMissing(ev)
	other := samplePricingMissingEvent()
	other.Platform = "gemini"
	other.BillingModel = "imagen-9.0-generate-001"
	n.NotifyPricingMissing(other) // 1 more immediate card (different model)
	waitFeishuCalls(t, doer.done, 2)

	n.flushDigest()
	waitFeishuCalls(t, doer.done, 1)
	body := doer.lastBody()
	require.Contains(t, body, "doubao-seedream-9")
	require.Contains(t, body, "imagen-9.0-generate-001")
	require.Contains(t, body, "3.0k", "digest must surface the unbilled token volume")

	n.mu.Lock()
	require.Empty(t, n.digest, "flush must clear the buffer")
	n.mu.Unlock()

	// 空 buffer 再 flush:不发。
	before := doer.callCount()
	n.flushDigest()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, before, doer.callCount(), "empty digest must not send")
}

func TestPricingMissingNotifier_DisabledFeishu_NoSend(t *testing.T) {
	cfg := enabledFeishuConfig()
	cfg.Feishu.Enabled = false
	doer := &blockingFeishuDoer{}
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: cfg}, doer, time.Now())

	n.NotifyPricingMissing(samplePricingMissingEvent())
	n.flushDigest()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, 0, doer.callCount())
}

func TestPricingMissingNotifier_EmptyModel_Ignored(t *testing.T) {
	doer := &blockingFeishuDoer{}
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, time.Now())

	n.NotifyPricingMissing(PricingMissingEvent{Platform: "openai"})
	n.mu.Lock()
	require.Empty(t, n.digest)
	n.mu.Unlock()
	require.Equal(t, 0, doer.callCount())
}

func TestPricingMissingNotifier_PruneFirstSeen(t *testing.T) {
	doer := &blockingFeishuDoer{done: make(chan struct{}, 8)}
	base := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, doer, base)

	n.NotifyPricingMissing(samplePricingMissingEvent())
	waitFeishuCalls(t, doer.done, 1)

	// 超过去重窗口后修剪,首见卡可再次发送。
	n.now = func() time.Time { return base.Add(pricingMissingFirstSeenDedupeWindow + time.Minute) }
	n.pruneFirstSeen()
	n.NotifyPricingMissing(samplePricingMissingEvent())
	waitFeishuCalls(t, doer.done, 1)
	require.Equal(t, 2, doer.callCount())
}

func TestPricingMissingNotifier_DigestIntervalFromConfig(t *testing.T) {
	cfg := enabledFeishuConfig()
	cfg.Feishu.PricingMissingDigestSeconds = 120
	n := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: cfg}, &blockingFeishuDoer{}, time.Now())
	require.Equal(t, 2*time.Minute, n.digestInterval())

	// 配置缺失 → 兜底 1800s。
	n2 := newTestPricingMissingNotifier(&fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, &blockingFeishuDoer{}, time.Now())
	require.Equal(t, time.Duration(pricingMissingDigestSecondsFallback)*time.Second, n2.digestInterval())
}

// --- served-but-zero-cost probe tests (root-cause ②) -----------------------

type recordingPricingMissingNotifier struct {
	events []PricingMissingEvent
}

func (r *recordingPricingMissingNotifier) NotifyPricingMissing(ev PricingMissingEvent) {
	r.events = append(r.events, ev)
}

func TestServedZeroCostReason_TruthTable(t *testing.T) {
	// 倍率前 TotalCost==0 且有计费单元 → unpriced（无价模型/渠道$0/视频·per_request
	// 无价/cost-calc 错误吞 $0 都落这里）。
	reason, ok := tkServedZeroCostReason(&CostBreakdown{TotalCost: 0, ActualCost: 0}, 160, 1.0, 1.0)
	require.True(t, ok)
	require.Equal(t, "unpriced", reason)

	// 价格有效但负倍率归零 → negative_multiplier（group 倍率 / account 倍率任一为负）。
	reason, ok = tkServedZeroCostReason(&CostBreakdown{TotalCost: 10, ActualCost: 0}, 160, -1.0, 1.0)
	require.True(t, ok)
	require.Equal(t, "negative_multiplier", reason)
	_, ok = tkServedZeroCostReason(&CostBreakdown{TotalCost: 10, ActualCost: 0}, 160, 1.0, -2.0)
	require.True(t, ok)

	// 合法免费分组（倍率恰为 0、价格有效）→ 不报。
	_, ok = tkServedZeroCostReason(&CostBreakdown{TotalCost: 10, ActualCost: 0}, 160, 0.0, 1.0)
	require.False(t, ok, "multiplier==0 is a legitimate free tier, must NOT alert")

	// 无计费单元（失败/空请求/count_tokens）→ 不报。
	_, ok = tkServedZeroCostReason(&CostBreakdown{TotalCost: 0}, 0, 1.0, 1.0)
	require.False(t, ok)

	// nil cost → 不报。
	_, ok = tkServedZeroCostReason(nil, 100, 1.0, 1.0)
	require.False(t, ok)
}

func TestGatewayTkNotifyServedZeroCost_FeedsNotifier(t *testing.T) {
	rec := &recordingPricingMissingNotifier{}
	svc := &GatewayService{tkPricingMissingNotifier: rec}

	result := &ForwardResult{
		Model:         "glm-5",
		UpstreamModel: "glm-5-air",
		Usage:         ClaudeUsage{InputTokens: 100, OutputTokens: 50, CacheReadInputTokens: 10},
	}
	apiKey := &APIKey{ID: 7, Group: &Group{ID: 3, Name: "g3", Platform: PlatformAnthropic}}

	// unpriced：TotalCost==0 且已服务 → 喂通知器。
	svc.tkNotifyServedZeroCost(&CostBreakdown{TotalCost: 0}, result, apiKey, "glm-5", "glm-5", 1.0, 1.0)
	require.Len(t, rec.events, 1)
	ev := rec.events[0]
	require.Equal(t, "unpriced", ev.Reason)
	require.Equal(t, "glm-5", ev.BillingModel)
	require.Equal(t, "glm-5-air", ev.UpstreamModel)
	require.Equal(t, PlatformAnthropic, ev.Platform)
	require.Equal(t, int64(3), ev.GroupID)
	require.Equal(t, int64(7), ev.APIKeyID)
	require.Equal(t, int64(160), ev.Tokens)

	// 合法免费（倍率=0、价格有效）→ 不喂。
	svc.tkNotifyServedZeroCost(&CostBreakdown{TotalCost: 10, ActualCost: 0}, result, apiKey, "glm-5", "glm-5", 0.0, 1.0)
	require.Len(t, rec.events, 1)

	// 负倍率 → 喂（negative_multiplier）。
	svc.tkNotifyServedZeroCost(&CostBreakdown{TotalCost: 10, ActualCost: 0}, result, apiKey, "glm-5", "glm-5", -1.0, 1.0)
	require.Len(t, rec.events, 2)
	require.Equal(t, "negative_multiplier", rec.events[1].Reason)
}

func TestGatewayTkNotifyServedZeroCost_NilSafe(t *testing.T) {
	svc := &GatewayService{}
	require.NotPanics(t, func() {
		svc.tkNotifyServedZeroCost(&CostBreakdown{TotalCost: 0}, &ForwardResult{Usage: ClaudeUsage{InputTokens: 1}}, nil, "m", "m", 1.0, 1.0)
	})
}

func TestOpenAITkNotifyServedZeroCost_FeedsNotifier(t *testing.T) {
	rec := &recordingPricingMissingNotifier{}
	svc := &OpenAIGatewayService{tkPricingMissingNotifier: rec}

	input := &OpenAIRecordUsageInput{}
	input.OriginalModel = "doubao-seedream-9"
	result := &OpenAIForwardResult{
		Model:         "doubao-seedream-9",
		UpstreamModel: "doubao-seedream-9-250901",
		Usage:         OpenAIUsage{OutputTokens: 5},
	}
	apiKey := &APIKey{ID: 9, Group: &Group{ID: 5, Name: "np", Platform: PlatformNewAPI}}

	// unpriced：actualInputTokens=20 + output 5 = 25 计费单元。
	svc.tkNotifyServedZeroCost(&CostBreakdown{TotalCost: 0}, result, apiKey, input, []string{"doubao-seedream-9"}, 20, 1.0, 1.0)
	require.Len(t, rec.events, 1)
	ev := rec.events[0]
	require.Equal(t, "unpriced", ev.Reason)
	require.Equal(t, "doubao-seedream-9", ev.BillingModel)
	require.Equal(t, "doubao-seedream-9", ev.RequestedModel)
	require.Equal(t, "doubao-seedream-9-250901", ev.UpstreamModel)
	require.Equal(t, PlatformNewAPI, ev.Platform)
	require.Equal(t, int64(25), ev.Tokens)

	// nil 输入安全。
	bare := &OpenAIGatewayService{}
	require.NotPanics(t, func() {
		bare.tkNotifyServedZeroCost(nil, nil, nil, nil, nil, 0, 1.0, 1.0)
	})
}

func TestPricingMissingAdviceText_MentionsBothRemediationPaths(t *testing.T) {
	require.True(t, strings.Contains(pricingMissingActionSteps, "apply-pricing-hotfix.py"))
	require.True(t, strings.Contains(pricingMissingActionSteps, "tk_pricing_overlay.json"))
}

// TestPricingMissingFirstSeenCard_GateRejectVsServed pins the R4 alert fix: a price-gate
// rejection card must NOT claim the request was served — that would make ops under-react to a
// real 404 (client rejected). The served-zero-cost card keeps the "served, not refused" framing.
func TestPricingMissingFirstSeenCard_GateRejectVsServed(t *testing.T) {
	now := time.Date(2026, 6, 10, 8, 0, 0, 0, time.UTC)

	served := samplePricingMissingEvent() // Reason "" → served-zero-cost
	servedBody := buildPricingMissingFirstSeenText("site", served, served.Platform, served.BillingModel, now)
	require.Contains(t, servedBody, "已照常服务", "served-zero-cost card states service was NOT refused")
	require.NotContains(t, servedBody, "返回 404 拒绝")

	rejected := samplePricingMissingEvent()
	rejected.Reason = tkPricedServingGateRejectReason
	rejectedBody := buildPricingMissingFirstSeenText("site", rejected, rejected.Platform, rejected.BillingModel, now)
	require.Contains(t, rejectedBody, "返回 404 拒绝", "gate-reject card must say the client was 404'd")
	require.Contains(t, rejectedBody, "未服务", "gate-reject card must say the request was NOT served")
	require.NotContains(t, rejectedBody, "已照常服务", "gate-reject card must NOT claim the request was served")
	// both cards carry the same actionable remediation steps
	require.Contains(t, rejectedBody, "apply-pricing-hotfix.py")
}
