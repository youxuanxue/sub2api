package service

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The digest must not print an already-billed served_at_fallback event as
// "未计费". 2026-08-25 prod: a deepseek-v4-flash-0731 request that billed
// normally produced the card "deepseek-v4-flash-0731：1 次 / 约 142 tokens
// 未计费", sending an operator to hunt a revenue leak that did not exist.
func TestDigestReasonWordingMatchesCustomerImpact(t *testing.T) {
	now := time.Date(2026, 8, 25, 11, 16, 26, 0, time.UTC)

	fallback := &pricingMissingDigestEntry{
		reason:       tkServedAtFallbackReason,
		platform:     "newapi",
		billingModel: "deepseek-v4-flash-0731",
		count:        1,
		tokens:       142,
		groupIDs:     map[int64]struct{}{19: {}},
		groupSamples: []string{"china(#19)"},
		apiKeyIDs:    map[int64]struct{}{361: {}},
	}

	body := buildPricingMissingDigestText("prod", []*pricingMissingDigestEntry{fallback}, now)
	require.NotContains(t, body, "未计费",
		"a served_at_fallback entry bills normally; printing 未计费 is the 2026-08-25 misreport")
	require.Contains(t, body, "tokens 按 floor 计费")
	require.Contains(t, body, "按家族兜底价(floor)计费")

	// Title and header colour must follow content too: no revenue is leaking here.
	require.Equal(t, "兜底价(floor)计费摘要", pricingMissingDigestTitleSubject([]*pricingMissingDigestEntry{fallback}))
	require.Equal(t, "blue", pricingMissingDigestHeaderTemplate([]*pricingMissingDigestEntry{fallback}))
}

// A real $0 leak must still read as a leak, and when both land in one flush the
// two reasons must appear as separate sections rather than one merged line.
func TestDigestSeparatesReasonsAndEscalatesOnRealLeak(t *testing.T) {
	now := time.Date(2026, 8, 25, 11, 16, 26, 0, time.UTC)

	leak := &pricingMissingDigestEntry{
		reason:       pricingMissingReasonUnpriced,
		platform:     "newapi",
		billingModel: "some-unpriced-model",
		count:        7,
		tokens:       5000,
		groupIDs:     map[int64]struct{}{19: {}},
		groupSamples: []string{"china(#19)"},
		apiKeyIDs:    map[int64]struct{}{361: {}},
	}
	fallback := &pricingMissingDigestEntry{
		reason:       tkServedAtFallbackReason,
		platform:     "newapi",
		billingModel: "deepseek-v4-flash-0731",
		count:        1,
		tokens:       142,
		groupIDs:     map[int64]struct{}{19: {}},
		groupSamples: []string{"china(#19)"},
		apiKeyIDs:    map[int64]struct{}{361: {}},
	}

	entries := []*pricingMissingDigestEntry{leak, fallback}
	body := buildPricingMissingDigestText("prod", entries, now)

	require.Contains(t, body, "缺价模型零成本流量摘要：")
	require.Contains(t, body, "按家族兜底价(floor)计费")
	require.Contains(t, body, "tokens 未计费")
	require.Contains(t, body, "tokens 按 floor 计费")

	// The real leak must dominate the title/colour when mixed.
	require.Equal(t, "已服务零计费摘要", pricingMissingDigestTitleSubject(entries))
	require.Equal(t, "orange", pricingMissingDigestHeaderTemplate(entries))

	// Ranking puts the leak above the already-billed section.
	require.Less(t, pricingMissingReasonRank(pricingMissingReasonUnpriced),
		pricingMissingReasonRank(tkServedAtFallbackReason))
}

// The aggregation key must include reason: without it the four reasons collapse
// into one entry and the digest cannot express which of them happened.
func TestDigestKeySeparatesReasonsForSameModel(t *testing.T) {
	n := newTKPricingMissingNotifier(nil, "prod")
	base := PricingMissingEvent{
		Platform:     "newapi",
		BillingModel: "deepseek-v4-flash-0731",
		GroupID:      19,
		GroupName:    "china",
		APIKeyID:     361,
		Tokens:       100,
	}

	leaked := base
	leaked.Reason = pricingMissingReasonUnpriced
	fell := base
	fell.Reason = tkServedAtFallbackReason

	n.NotifyPricingMissing(leaked)
	n.NotifyPricingMissing(fell)

	n.mu.Lock()
	got := len(n.digest)
	n.mu.Unlock()
	require.Equal(t, 2, got, "same model + different reason must not merge")

	// A legacy empty Reason folds into unpriced rather than creating a third bucket.
	legacy := base
	legacy.Reason = ""
	n.NotifyPricingMissing(legacy)
	n.mu.Lock()
	got = len(n.digest)
	n.mu.Unlock()
	require.Equal(t, 2, got, "empty Reason must fold into unpriced")
}

// The first-seen card must not call a floor-billed or 404-rejected event a
// "P0 计费漏算" — only a genuine $0 leak earns that title.
func TestFirstSeenTitleFollowsReason(t *testing.T) {
	subject, header := pricingMissingFirstSeenTitleSubject(tkServedAtFallbackReason)
	require.NotContains(t, subject, "漏算")
	require.Equal(t, "blue", header)

	subject, header = pricingMissingFirstSeenTitleSubject(tkPricedServingGateRejectReason)
	require.NotContains(t, subject, "漏算")
	require.Equal(t, "orange", header)

	subject, header = pricingMissingFirstSeenTitleSubject(pricingMissingReasonUnpriced)
	require.Contains(t, subject, "漏算")
	require.Equal(t, "red", header)

	// 组合出的完整标题（与 NotifyPricingMissing 的拼法一致）不得再说「漏算」。
	subject, _ = pricingMissingFirstSeenTitleSubject(tkServedAtFallbackReason)
	title := fmt.Sprintf("TokenKey %s [%s]", subject, "prod")
	require.True(t, strings.Contains(title, "prod"), "title must name the site")
	require.NotContains(t, title, "漏算")
}
