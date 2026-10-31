package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type FeedbackHandler struct{ service *service.FeedbackService }

func NewFeedbackHandler(s *service.FeedbackService) *FeedbackHandler {
	return &FeedbackHandler{service: s}
}

type feedbackRequest struct {
	Category  string `json:"category"`
	Subject   string `json:"subject"`
	Content   string `json:"content"`
	RequestID string `json:"request_id"`
}

func (h *FeedbackHandler) Create(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	var req feedbackRequest
	if c.ShouldBindJSON(&req) != nil {
		response.InvalidRequest(c)
		return
	}
	t, err := h.service.Create(c.Request.Context(), subject.UserID, req.Category, req.Subject, req.Content, req.RequestID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, t)
}
func (h *FeedbackHandler) List(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	t, err := h.service.List(c.Request.Context(), subject.UserID, false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, t)
}
func (h *FeedbackHandler) Get(c *gin.Context) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not authenticated")
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid ticket ID")
		return
	}
	t, err := h.service.Get(c.Request.Context(), id, subject.UserID, false)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, t)
}
func (h *FeedbackHandler) AdminList(c *gin.Context) {
	t, err := h.service.List(c.Request.Context(), 0, true)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, t)
}
func (h *FeedbackHandler) AdminUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "Invalid ticket ID")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.InvalidRequest(c)
		return
	}
	t, err := h.service.UpdateStatus(c.Request.Context(), id, req.Status)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, t)
}
