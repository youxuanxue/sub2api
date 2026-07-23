package service

import (
	"context"
	"math"
	"sort"
	"time"
)

const (
	openAIAccountSelectionProbeLimit = 64
	openAIUpstreamCostNeutralFactor  = 0.5
)

type openAISelectionProbeBudget struct {
	acquires  int
	rechecks  int
	attempted map[int64]struct{}
	limited   bool
}

func newOpenAISelectionProbeBudget() *openAISelectionProbeBudget {
	return &openAISelectionProbeBudget{attempted: make(map[int64]struct{})}
}

func (b *openAISelectionProbeBudget) enableLimit() {
	if b != nil {
		b.limited = true
	}
}

func (b *openAISelectionProbeBudget) recordAcquire(accountID int64) bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
	if b.acquires >= openAIAccountSelectionProbeLimit {
		return false
	}
	if b.attempted == nil {
		b.attempted = make(map[int64]struct{})
	}
	b.acquires++
	b.attempted[accountID] = struct{}{}
	return true
}

func (b *openAISelectionProbeBudget) recordRecheck() bool {
	if b == nil {
		return false
	}
	if !b.limited {
		return true
	}
	if b.rechecks >= openAIAccountSelectionProbeLimit {
		return false
	}
	b.rechecks++
	return true
}

func (b *openAISelectionProbeBudget) acquireExhausted() bool {
	return b != nil && b.limited && b.acquires >= openAIAccountSelectionProbeLimit
}

func (b *openAISelectionProbeBudget) wasAttempted(accountID int64) bool {
	if b == nil || b.attempted == nil {
		return false
	}
	_, ok := b.attempted[accountID]
	return ok
}

func openAICostOverflowExpanded(req OpenAIAccountScheduleRequest, plan openAIAccountLoadPlan) bool {
	if !plan.includeOverflowFallback || plan.topK <= 0 {
		return false
	}
	if !req.RequireCompact {
		return len(plan.candidates) > plan.topK
	}
	supported, unknown := 0, 0
	for _, candidate := range plan.candidates {
		switch openAICompactSupportTier(candidate.account) {
		case 2:
			supported++
		case 1:
			unknown++
		}
	}
	return supported > plan.topK || unknown > plan.topK
}

func openAIUpstreamCostFactors(accounts []*Account, now time.Time, oauthSchedulingRateMultiplier float64) map[int64]float64 {
	type rateSample struct {
		accountID int64
		rate      float64
	}

	factors := make(map[int64]float64, len(accounts))
	samples := make([]rateSample, 0, len(accounts))
	eligibleCount := 0
	for _, account := range accounts {
		if account == nil {
			continue
		}
		factors[account.ID] = openAIUpstreamCostNeutralFactor
		if !account.IsOpenAIApiKey() && !account.IsOpenAIOAuth() {
			continue
		}
		eligibleCount++
		if rate, ok := openAISchedulingRate(account, now, oauthSchedulingRateMultiplier); ok {
			samples = append(samples, rateSample{accountID: account.ID, rate: rate})
		}
	}
	if len(samples) < 2 || eligibleCount == 0 {
		return factors
	}

	allEqual := true
	positiveLogs := make([]float64, 0, len(samples))
	for i, sample := range samples {
		if i > 0 && sample.rate != samples[0].rate {
			allEqual = false
		}
		if sample.rate > 0 {
			positiveLogs = append(positiveLogs, math.Log(sample.rate))
		}
	}
	if allEqual || len(positiveLogs) == 0 {
		return factors
	}

	sort.Float64s(positiveLogs)
	middle := len(positiveLogs) / 2
	medianLog := positiveLogs[middle]
	if len(positiveLogs)%2 == 0 {
		medianLog = (positiveLogs[middle-1] + positiveLogs[middle]) / 2
	}
	center := math.Exp(medianLog)
	if center <= 0 || math.IsNaN(center) || math.IsInf(center, 0) {
		return factors
	}

	coverage := float64(len(samples)) / float64(eligibleCount)
	for _, sample := range samples {
		rawFactor := 1.0
		if sample.rate > 0 {
			rawFactor = 1 / (1 + sample.rate/center)
		}
		factors[sample.accountID] = clamp01(openAIUpstreamCostNeutralFactor + coverage*(rawFactor-openAIUpstreamCostNeutralFactor))
	}
	return factors
}

