package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/observability/qa"
	"github.com/gin-gonic/gin"
)

type QAHandler struct {
	service *qa.Service
}

func NewQAHandler(service *qa.Service) *QAHandler {
	return &QAHandler{service: service}
}

type TrajectoryExportRequest struct {
	APIKeyID *int64 `json:"api_key_id"`
	Format   string `json:"format"`
}

func absoluteRequestURL(c *gin.Context, path string) string {
	scheme := "http"
	if c.Request != nil && c.Request.TLS != nil {
		scheme = "https"
	}
	if xfProto := firstForwardedHeaderValue(c.GetHeader("X-Forwarded-Proto")); xfProto != "" {
		scheme = xfProto
	}

	host := ""
	if c.Request != nil {
		host = strings.TrimSpace(c.Request.Host)
	}
	if xfHost := firstForwardedHeaderValue(c.GetHeader("X-Forwarded-Host")); xfHost != "" {
		host = xfHost
	}
	if host == "" {
		return path
	}
	return scheme + "://" + host + path
}

func firstForwardedHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	return strings.TrimSpace(strings.Split(value, ",")[0])
}
