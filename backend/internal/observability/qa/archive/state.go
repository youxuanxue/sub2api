package archive

// Shard state machine (historical §14.1; S3 lifecycle design deleted).
const (
	MaintenanceAdvisoryLockID int64 = 0x51414D41 // 'QAMA'

	StatePending           = "pending"
	StateWriting           = "writing"
	StateVerified          = "verified"
	StateCommitted         = "committed"
	StateFailed            = "failed"
	StateExpiredUnarchived = "expired_unarchived"
	StateGapRecorded       = "gap_recorded"
)