func openAISchedulingRate(account *Account, now time.Time, oauthSchedulingRateMultiplier float64) (float64, bool) {
	if account != nil && account.IsOpenAIOAuth() {
		return oauthSchedulingRateMultiplier, true
	}
	return openAIFreshUpstreamBillingRate(account, now)
}

func openAIFreshUpstreamBillingRate(account *Account, now time.Time) (float64, bool) {
	if !isUpstreamBillingProbeAccount(account) {
		return 0, false
	}
	snapshot := decodeUpstreamBillingProbeSnapshot(account.Extra)
	if snapshot == nil || (snapshot.Status != UpstreamBillingProbeStatusOK && snapshot.Status != UpstreamBillingProbeStatusFailed) ||
		snapshot.ReceivedAt == nil || snapshot.ReceivedAt.IsZero() {
		return 0, false
	}
	receivedAt := *snapshot.ReceivedAt
	freshUntil := snapshot.FreshUntil
	if freshUntil == nil && snapshot.Status == UpstreamBillingProbeStatusOK {
		interval := snapshot.NextProbeAt.Sub(receivedAt)
		if interval > 0 {
			freshUntil = probeTimePtr(receivedAt.Add(2 * interval))
		}
	}
	if freshUntil == nil || !freshUntil.After(receivedAt) || now.Before(receivedAt) || now.After(*freshUntil) {
		return 0, false
	}
	return upstreamBillingRateAt(snapshot.Data, now)
}

type openAILegacyUpstreamRateOrder struct {
	enabled bool
	rates   map[int64]float64
}

func newOpenAILegacyUpstreamRateOrder(accounts []*Account, now time.Time, oauthSchedulingRateMultiplier float64) openAILegacyUpstreamRateOrder {
	rates := make(map[int64]float64, len(accounts))
	var first float64
	distinct := false
	for _, account := range accounts {
		rate, ok := openAISchedulingRate(account, now, oauthSchedulingRateMultiplier)
		if !ok {
			continue
		}
		if len(rates) == 0 {
			first = rate
		} else if rate != first {
			distinct = true
		}
		rates[account.ID] = rate
	}
	return openAILegacyUpstreamRateOrder{enabled: len(rates) >= 2 && distinct, rates: rates}
}

func (o openAILegacyUpstreamRateOrder) compare(a, b *Account) int {
	if !o.enabled || a == nil || b == nil {
		return 0
	}
	aRate, aKnown := o.rates[a.ID]
	bRate, bKnown := o.rates[b.ID]
	if aKnown != bKnown {
		if aKnown {
			return -1
		}
		return 1
	}
	if !aKnown || aRate == bRate {
		return 0
	}
	if aRate < bRate {
		return -1
	}
	return 1
}

func (s *OpenAIGatewayService) openAIOAuthSchedulingRateMultiplier(ctx context.Context) float64 {
	settings := s.openAIAdvancedSchedulerRuntimeSettings(ctx)
	if settings.oauthSchedulingRateMultiplier > 0 {
		return settings.oauthSchedulingRateMultiplier
	}
	return defaultOpenAIOAuthSchedulingRateMultiplier
}

func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAIAccountSlot(
	ctx context.Context,
	accountID int64,
	maxConcurrency int,
	budget *openAISelectionProbeBudget,
) (*AcquireResult, bool, error) {
	if s.service.concurrencyService != nil && maxConcurrency > 0 && !budget.recordAcquire(accountID) {
		return nil, false, nil
	}
	result, err := s.service.tryAcquireAccountSlot(ctx, accountID, maxConcurrency)
	return result, true, err
}

func (s *defaultOpenAIAccountScheduler) consumeOpenAISelectionDBRecheck(budget *openAISelectionProbeBudget) bool {
	if s.service.schedulerSnapshot == nil || s.service.accountRepo == nil {
		return true
	}
	return budget.recordRecheck()
}
