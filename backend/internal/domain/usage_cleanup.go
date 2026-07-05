package domain

// Usage cleanup task status constants.
//
// These are used by both service/ (business logic) and repository/ (persistence),
// so they belong in domain/.
const (
	UsageCleanupStatusPending   = "pending"
	UsageCleanupStatusRunning   = "running"
	UsageCleanupStatusSucceeded = "succeeded"
	UsageCleanupStatusFailed    = "failed"
	UsageCleanupStatusCanceled  = "canceled"
)
