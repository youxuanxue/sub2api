package domain

import "time"

// UserPlatformQuotaRecord is the cross-layer quota transport shape.
type UserPlatformQuotaRecord struct {
	UserID             int64
	Platform           string
	DailyLimitUSD      *float64
	WeeklyLimitUSD     *float64
	MonthlyLimitUSD    *float64
	DailyUsageUSD      float64
	WeeklyUsageUSD     float64
	MonthlyUsageUSD    float64
	DailyWindowStart   *time.Time
	WeeklyWindowStart  *time.Time
	MonthlyWindowStart *time.Time
}
