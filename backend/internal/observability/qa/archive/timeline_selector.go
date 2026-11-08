package archive

import "strings"

const (
	IntegrityCommitMismatch                  = "commit_mismatch"
	IntegrityCommitExistenceUnknown          = "commit_existence_unknown"
	IntegrityRestoreFailed                   = "restore_failed"
	IntegritySourceUnavailableAfterRetention = "source_unavailable_after_retention"
	// Codes persisted by the retired S3 builder/verifier. Historical rows still
	// carry them, so they stay terminal.
	IntegrityMissingEvidence = "missing_evidence"
	IntegrityCorruptArtifact = "corrupt_artifact"
)

type CatchupHourStatus struct {
	Exists                bool
	ShardID               int64
	State                 string
	RestoreVerified       bool
	VerificationErrorCode string
	SourceExists          bool
	UncoveredSourceExists bool
}

func IsTerminalArchiveFailure(code string) bool {
	switch strings.TrimSpace(code) {
	case IntegrityMissingEvidence,
		IntegrityCorruptArtifact,
		IntegrityCommitMismatch,
		IntegrityRestoreFailed,
		IntegritySourceUnavailableAfterRetention:
		return true
	default:
		return false
	}
}
