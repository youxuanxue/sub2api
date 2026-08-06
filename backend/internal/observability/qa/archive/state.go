package archive

// Shard state machine (design-prod-qa-24h-s3-lifecycle.md §14.1).
const (
	StatePending           = "pending"
	StateWriting           = "writing"
	StateVerified          = "verified"
	StateCommitted         = "committed"
	StateFailed            = "failed"
	StateExpiredUnarchived = "expired_unarchived"
	StateGapRecorded       = "gap_recorded"
)

// MutableShardStates are safe to refresh counts/prefix during archive-only maintenance.
func MutableShardStates() []string {
	return []string{StatePending, StateFailed}
}
