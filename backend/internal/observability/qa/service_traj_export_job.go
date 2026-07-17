package qa

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/qaexportjob"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/alitto/pond/v2"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrExportBusy is returned by EnqueueExport when the single export worker's
// queue is full — the caller should surface a "try again shortly" to the user
// rather than pile on more work.
var ErrExportBusy = errors.New("export queue is full")

// ExportJobStatus is the lifecycle of an async trajectory export.
type ExportJobStatus string

const (
	ExportJobPending ExportJobStatus = "pending"
	ExportJobRunning ExportJobStatus = "running"
	ExportJobDone    ExportJobStatus = "done"
	ExportJobFailed  ExportJobStatus = "failed"
)

// export kinds — see exportStorageKey for how each maps to the blob-store key.
const (
	exportKindManual = "manual"
	exportKindAuto   = "auto"
)

const (
	// exportQueueSize bounds pending exports behind the single worker.
	exportQueueSize = 8
	// exportJobMaxRuntime bounds one export so a pathological run can't hold the
	// worker forever.
	exportJobMaxRuntime = 30 * time.Minute
	// autoExportArtifactTTL is how long a daily auto archive stays downloadable;
	// matches the recommended S3 lifecycle expiration for the traj-exports prefix.
	autoExportArtifactTTL = 7 * 24 * time.Hour
	// exportListLimit caps the "my exports" panel listing per (user[, key]).
	exportListLimit = 50
)

// Machine-readable error codes the frontend maps to localized toasts, so we
// don't leak internal error text to the UI.
const (
	exportErrNoRecords   = "no_records"
	exportErrBusy        = "busy"
	exportErrFailed      = "export_failed"
	exportErrInterrupted = "interrupted"
)

// ExportJob is the DTO returned to the handler/UI. The durable record of truth
// is the qa_export_jobs row; this is a snapshot mapped from it (the prior
// in-memory map was wiped on every redeploy, orphaning the download).
type ExportJob struct {
	ID          string          `json:"job_id"`
	UserID      int64           `json:"-"`
	APIKeyID    *int64          `json:"api_key_id,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Status      ExportJobStatus `json:"status"`
	DownloadURL string          `json:"download_url,omitempty"`
	StorageKey  string          `json:"-"`
	ExpiresAt   time.Time       `json:"expires_at,omitempty"`
	RecordCount int             `json:"record_count"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// ensureExportPool lazily provisions the single-worker export pool for Services
// built via NewServiceForTest (which skip the production constructor).
func (s *Service) ensureExportPool() {
	s.exportPoolMu.Lock()
	defer s.exportPoolMu.Unlock()
	if s.exportPool == nil {
		s.exportPool = pond.NewPool(1, pond.WithQueueSize(exportQueueSize))
	}
}

// EnqueueExport registers a user-initiated ("立即导出") export job, persists it,
// and submits it to the single export worker, returning immediately with a job
// snapshot. The heavy work (blob reads, zip build) runs off the request path so
// it can never block or starve the gateway — the synchronous in-memory build is
// what hung prod on 2026-06-17. Returns ErrExportBusy when the worker queue is
// full.
func (s *Service) EnqueueExport(ctx context.Context, userID int64, filter ExportFilter) (ExportJob, error) {
	s.ensureExportPool()
	filter.Kind = exportKindManual
	jobID := uuid.New().String()

	create := s.client.QAExportJob.Create().
		SetJobID(jobID).
		SetUserID(userID).
		SetStatus(string(ExportJobPending)).
		SetExportKind(exportKindManual).
		SetFormat(exportFormatOrDefault(filter.Format))
	if filter.APIKeyID != nil {
		create = create.SetAPIKeyID(*filter.APIKeyID)
	}
	if _, err := create.Save(ctx); err != nil {
		return ExportJob{}, err
	}

	_, ok := s.exportPool.TrySubmit(func() { s.runExportJob(jobID, userID, filter) })
	if !ok {
		s.setExportJobFailed(jobID, exportErrBusy)
		snap, _ := s.GetExportJob(ctx, userID, jobID)
		return snap, ErrExportBusy
	}
	snap, _ := s.GetExportJob(ctx, userID, jobID)
	return snap, nil
}

// ArchiveAuto registers/refreshes the daily archive for one (user, key, day)
// and runs it to completion synchronously. The job_id is deterministic so a
// same-day re-run upserts the same row (idempotent) instead of duplicating;
// window covers [dayStart, dayEnd).
//
// Unlike the user-initiated EnqueueExport (which TrySubmits and returns busy so
// the gateway never blocks), the daily cron calls this sequentially per pair and
// Submit().Wait()s each one. Submitting one task at a time can never overflow the
// worker's bounded queue, so no archive is ever dropped — and routing through the
// SAME single worker keeps "at most one export materializes at a time" intact
// (no auto/manual I/O overlap). The trade-off is the cron blocks per archive,
// which is exactly what we want for an off-peak background sweep.
func (s *Service) ArchiveAuto(ctx context.Context, userID, apiKeyID int64, dayStart time.Time) (ExportJob, error) {
	s.ensureExportPool()
	dayStart = dayStart.UTC().Truncate(24 * time.Hour)
	dayEnd := dayStart.Add(24 * time.Hour)
	jobID := autoExportJobID(userID, apiKeyID, dayStart)
	filter := ExportFilter{
		APIKeyID: &apiKeyID,
		Platform: "anthropic",
		Format:   "v2",
		Kind:     exportKindAuto,
		Since:    dayStart,
		Until:    dayEnd,
	}

	// Upsert the row to pending — a re-run of an already-archived day just
	// rebuilds it (overwriting the same dated S3 object).
	if err := s.client.QAExportJob.Create().
		SetJobID(jobID).
		SetUserID(userID).
		SetAPIKeyID(apiKeyID).
		SetStatus(string(ExportJobPending)).
		SetExportKind(exportKindAuto).
		SetFormat("v2").
		SetWindowStart(dayStart).
		SetWindowEnd(dayEnd).
		OnConflictColumns(qaexportjob.FieldJobID).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return ExportJob{}, err
	}

	// Blocking submit + wait: backpressure instead of drop, single-worker serialized.
	// Wait() only returns non-nil if the worker task panicked (runExportJob records
	// its own terminal state); log that so a crashed archive isn't silent.
	if werr := s.exportPool.Submit(func() { s.runExportJob(jobID, userID, filter) }).Wait(); werr != nil {
		logger.L().Warn("qa auto-export: worker task error", zap.String("job_id", jobID), zap.Error(werr))
	}
	snap, _ := s.GetExportJob(ctx, userID, jobID)
	return snap, nil
}

