package qa

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/ent/user"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

// autoExportHourUTC is the wall-clock hour the daily archive runs. 02:00 UTC is
// off the gateway's busy window; the run must land BEFORE the host localfs
// cleanup purges the day's capture blobs (coordinate the cleanup schedule to
// run later than this).
const autoExportHourUTC = 2

// StartAutoExportLoop launches the daily per-(user,key) archive goroutine. It
// archives the just-completed UTC day for every traj_export_enabled user that
// captured records that day, writing one idempotent dated zip per key to the
// export store. Gated by qa_capture.auto_export_enabled (see NewService).
func (s *Service) StartAutoExportLoop() {
	s.ensureExportPool()
	s.autoExportStop = make(chan struct{})
	go func() {
		for {
			now := time.Now().UTC()
			next := time.Date(now.Year(), now.Month(), now.Day(), autoExportHourUTC, 0, 0, 0, time.UTC)
			if !next.After(now) {
				next = next.Add(24 * time.Hour)
			}
			timer := time.NewTimer(next.Sub(now))
			select {
			case <-s.autoExportStop:
				timer.Stop()
				return
			case <-timer.C:
				// Archive the day that just ended (the one the host cleanup is
				// about to purge from localfs).
				dayStart := next.Add(-24 * time.Hour).Truncate(24 * time.Hour)
				s.runAutoExportOnce(context.Background(), dayStart)
			}
		}
	}()
}

// runAutoExportOnce enqueues an auto-export for every (user, api_key) pair that
// captured records in [dayStart, dayStart+24h) and whose user has trajectory
// export enabled. Idempotent: same-day re-runs upsert the same job rows/objects.
func (s *Service) runAutoExportOnce(ctx context.Context, dayStart time.Time) {
	dayStart = dayStart.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)

	enabledIDs, err := s.client.User.Query().
		Where(user.TrajExportEnabledEQ(true)).
		IDs(ctx)
	if err != nil {
		logger.L().Warn("qa auto-export: list enabled users failed", zap.Error(err))
		return
	}
	if len(enabledIDs) == 0 {
		return
	}
	enabled := make(map[int64]bool, len(enabledIDs))
	for _, id := range enabledIDs {
		enabled[id] = true
	}

	var pairs []struct {
		UserID   int64 `json:"user_id"`
		APIKeyID int64 `json:"api_key_id"`
	}
	if err := s.client.QARecord.Query().
		Where(
			qarecord.CreatedAtGTE(dayStart),
			qarecord.CreatedAtLT(dayEnd),
			qarecord.UserIDIn(enabledIDs...),
		).
		GroupBy(qarecord.FieldUserID, qarecord.FieldAPIKeyID).
		Scan(ctx, &pairs); err != nil {
		logger.L().Warn("qa auto-export: distinct (user,key) scan failed", zap.Error(err))
		return
	}

	var enqueued, failed int
	for _, p := range pairs {
		if !enabled[p.UserID] {
			continue
		}
		if _, err := s.EnqueueAutoExport(ctx, p.UserID, p.APIKeyID, dayStart); err != nil {
			failed++
			logger.L().Warn("qa auto-export: enqueue failed",
				zap.Int64("user_id", p.UserID), zap.Int64("api_key_id", p.APIKeyID), zap.Error(err))
			continue
		}
		enqueued++
	}
	logger.L().Info("qa auto-export: daily archive enqueued",
		zap.Time("day", dayStart), zap.Int("enqueued", enqueued), zap.Int("failed", failed))
}
