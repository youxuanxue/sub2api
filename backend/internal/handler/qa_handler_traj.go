package handler

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

func (h *QAHandler) ExportSelfTrajectory(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
		return
	}

	// Per-user authorization backstop behind the UI visibility gate: the
	// export entry is hidden unless the admin set users.traj_export_enabled,
	// but never trust the client — re-check before doing any work.
	authorized, err := h.service.UserTrajExportEnabled(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !authorized {
		response.Forbidden(c, "Conversation export is not enabled for this account")
		return
	}

	req := ExportSelfRequest{}
	if c.Request != nil && c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "Invalid request: "+err.Error())
			return
		}
	}

	filter := qa.ExportFilter{
		SynthSessionID: strings.TrimSpace(req.SynthSessionID),
		SynthRole:      strings.TrimSpace(req.SynthRole),
		APIKeyID:       req.APIKeyID,
		// No platform pin: the traj v2 projector now dispatches per record by
		// wire shape (trajectory.WireShapeForRecord) and reconstructs anthropic /
		// openai / gemini / antigravity / kiro / newapi faithfully, skipping
		// non-conversation (Unknown-shape) records. A per-key export is already
		// single-platform via APIKeyID, and the export chip is gated server-side
		// to engine.TrajProjectablePlatforms() (see /auth/me traj_export_platforms),
		// so a non-projectable key never reaches here with a misleading zip.
		Format: strings.TrimSpace(req.Format),
	}
	// Per-key export ("导出该 Key 的对话记录") drops the trailing-24h default
	// window and returns the key's full retained trajectory; the data set is
	// already bounded by qa_capture.retention_days. The default 24h window
	// only guards the unscoped "export my recent traffic" path.
	if filter.SynthSessionID == "" && filter.APIKeyID == nil {
		filter.Until = time.Now().UTC()
		filter.Since = filter.Until.Add(-defaultQAExportWindow)
	}

	// Async: enqueue the export on the dedicated single-worker pool and return a
	// job id immediately. The heavy work (blob reads + zip build) runs off the
	// request path so it can never block or starve the gateway — the synchronous,
	// in-memory build is what hung prod on 2026-06-17. The client polls
	// GET .../traj/export/jobs/:job_id and downloads when status == done.
	job, err := h.service.EnqueueExport(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		if errors.Is(err, qa.ErrExportBusy) {
			response.Error(c, http.StatusTooManyRequests, "An export is already being prepared, please try again shortly")
			return
		}
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.trajExportJobResponse(c, job))
}

// GetSelfTrajectoryExportJob handles GET
// /api/v1/users/me/qa/traj/export/jobs/:job_id — the poll endpoint for an async
// export. Returns the job status and, once done, a client-usable download URL.
func (h *QAHandler) GetSelfTrajectoryExportJob(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
		return
	}

	jobID := strings.Trim(c.Param("job_id"), "/")
	job, found := h.service.GetExportJob(c.Request.Context(), subject.UserID, jobID)
	if !found {
		response.NotFound(c, "Export job not found or expired")
		return
	}
	response.Success(c, h.trajExportJobResponse(c, job))
}

// ListSelfTrajectoryExports handles GET /api/v1/users/me/qa/traj/exports —
// the "my exports" panel feed. Optional ?api_key_id= scopes to one key. Returns
// the user's recent export jobs newest-first; done & unexpired ones carry a
// download URL so the panel can offer re-download without re-running the export.
func (h *QAHandler) ListSelfTrajectoryExports(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
		return
	}

	var apiKeyID *int64
	if raw := strings.TrimSpace(c.Query("api_key_id")); raw != "" {
		id, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil {
			response.BadRequest(c, "Invalid api_key_id")
			return
		}
		apiKeyID = &id
	}

	jobs, err := h.service.ListExports(c.Request.Context(), subject.UserID, apiKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	out := make([]ExportJobResponse, 0, len(jobs))
	for i := range jobs {
		out = append(out, h.trajExportJobResponse(c, jobs[i]))
	}
	response.Success(c, ListExportJobsResponse{Exports: out})
}

// trajExportJobResponse maps a service ExportJob to the wire shape, converting a
// localfs file:// download URL into the proxied HTTP path the client can fetch.
func (h *QAHandler) trajExportJobResponse(c *gin.Context, job qa.ExportJob) ExportJobResponse {
	resp := ExportJobResponse{
		JobID:       job.ID,
		Status:      string(job.Status),
		Kind:        job.Kind,
		APIKeyID:    job.APIKeyID,
		RecordCount: job.RecordCount,
		Error:       job.Error,
		CreatedAt:   job.CreatedAt,
	}
	if job.Status == qa.ExportJobDone && job.DownloadURL != "" {
		resp.DownloadURL = h.clientTrajDownloadURL(c, job.DownloadURL, job.StorageKey)
		resp.ExpiresAt = job.ExpiresAt
	}
	return resp
}

func (h *QAHandler) DownloadSelfTrajectoryExport(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
		return
	}

	key := strings.TrimPrefix(c.Param("key"), "/")
	body, err := h.service.DownloadUserTrajectoryExport(c.Request.Context(), subject.UserID, key)
	if err != nil {
		switch {
		case errors.Is(err, fs.ErrPermission):
			response.Forbidden(c, "Export not owned by authenticated user")
		case errors.Is(err, fs.ErrNotExist):
			response.NotFound(c, "Export not found or expired")
		default:
			response.ErrorFrom(c, err)
		}
		return
	}

	filename := "trajectory_export.zip"
	parts := strings.Split(strings.TrimRight(key, "/"), "/")
	if len(parts) > 0 && strings.HasSuffix(parts[len(parts)-1], ".zip") {
		filename = parts[len(parts)-1]
	}
	c.DataFromReader(http.StatusOK, int64(len(body)), "application/zip", bytes.NewReader(body), map[string]string{
		"Content-Disposition": fmt.Sprintf(`attachment; filename="%s"`, filename),
	})
}

// ExportJobResponse is the async traj-export contract: POST returns {job_id,
// status:"pending"}; the poll GET returns the evolving status and, on done, a
// download_url + expires_at. error carries a machine code (no_records /
// export_failed / busy) for the UI to localize.
type ExportJobResponse struct {
	JobID       string    `json:"job_id"`
	Status      string    `json:"status"`
	Kind        string    `json:"kind,omitempty"` // manual | auto
	APIKeyID    *int64    `json:"api_key_id,omitempty"`
	DownloadURL string    `json:"download_url,omitempty"`
	ExpiresAt   time.Time `json:"expires_at,omitempty"`
	RecordCount int       `json:"record_count"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// ListExportJobsResponse is the "my exports" panel feed.
type ListExportJobsResponse struct {
	Exports []ExportJobResponse `json:"exports"`
}

func (h *QAHandler) clientTrajDownloadURL(c *gin.Context, downloadURL, storageKey string) string {
	if !strings.HasPrefix(downloadURL, "file://") || storageKey == "" {
		return downloadURL
	}
	return absoluteRequestURL(c, "/api/v1/users/me/qa/traj/exports/"+storageKey)
}
