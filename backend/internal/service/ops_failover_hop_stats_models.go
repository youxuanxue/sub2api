package service

import "time"

// OpsFailoverHopStatsFilter scopes the per-account "wasted failover hops" KPI.
// TopN-only (no pagination): the dashboard card shows the worst N accounts.
type OpsFailoverHopStatsFilter struct {
	TimeRange string
	StartTime time.Time
	EndTime   time.Time

	Platform string
	GroupID  *int64

	TopN int
}

// OpsFailoverHopStatsItem is one account's failover-hop waste over the window.
// A "wasted hop" is one failed upstream attempt on a request that STILL succeeded
// (recovered-200): the request first landed on a hot account, ate an error, and
// failed over. total_failover_hops counts only account-switch events; total_wasted
// _attempts counts every wasted upstream round-trip regardless of kind.
type OpsFailoverHopStatsItem struct {
	AccountID                   int64    `json:"account_id"`
	AccountName                 string   `json:"account_name"`
	Platform                    string   `json:"platform"`
	RecoveredCount              int64    `json:"recovered_count"`
	TotalFailoverHops           int64    `json:"total_failover_hops"`
	TotalWastedAttempts         int64    `json:"total_wasted_attempts"`
	AvgFailoverHopsPerRecovered *float64 `json:"avg_failover_hops_per_recovered"`
}

type OpsFailoverHopStatsResponse struct {
	TimeRange string    `json:"time_range"`
	StartTime time.Time `json:"start_time"`
	EndTime   time.Time `json:"end_time"`

	Platform string `json:"platform,omitempty"`
	GroupID  *int64 `json:"group_id,omitempty"`

	Items []*OpsFailoverHopStatsItem `json:"items"`
	Total int64                      `json:"total"`
	TopN  int                        `json:"top_n"`
}