// runExportJob is the worker body shared by the manual (EnqueueExport) and auto
// (ArchiveAuto) paths. It marks the job running, builds the zip off the request
// path (bounded by exportJobMaxRuntime), and records the terminal state.
func (s *Service) runExportJob(jobID string, userID int64, filter ExportFilter) {
	// Background (not request) context so the export completes even after the
	// client disconnects — bounded by exportJobMaxRuntime.
	ctx, cancel := context.WithTimeout(context.Background(), exportJobMaxRuntime)
	defer cancel()

	if _, err := s.client.QAExportJob.Update().
		Where(qaexportjob.JobIDEQ(jobID)).
		SetStatus(string(ExportJobRunning)).
		Save(ctx); err != nil {
		logger.L().Warn("traj export: mark running failed", zap.String("job_id", jobID), zap.Error(err))
	}

	res, err := s.ExportUserTrajectoryData(ctx, userID, filter)
	if err != nil {
		logger.L().Warn("traj export job failed",
			zap.String("job_id", jobID), zap.Int64("user_id", userID), zap.Error(err))
		s.setExportJobFailed(jobID, exportJobErrorCode(err))
		return
	}
	// expires_at follows the artifact's retention (auto archives live 7 days in
	// S3; manual exports 24h), NOT the presigned-URL lifetime — the URL is
	// re-signed fresh on every list, so the row's expiry is what gates how long
	// the panel keeps offering the download.
	if _, uerr := s.client.QAExportJob.Update().
		Where(qaexportjob.JobIDEQ(jobID)).
		SetStatus(string(ExportJobDone)).
		SetStorageKey(res.StorageKey).
		SetRecordCount(res.RecordCount).
		SetExpiresAt(time.Now().UTC().Add(exportArtifactTTL(filter.Kind))).
		Save(context.Background()); uerr != nil {
		logger.L().Warn("traj export: mark done failed", zap.String("job_id", jobID), zap.Error(uerr))
	}
}

// exportArtifactTTL is how long a finished export stays downloadable: auto daily
// archives are retained 7 days (matching the S3 lifecycle), manual exports 24h.
func exportArtifactTTL(kind string) time.Duration {
	if strings.EqualFold(strings.TrimSpace(kind), exportKindAuto) {
		return autoExportArtifactTTL
	}
	return presignedURLTTL
}

