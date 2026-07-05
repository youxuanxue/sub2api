package domain

import "time"

// UsageCleanupFilters defines cleanup task filter criteria.
type UsageCleanupFilters struct {
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	UserID      *int64    `json:"user_id,omitempty"`
	APIKeyID    *int64    `json:"api_key_id,omitempty"`
	AccountID   *int64    `json:"account_id,omitempty"`
	GroupID     *int64    `json:"group_id,omitempty"`
	Model       *string   `json:"model,omitempty"`
	RequestType *int16    `json:"request_type,omitempty"`
	Stream      *bool     `json:"stream,omitempty"`
	BillingType *int8     `json:"billing_type,omitempty"`
}

// UsageCleanupTask represents a usage-log cleanup job.
type UsageCleanupTask struct {
	ID          int64
	Status      string
	Filters     UsageCleanupFilters
	CreatedBy   int64
	DeletedRows int64
	ErrorMsg    *string
	CanceledBy  *int64
	CanceledAt  *time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
