package handler

import (
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// QAHandler exposes self-service endpoints over the captured qa_records
// owned by the authenticated user.
//
// Issue #59 / docs/approved/ops_xx.md §2: the "100% QA Capture" capability
// was approved with capture-side already shipped (Service.ExportUserData was
// implemented at observability/qa/service.go but never wired to a route).
// This handler closes that gap. Auth is by user-scope JWT (NOT admin) and
// every query is scoped to `WHERE user_id = <subject.UserID>` at the
// service layer, so an authenticated user can never read another user's
// captures even if they craft `synth_session_id` values.
type QAHandler struct {
	service *qa.Service
}

// NewQAHandler wires the user-facing QA export handler. Returns a handler
// even when service is nil so the route can advertise a stable error
// (rather than 404 → operator confusion) when QA capture is disabled in
// the running environment.
func NewQAHandler(service *qa.Service) *QAHandler {
	return &QAHandler{service: service}
}

// ExportSelfRequest is the JSON body accepted by POST
// /api/v1/users/me/qa/export.
//
// The M0 dual-CC client (`m0/runtime/tokenkey.py`) sends:
//
//	{"synth_session_id": "m0-...-...", "format": "json"}
//
// `format` is reserved for future variants; today we always emit a zip
// containing `qa_records.jsonl` (one Ent JSON-encoded record per line)
// because the M0 verifiers stream-decode line-by-line.
type ExportSelfRequest struct {
	SynthSessionID string `json:"synth_session_id"`
	SynthRole      string `json:"synth_role"`
	Format         string `json:"format"`
	SinceRFC3339   string `json:"since"`
	UntilRFC3339   string `json:"until"`
}

// ExportSelfResponse mirrors the contract documented in issue #59.
// `download_url` is a 24h presigned S3 URL (or `file://` path on
// localfs blob store, for dev / single-replica ops). `record_count`
// is a convenience for the caller so they can fail fast when the
// session id matched no rows (the most common synth-pipeline mistake).
type ExportSelfResponse struct {
	DownloadURL string    `json:"download_url"`
	ExpiresAt   time.Time `json:"expires_at"`
	RecordCount int       `json:"record_count"`
}

// ExportSelf handles POST /api/v1/users/me/qa/export.
//
// Defaults when the body is empty: last 24h of the caller's traffic.
// When `synth_session_id` is provided it overrides the time window
// (synth sessions can legitimately span the default window if a turn
// blocks on a long upstream call).
func (h *QAHandler) ExportSelf(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}

	if h == nil || h.service == nil || !h.service.Enabled() {
		response.Error(c, 503, "QA capture is disabled in this environment")
		return
	}

	req := ExportSelfRequest{}
	// Body is optional: M0 always sends one, GDPR-style "give me all my
	// recent data" can POST with no body. ShouldBindJSON returns EOF on
	// empty bodies which we explicitly accept.
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
		// Default window: trailing 24h. Bounded to defend against an
		// authenticated user accidentally exporting their entire history
		// in one shot (the underlying service supports it for GDPR but
		// the user-facing endpoint should be cheap by default).
		until := time.Now().UTC()
		since := until.Add(-24 * time.Hour)
		if v := strings.TrimSpace(req.SinceRFC3339); v != "" {
			parsed, err := time.Parse(time.RFC3339, v)
			if err != nil {
				response.BadRequest(c, "Invalid since: must be RFC3339")
				return
			}
			since = parsed
		}
		if v := strings.TrimSpace(req.UntilRFC3339); v != "" {
			parsed, err := time.Parse(time.RFC3339, v)
			if err != nil {
				response.BadRequest(c, "Invalid until: must be RFC3339")
				return
			}
			until = parsed
		}
		if !until.After(since) {
			response.BadRequest(c, "until must be after since")
			return
		}
		filter.Since = since
		filter.Until = until
	}

	result, err := h.service.ExportUserDataWithFilter(c.Request.Context(), subject.UserID, filter)
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