// GetExportJob returns a snapshot of the job if it exists and is owned by userID.
func (s *Service) GetExportJob(ctx context.Context, userID int64, id string) (ExportJob, bool) {
	m, err := s.client.QAExportJob.Query().
		Where(qaexportjob.JobIDEQ(strings.TrimSpace(id)), qaexportjob.UserIDEQ(userID)).
		Only(ctx)
	if err != nil {
		return ExportJob{}, false
	}
	return s.exportJobFromEnt(ctx, m), true
}

// ListExports returns the user's export jobs (optionally scoped to one api key),
// newest first, for the "my exports" panel. Done & unexpired jobs carry a fresh
// download URL; expired/failed ones carry none.
func (s *Service) ListExports(ctx context.Context, userID int64, apiKeyID *int64) ([]ExportJob, error) {
	q := s.client.QAExportJob.Query().Where(qaexportjob.UserIDEQ(userID))
	if apiKeyID != nil {
		q = q.Where(qaexportjob.APIKeyIDEQ(*apiKeyID))
	}
	rows, err := q.Order(ent.Desc(qaexportjob.FieldCreatedAt)).Limit(exportListLimit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ExportJob, 0, len(rows))
	for _, m := range rows {
		out = append(out, s.exportJobFromEnt(ctx, m))
	}
	return out, nil
}

// ReconcileOrphanedExports fails any job left pending/running by a previous
// process. The in-process worker that owned it is gone after a restart/redeploy,
// so without this the row (and the UI poll) would hang forever.
func (s *Service) ReconcileOrphanedExports(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	n, err := s.client.QAExportJob.Update().
		Where(qaexportjob.StatusIn(string(ExportJobPending), string(ExportJobRunning))).
		SetStatus(string(ExportJobFailed)).
		SetError(exportErrInterrupted).
		Save(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		logger.L().Info("qa export: failed orphaned jobs on startup", zap.Int("count", n))
	}
	return nil
}

func (s *Service) setExportJobFailed(jobID, code string) {
	if _, err := s.client.QAExportJob.Update().
		Where(qaexportjob.JobIDEQ(jobID)).
		SetStatus(string(ExportJobFailed)).
		SetError(code).
		Save(context.Background()); err != nil {
		logger.L().Warn("traj export: mark failed failed", zap.String("job_id", jobID), zap.Error(err))
	}
}

// exportJobFromEnt maps a persisted row to the DTO, recomputing a fresh download
// URL for done & unexpired jobs (presigned URLs are ephemeral, so we never store
// them — only the storage_key).
func (s *Service) exportJobFromEnt(ctx context.Context, m *ent.QAExportJob) ExportJob {
	ej := ExportJob{
		ID:          m.JobID,
		UserID:      m.UserID,
		APIKeyID:    m.APIKeyID,
		Kind:        m.ExportKind,
		Status:      ExportJobStatus(m.Status),
		StorageKey:  m.StorageKey,
		RecordCount: m.RecordCount,
		CreatedAt:   m.CreatedAt,
		UpdatedAt:   m.UpdatedAt,
	}
	if m.Error != nil {
		ej.Error = *m.Error
	}
	if m.ExpiresAt != nil {
		ej.ExpiresAt = *m.ExpiresAt
	}
	if ej.Status == ExportJobDone && m.StorageKey != "" {
		expired := m.ExpiresAt != nil && time.Now().UTC().After(*m.ExpiresAt)
		if !expired && s.exportStore != nil {
			if url, err := s.exportStore.PresignURL(ctx, m.StorageKey, presignedURLTTL); err == nil {
				ej.DownloadURL = url
			}
		}
	}
	return ej
}

// exportJobErrorCode maps an internal export error to a machine-readable code
// for the UI (no raw internal text leaks to the user).
func exportJobErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "no rows") {
		return exportErrNoRecords
	}
	return exportErrFailed
}

func exportFormatOrDefault(f string) string {
	if strings.TrimSpace(f) == "" {
		return "v2"
	}
	return f
}

func autoExportJobID(userID, apiKeyID int64, dayStart time.Time) string {
	return "auto:" + strconv.FormatInt(userID, 10) + ":" + strconv.FormatInt(apiKeyID, 10) + ":" + dayStart.UTC().Format("2006-01-02")
}
