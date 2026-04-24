package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// defaultQAExportWindow bounds the data set returned when the caller does
// not narrow by synth_session_id. Picked to cover one M0 run plus
// generous slack; large enough that a casual user export still works,
// small enough that "POST /qa/export with empty body" can never become
// a "give me my entire history" foot-gun.
const defaultQAExportWindow = 24 * time.Hour

// QAHandler exposes the user-facing self-export endpoint over the
// qa_records owned by the authenticated user. Issue #59 /
// docs/approved/ops_xx.md §2 — closes the half-shipped "100% QA
// Capture" capability where the capture path wrote rows but no
// user-facing read path existed.
//
// Auth is by user-scope JWT (NOT admin); the service layer always emits
// `WHERE user_id = subject.UserID` so guessing another user's
// synth_session_id still returns zero rows.
type QAHandler struct {
	service *qa.Service
}

// NewQAHandler wires the user-facing QA export handler. Tolerates a nil
// service so the route can return a stable 503 (rather than 404 →
// operator confusion) when QA capture is disabled in this environment.
func NewQAHandler(service *qa.Service) *QAHandler {
	return &QAHandler{service: service}
}

// ExportSelfRequest is the JSON body accepted by POST
// /api/v1/users/me/qa/export. Matches the M0 dual-CC client contract at
// `m0/runtime/tokenkey.py::export_user_qa()`. Unknown JSON fields (e.g.
// the M0 client's `format: "json"`) are silently ignored — today we
// always emit a zip containing `qa_records.jsonl`.
type ExportSelfRequest struct {
	SynthSessionID string `json:"synth_session_id"`
	SynthRole      string `json:"synth_role"`
}

// ExportSelfResponse mirrors the contract documented in issue #59.
// `record_count` lets the M0 client distinguish "session not yet
// captured, retry" from "captured but empty"; without it the only
// signal would be opening the zip.
type ExportSelfResponse struct {
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
	RecordCount int       `json:"record_count"`
}

// ExportSelf handles POST /api/v1/users/me/qa/export.
//
// Behavior is deliberately minimal (one canonical path):
//   - synth_session_id set → ignore time window, scope to that session
//   - synth_session_id empty → trailing defaultQAExportWindow of caller's traffic
func (h *QAHandler) ExportSelf(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
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
	}
	if filter.SynthSessionID == "" {
		filter.Until = time.Now().UTC()
		filter.Since = filter.Until.Add(-defaultQAExportWindow)
	}

	result, err := h.service.ExportUserData(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ExportSelfResponse{
		DownloadURL: result.DownloadURL,
		ExpiresAt:   result.ExpiresAt,
		RecordCount: result.RecordCount,
	})
}
