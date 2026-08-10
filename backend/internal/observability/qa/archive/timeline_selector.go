package archive

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	IntegrityCommitMismatch                  = "commit_mismatch"
	IntegrityRestoreFailed                   = "restore_failed"
	IntegritySourceUnavailableAfterRetention = "source_unavailable_after_retention"

	CatchupDispositionReconcile                       = "reconcile"
	CatchupDispositionSourceUnavailableAfterRetention = IntegritySourceUnavailableAfterRetention
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

type CatchupSelection struct {
	Window      Window
	ShardID     int64
	Disposition string
}

type catchupTimelineControl interface {
	ReadForwardCutover(context.Context, *sql.Conn) (ForwardCutover, bool, error)
	InspectCatchupHour(context.Context, *sql.Conn, Window) (CatchupHourStatus, error)
	MarkSourceUnavailableAfterRetention(context.Context, *sql.Conn, Window) (int64, error)
}

func SelectOldestCatchup(
	ctx context.Context,
	conn *sql.Conn,
	control catchupTimelineControl,
	normal Window,
	retentionCutoff time.Time,
) (CatchupSelection, bool, error) {
	if control == nil {
		return CatchupSelection{}, false, fmt.Errorf("select catchup: nil control store")
	}
	normal.Start = normal.Start.UTC()
	normal.End = normal.End.UTC()
	if !normal.End.Equal(normal.Start.Add(time.Hour)) {
		return CatchupSelection{}, false, fmt.Errorf("select catchup: normal window must be one UTC hour")
	}
	cutover, ok, err := control.ReadForwardCutover(ctx, conn)
	if err != nil {
		return CatchupSelection{}, false, fmt.Errorf("select catchup: read forward cutover: %w", err)
	}
	if !ok {
		return CatchupSelection{}, false, fmt.Errorf("select catchup: forward cutover is not set")
	}
	first := cutover.Window.End.UTC()
	if normal.Start.Before(first) {
		return CatchupSelection{}, false, fmt.Errorf("select catchup: normal window precedes forward cutover")
	}
	cutoff := retentionCutoff.UTC()
	for start := first; start.Before(normal.Start); start = start.Add(time.Hour) {
		window := Window{Start: start, End: start.Add(time.Hour)}
		status, err := control.InspectCatchupHour(ctx, conn, window)
		if err != nil {
			return CatchupSelection{}, false, fmt.Errorf("select catchup %s: %w", start.Format(time.RFC3339), err)
		}
		if status.State == StateFailed && IsTerminalArchiveFailure(status.VerificationErrorCode) {
			continue
		}
		switch status.State {
		case "", StatePending, StateWriting, StateVerified, StateFailed:
			if !status.SourceExists && start.Before(cutoff) {
				shardID, err := control.MarkSourceUnavailableAfterRetention(ctx, conn, window)
				if err != nil {
					return CatchupSelection{}, false, fmt.Errorf("classify catchup %s: %w", start.Format(time.RFC3339), err)
				}
				return CatchupSelection{
					Window: window, ShardID: shardID,
					Disposition: CatchupDispositionSourceUnavailableAfterRetention,
				}, true, nil
			}
			return CatchupSelection{Window: window, ShardID: status.ShardID, Disposition: CatchupDispositionReconcile}, true, nil
		case StateCommitted:
			if !status.RestoreVerified || status.UncoveredSourceExists {
				return CatchupSelection{Window: window, ShardID: status.ShardID, Disposition: CatchupDispositionReconcile}, true, nil
			}
		default:
			return CatchupSelection{}, false, fmt.Errorf("select catchup %s: invalid shard state %q", start.Format(time.RFC3339), status.State)
		}
	}
	return CatchupSelection{}, false, nil
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
