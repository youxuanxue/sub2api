package usagestats

import "time"

// HistoricalBillingSnapshot is a consistent read of the retained per-user daily
// aggregates. It intentionally does not depend on raw request retention.
type HistoricalBillingSnapshot struct {
	RegisteredAt time.Time
	Timezone     string
	GeneratedAt  time.Time
	Rows         []HistoricalBillingRow
}

type HistoricalBillingRow struct {
	Date                string `json:"date"`
	Platform            string `json:"platform"`
	Requests            int64  `json:"requests"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	CacheCreationTokens int64  `json:"cache_creation_tokens"`
	CacheReadTokens     int64  `json:"cache_read_tokens"`
	TotalTokens         int64  `json:"total_tokens"`
	// Money remains decimal text through JSON and exports; no float accumulation.
	ActualCost  string    `json:"actual_cost"`
	ComputedAt  time.Time `json:"computed_at"`
	Provisional bool      `json:"provisional"`
}

type HistoricalBillingQuery struct {
	StartDate   string
	EndDate     string
	Granularity string
	Platform    *string
}

type HistoricalBillingReport struct {
	UserID             int64                  `json:"user_id"`
	RegisteredAt       time.Time              `json:"registered_at"`
	StartDate          string                 `json:"start_date"`
	EndDate            string                 `json:"end_date"`
	Timezone           string                 `json:"timezone"`
	Currency           string                 `json:"currency"`
	Granularity        string                 `json:"granularity"`
	Platform           *string                `json:"platform"`
	Platforms          []string               `json:"platforms"`
	AvailableStartDate string                 `json:"available_start_date"`
	AvailableEndDate   string                 `json:"available_end_date"`
	GeneratedAt        time.Time              `json:"generated_at"`
	Rows               []HistoricalBillingRow `json:"rows"`
	Totals             HistoricalBillingRow   `json:"totals"`
}
