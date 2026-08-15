package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func (h *QAHandler) requireTrajectoryExportEnabled(c *gin.Context, userID int64) bool {
	if !h.service.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "QA capture is disabled in this environment")
		return false
	}
	authorized, err := h.service.UserTrajExportEnabled(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return false
	}
	if !authorized {
		response.Forbidden(c, "Conversation export is not enabled for this account")
		return false
	}
	return true
}

func (h *QAHandler) CreateSelfQABundle(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireTrajectoryExportEnabled(c, subject.UserID) {
		return
	}
	var request struct {
		APIKeyID int64 `json:"api_key_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.APIKeyID <= 0 {
		response.BadRequest(c, "api_key_id is required")
		return
	}
	authorized, err := h.service.UserMayExportAPIKey(c.Request.Context(), subject.UserID, request.APIKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !authorized {
		response.Forbidden(c, "API key is not eligible for QA history")
		return
	}
	job, err := h.service.CreateUserBundle(c.Request.Context(), subject.UserID, request.APIKeyID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}

func (h *QAHandler) GetSelfQABundle(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireTrajectoryExportEnabled(c, subject.UserID) {
		return
	}
	job, found, err := h.service.GetUserBundle(c.Request.Context(), subject.UserID, strings.TrimSpace(c.Param("job_id")))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !found {
		response.NotFound(c, "QA bundle not found")
		return
	}
	response.Success(c, job)
}

func (h *QAHandler) CreateSelfQABundleExport(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireTrajectoryExportEnabled(c, subject.UserID) {
		return
	}
	job, err := h.service.CreateUserBundleExport(c.Request.Context(), subject.UserID, strings.TrimSpace(c.Param("job_id")))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, job)
}

func (h *QAHandler) GetSelfQABundleExport(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	if !h.requireTrajectoryExportEnabled(c, subject.UserID) {
		return
	}
	job, found, err := h.service.GetUserBundleExport(c.Request.Context(), subject.UserID, strings.TrimSpace(c.Param("job_id")))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !found {
		response.NotFound(c, "QA bundle export not found")
		return
	}
	response.Success(c, job)
}
