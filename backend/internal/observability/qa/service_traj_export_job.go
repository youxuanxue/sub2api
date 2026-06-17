package qa

import (
	"context"
	"errors"
	"strings"
	"time"

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

const (
	// exportQueueSize bounds pending exports behind the single worker.
	exportQueueSize = 8
	// exportJobMaxRuntime bounds one export so a pathological run can't hold the
	// worker forever.
	exportJobMaxRuntime = 30 * time.Minute
	// exportJobTTL is how long a finished job stays queryable (matches the
	// presigned download URL lifetime).
	exportJobTTL = presignedURLTTL
)

// Machine-readable error codes the frontend maps to localized toasts, so we
// don't leak internal error text to the UI.
const (
	exportErrNoRecords = "no_records"
	exportErrBusy      = "busy"
	exportErrFailed    = "export_failed"
)

// ExportJob is the in-memory status record for one async export. The zip itself
// lives in the blob store (durable); this only tracks progress.
type ExportJob struct {
	ID          string          `json:"job_id"`
	UserID      int64           `json:"-"`
	Status      ExportJobStatus `json:"status"`
	DownloadURL string          `json:"download_url,omitempty"`
	StorageKey  string          `json:"-"`
	ExpiresAt   time.Time       `json:"expires_at,omitempty"`
	RecordCount int             `json:"record_count"`
	Error       string          `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

func (j *ExportJob) snapshot() ExportJob { return *j }

// ensureExportInit lazily provisions the export pool/map for Services built via
// NewServiceForTest (which skips the production constructor).
func (s *Service) ensureExportInit() {
	s.exportJobsMu.Lock()
	defer s.exportJobsMu.Unlock()
	if s.exportJobs == nil {
		s.exportJobs = map[string]*ExportJob{}
	}
	if s.exportPool == nil {
		s.exportPool = pond.NewPool(1, pond.WithQueueSize(exportQueueSize))
	}
}

// EnqueueExport registers a background export job and submits it to the single
// export worker, returning immediately with a job snapshot. The heavy work (blob
// reads, zip build) runs off the request path so it can never block or starve
// the gateway. Returns ErrExportBusy (with a terminal Failed snapshot) when the
// worker queue is full.
func (s *Service) EnqueueExport(userID int64, filter ExportFilter) (ExportJob, error) {
	s.ensureExportInit()
	now := time.Now().UTC()
	job := &ExportJob{
		ID:        uuid.New().String(),
		UserID:    userID,
		Status:    ExportJobPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.exportJobsMu.Lock()
	s.pruneExportJobsLocked(now)
	s.exportJobs[job.ID] = job
	s.exportJobsMu.Unlock()

	_, ok := s.exportPool.TrySubmit(func() { s.runExportJob(job.ID, userID, filter) })
	if !ok {
		s.setExportJobFailed(job.ID, exportErrBusy)
		snap, _ := s.GetExportJob(userID, job.ID)
		return snap, ErrExportBusy
	}
	return job.snapshot(), nil
}

func (s *Service) runExportJob(jobID string, userID int64, filter ExportFilter) {
	s.updateExportJob(jobID, func(j *ExportJob) { j.Status = ExportJobRunning })
	// Background (not request) context so the export completes even after the
	// client disconnects — bounded by exportJobMaxRuntime.
	ctx, cancel := context.WithTimeout(context.Background(), exportJobMaxRuntime)
	defer cancel()

	res, err := s.ExportUserTrajectoryData(ctx, userID, filter)
	if err != nil {
		logger.L().Warn("traj export job failed",
			zap.String("job_id", jobID), zap.Int64("user_id", userID), zap.Error(err))
		s.setExportJobFailed(jobID, exportJobErrorCode(err))
		return
	}
	s.updateExportJob(jobID, func(j *ExportJob) {
		j.Status = ExportJobDone
		j.DownloadURL = res.DownloadURL
		j.StorageKey = res.StorageKey
		j.ExpiresAt = res.ExpiresAt
		j.RecordCount = res.RecordCount
	})
}

// GetExportJob returns a snapshot of the job if it exists and is owned by userID.
func (s *Service) GetExportJob(userID int64, id string) (ExportJob, bool) {
	s.exportJobsMu.Lock()
	defer s.exportJobsMu.Unlock()
	j, ok := s.exportJobs[strings.TrimSpace(id)]
	if !ok || j.UserID != userID {
		return ExportJob{}, false
	}
	return j.snapshot(), true
}

func (s *Service) updateExportJob(id string, fn func(*ExportJob)) {
	s.exportJobsMu.Lock()
	defer s.exportJobsMu.Unlock()
	if j, ok := s.exportJobs[id]; ok {
		fn(j)
		j.UpdatedAt = time.Now().UTC()
	}
}

func (s *Service) setExportJobFailed(id, code string) {
	s.updateExportJob(id, func(j *ExportJob) {
		j.Status = ExportJobFailed
		j.Error = code
	})
}

// pruneExportJobsLocked drops jobs past their TTL. Caller holds exportJobsMu.
func (s *Service) pruneExportJobsLocked(now time.Time) {
	for id, j := range s.exportJobs {
		if now.Sub(j.CreatedAt) > exportJobTTL {
			delete(s.exportJobs, id)
		}
	}
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
