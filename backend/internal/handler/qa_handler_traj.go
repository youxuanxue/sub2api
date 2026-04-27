package handler

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
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

	result, err := h.service.ExportUserTrajectoryData(c.Request.Context(), subject.UserID, filter)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}

	response.Success(c, ExportSelfResponse{
		DownloadURL: h.clientTrajectoryDownloadURL(c, result),
		ExpiresAt:   result.ExpiresAt,
		RecordCount: result.RecordCount,
	})
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

func (h *QAHandler) clientTrajectoryDownloadURL(c *gin.Context, result *qa.ExportResult) string {
	if result == nil {
		return ""
	}
	if !strings.HasPrefix(result.DownloadURL, "file://") || result.StorageKey == "" {
		return result.DownloadURL
	}
	return absoluteRequestURL(c, "/api/v1/users/me/qa/traj/exports/"+result.StorageKey)
}
