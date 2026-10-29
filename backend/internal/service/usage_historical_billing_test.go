package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type historicalBillingFake struct {
	UsageLogRepository
	snapshot *usagestats.HistoricalBillingSnapshot
	err      error
	userID   int64
	calls    int
}

func (r *historicalBillingFake) GetHistoricalBillingSnapshot(_ context.Context, userID int64) (*usagestats.HistoricalBillingSnapshot, error) {
	r.userID = userID
	r.calls++
	return r.snapshot, r.err
}
func billingFixture() *usagestats.HistoricalBillingSnapshot {
	updated := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	return &usagestats.HistoricalBillingSnapshot{
		RegisteredAt: time.Date(2026, 6, 4, 7, 30, 0, 0, time.UTC), Timezone: "UTC", GeneratedAt: updated.Add(time.Hour),
		Rows: []usagestats.HistoricalBillingRow{
			{Date: "2026-06-04", Platform: "openai", Requests: 2, InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 40, ActualCost: "0.1000000001", ComputedAt: updated},
			{Date: "2026-06-05", Platform: "openai", Requests: 1, InputTokens: 5, ActualCost: "0.2000000002", ComputedAt: updated},
			{Date: "2026-06-05", Platform: "", Requests: 3, InputTokens: 7, ActualCost: "0", ComputedAt: updated},
			{Date: "2026-10-29", Platform: "anthropic", Requests: 4, OutputTokens: 8, ActualCost: "1.25", ComputedAt: updated},
		},
	}
}
func TestUS073_HistoricalBillingRetainsDailyAndMonthlyCharges(t *testing.T) {
	repo := &historicalBillingFake{snapshot: billingFixture()}
	svc := NewUsageService(repo, nil, nil, nil)
	daily, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.NoError(t, err)
	require.Equal(t, int64(16), repo.userID)
	require.Equal(t, "2026-06-04", daily.StartDate)
	require.Equal(t, "2026-10-29", daily.EndDate)
	require.Equal(t, "1.5500000003", daily.Totals.ActualCost)
	require.Equal(t, int64(10), daily.Totals.Requests) // include zero-cost requests
	require.Equal(t, int64(120), daily.Totals.TotalTokens)
	require.Equal(t, []string{"", "anthropic", "openai"}, daily.Platforms)
	require.False(t, daily.Rows[0].Provisional)
	require.True(t, daily.Rows[3].Provisional)
	platform := "openai"
	monthly, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{StartDate: "2026-06-04", EndDate: "2026-06-30", Granularity: "month", Platform: &platform})
	require.NoError(t, err)
	require.Equal(t, "0.3000000003", monthly.Rows[0].ActualCost)
	require.Equal(t, "2026-06", monthly.Rows[0].Date)
	require.Equal(t, int64(105), monthly.Totals.TotalTokens)
	require.False(t, monthly.Totals.Provisional)
	partial, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{StartDate: "2026-06-05", EndDate: "2026-06-05", Granularity: "month", Platform: &platform})
	require.NoError(t, err)
	require.Equal(t, "0.2000000002", partial.Totals.ActualCost)
	emptyPlatform := ""
	unknown, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{Platform: &emptyPlatform})
	require.NoError(t, err)
	require.Equal(t, int64(3), unknown.Totals.Requests)
	require.Equal(t, "0", unknown.Totals.ActualCost)
	// A month is still open even when this platform has no usage today.
	repo.snapshot.Rows[3].Date = "2026-10-28"
	currentMonth, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{Granularity: "month"})
	require.NoError(t, err)
	require.True(t, currentMonth.Rows[len(currentMonth.Rows)-1].Provisional)
	require.True(t, currentMonth.Totals.Provisional)
}
func TestUS073_HistoricalBillingValidationAndFailures(t *testing.T) {
	for _, query := range []usagestats.HistoricalBillingQuery{
		{StartDate: "2026-06-04"}, {StartDate: "2026-02-30", EndDate: "2026-03-01"},
		{StartDate: "2026-06-05", EndDate: "2026-06-04"}, {Granularity: "hour"},
	} {
		repo := &historicalBillingFake{snapshot: billingFixture()}
		_, err := NewUsageService(repo, nil, nil, nil).GetHistoricalBilling(context.Background(), 16, query)
		require.Error(t, err)
		require.Zero(t, repo.calls)
	}
	repo := &historicalBillingFake{snapshot: billingFixture()}
	svc := NewUsageService(repo, nil, nil, nil)
	_, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{StartDate: "2026-10-29", EndDate: "2026-10-30"})
	require.ErrorContains(t, err, "current billing day")
	repo.snapshot.Timezone = ""
	_, err = svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.ErrorContains(t, err, "timezone")
	repo.err = errors.New("database unavailable")
	_, err = svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.ErrorContains(t, err, "database unavailable")
}
func TestUS073_HistoricalBillingTimezoneEmptyAndInvalidMoney(t *testing.T) {
	repo := &historicalBillingFake{snapshot: billingFixture()}
	repo.snapshot.Timezone = "America/Los_Angeles"
	repo.snapshot.RegisteredAt = time.Date(2026, 6, 4, 1, 0, 0, 0, time.UTC)
	svc := NewUsageService(repo, nil, nil, nil)
	report, err := svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.NoError(t, err)
	require.Equal(t, "2026-06-03", report.StartDate)
	require.Equal(t, "2026-10-28", report.EndDate)
	require.Equal(t, "0.3000000003", report.Totals.ActualCost) // future billing date excluded
	repo.snapshot.Rows = nil
	report, err = svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.NoError(t, err)
	require.Equal(t, "0", report.Totals.ActualCost)
	require.Equal(t, []usagestats.HistoricalBillingRow{}, report.Rows)
	require.Empty(t, report.AvailableStartDate)
	repo.snapshot = billingFixture()
	repo.snapshot.Rows[0].ActualCost = "invalid"
	report, err = svc.GetHistoricalBilling(context.Background(), 16, usagestats.HistoricalBillingQuery{})
	require.Nil(t, report)
	require.ErrorContains(t, err, "billing amount")
}
