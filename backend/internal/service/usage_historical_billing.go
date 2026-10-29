package service

import (
	"context"
	"fmt"
	"sort"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/shopspring/decimal"
)

type historicalBillingRepository interface {
	GetHistoricalBillingSnapshot(context.Context, int64) (*usagestats.HistoricalBillingSnapshot, error)
}

// GetHistoricalBilling is the shared user/admin query and export contract.
// All-request counters are preserved, including zero-cost usage. Today remains
// a provisional feeder snapshot; no raw-log fallback can erase older history.
func (s *UsageService) GetHistoricalBilling(ctx context.Context, userID int64, query usagestats.HistoricalBillingQuery) (*usagestats.HistoricalBillingReport, error) {
	if userID <= 0 {
		return nil, historicalBillingBadRequest("Invalid user")
	}
	if query.Granularity == "" {
		query.Granularity = "day"
	}
	if query.Granularity != "day" && query.Granularity != "month" {
		return nil, historicalBillingBadRequest("granularity must be day or month")
	}
	if (query.StartDate == "") != (query.EndDate == "") {
		return nil, historicalBillingBadRequest("start_date and end_date are required together")
	}
	if query.StartDate != "" {
		for _, value := range []string{query.StartDate, query.EndDate} {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return nil, historicalBillingBadRequest("Dates must use YYYY-MM-DD")
			}
		}
		if query.StartDate > query.EndDate {
			return nil, historicalBillingBadRequest("end_date must be on or after start_date")
		}
	}
	repo, ok := s.usageRepo.(historicalBillingRepository)
	if !ok {
		return nil, fmt.Errorf("historical billing repository unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snapshot, err := repo.GetHistoricalBillingSnapshot(ctx, userID)
	if err != nil {
		return nil, err
	}
	if snapshot.Timezone == "" {
		return nil, infraerrors.ServiceUnavailable("BILLING_TIMEZONE_UNAVAILABLE", "Billing timezone is not available")
	}
	loc, err := time.LoadLocation(snapshot.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid historical billing timezone: %w", err)
	}
	start := snapshot.RegisteredAt.In(loc).Format("2006-01-02")
	today := snapshot.GeneratedAt.In(loc).Format("2006-01-02")
	end := today
	if query.StartDate != "" {
		start, end = query.StartDate, query.EndDate
	}
	if end > today {
		return nil, historicalBillingBadRequest("end_date cannot be after the current billing day")
	}
	report := &usagestats.HistoricalBillingReport{
		UserID: userID, RegisteredAt: snapshot.RegisteredAt, StartDate: start, EndDate: end,
		Timezone: snapshot.Timezone, Currency: "USD", Granularity: query.Granularity,
		Platform: query.Platform, Platforms: []string{}, GeneratedAt: snapshot.GeneratedAt,
		Rows: []usagestats.HistoricalBillingRow{}, Totals: usagestats.HistoricalBillingRow{ActualCost: "0"},
	}
	platforms := map[string]bool{}
	grouped := map[string]*usagestats.HistoricalBillingRow{}
	for _, row := range snapshot.Rows {
		if _, err := time.Parse("2006-01-02", row.Date); err != nil {
			return nil, fmt.Errorf("invalid stored billing date: %w", err)
		}
		if row.Date > today {
			continue
		}
		platforms[row.Platform] = true
		if report.AvailableStartDate == "" || row.Date < report.AvailableStartDate {
			report.AvailableStartDate = row.Date
		}
		if row.Date > report.AvailableEndDate {
			report.AvailableEndDate = row.Date
		}
		if row.Date < start || row.Date > end || (query.Platform != nil && row.Platform != *query.Platform) {
			continue
		}
		row.TotalTokens = row.InputTokens + row.OutputTokens + row.CacheCreationTokens + row.CacheReadTokens
		row.Provisional = row.Date == today
		if query.Granularity == "month" {
			row.Date = row.Date[:7]
			row.Provisional = row.Date == today[:7]
		}
		key := row.Date + "\x00" + row.Platform
		if grouped[key] == nil {
			grouped[key] = &usagestats.HistoricalBillingRow{Date: row.Date, Platform: row.Platform, ActualCost: "0"}
		}
		if err := addHistoricalBillingRow(grouped[key], row); err != nil {
			return nil, err
		}
		if err := addHistoricalBillingRow(&report.Totals, row); err != nil {
			return nil, err
		}
	}
	for platform := range platforms {
		report.Platforms = append(report.Platforms, platform)
	}
	sort.Strings(report.Platforms)
	for _, row := range grouped {
		report.Rows = append(report.Rows, *row)
	}
	sort.Slice(report.Rows, func(i, j int) bool {
		if report.Rows[i].Date != report.Rows[j].Date {
			return report.Rows[i].Date < report.Rows[j].Date
		}
		return report.Rows[i].Platform < report.Rows[j].Platform
	})
	return report, nil
}

func historicalBillingBadRequest(message string) error {
	return infraerrors.BadRequest("INVALID_BILLING_QUERY", message)
}

func addHistoricalBillingRow(total *usagestats.HistoricalBillingRow, row usagestats.HistoricalBillingRow) error {
	amount, err := decimal.NewFromString(row.ActualCost)
	if err != nil {
		return fmt.Errorf("invalid stored billing amount: %w", err)
	}
	accumulated, err := decimal.NewFromString(total.ActualCost)
	if err != nil {
		return err
	}
	total.ActualCost = accumulated.Add(amount).String()
	total.Requests += row.Requests
	total.InputTokens += row.InputTokens
	total.OutputTokens += row.OutputTokens
	total.CacheCreationTokens += row.CacheCreationTokens
	total.CacheReadTokens += row.CacheReadTokens
	total.TotalTokens += row.TotalTokens
	total.Provisional = total.Provisional || row.Provisional
	if row.ComputedAt.After(total.ComputedAt) {
		total.ComputedAt = row.ComputedAt
	}
	return nil
}
