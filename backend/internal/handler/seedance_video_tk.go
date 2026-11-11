package handler

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// The facade changes protocol only. VideoSubmit/Fetch retain account selection,
// holds, settlement, route pinning, ownership and terminal-failure refunds.
func (h *OpenAIGatewayHandler) seedanceVideoTask(c *gin.Context) {
	setActualUpstreamEndpoint(c, EndpointVideoGenerations)
	taskID := c.Param("task_id")
	var record *service.VideoTaskRecord
	if c.Request.Method != http.MethodPost {
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		if !ok {
			h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "User context not found")
			return
		}
		if h.videoTaskCache != nil {
			record, _ = h.videoTaskCache.Lookup(c.Request.Context(), taskID)
		}
		if record == nil || record.UserID != subject.UserID {
			h.errorResponse(c, http.StatusNotFound, "not_found_error", "video task not found or expired")
			return
		}
		if c.Request.Method == http.MethodDelete {
			c.Header("Allow", "GET")
			h.errorResponse(c, http.StatusMethodNotAllowed, "invalid_request_error", "This video provider does not support task cancellation")
			return
		}
	} else {
		body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
		if err != nil {
			writeReadRequestBodyError(c, err, h.errorResponse)
			return
		}
		body, err = service.PrepareSeedanceVideoRequest(body)
		if err != nil {
			h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))
		c.Request.Header.Del("Content-Encoding")
		c.Request.Header.Del("Content-Length")
	}

	original := c.Writer
	buffered := &bufferedJSONWriter{ResponseWriter: original, headers: original.Header().Clone(), status: http.StatusOK}
	c.Writer = buffered
	defer func() { c.Writer = original }()
	if c.Request.Method == http.MethodPost {
		h.VideoSubmit(c)
	} else {
		h.VideoFetch(c)
	}
	c.Writer = original
	for name, values := range buffered.headers {
		if strings.EqualFold(name, "Content-Length") {
			continue
		}
		c.Writer.Header()[name] = values
	}
	c.Header("Content-Length", "")
	if buffered.status >= http.StatusBadRequest {
		c.Data(buffered.status, "application/json", buffered.body.Bytes())
		return
	}
	if buffered.overflow || !gjson.ValidBytes(buffered.body.Bytes()) {
		h.errorResponse(c, http.StatusBadGateway, "api_error", "Invalid video task response")
		return
	}
	raw := gjson.ParseBytes(buffered.body.Bytes())
	if c.Request.Method == http.MethodPost {
		id := raw.Get("id").String()
		if id == "" {
			id = raw.Get("task_id").String()
		}
		if !strings.HasPrefix(id, "vt_") {
			h.errorResponse(c, http.StatusBadGateway, "api_error", "Video response missing public task ID")
			return
		}
		c.JSON(buffered.status, gin.H{"id": id})
		return
	}
	result := gin.H{"id": taskID, "model": record.OriginModel, "status": seedanceVideoStatus(raw)}
	for _, field := range []string{"created_at", "updated_at", "error"} {
		if value := raw.Get(field); value.Exists() {
			result[field] = value.Value()
		}
	}
	for _, path := range []string{"content.video_url", "result.url", "result_url", "video_url", "url", "data.video_url"} {
		if url := raw.Get(path).String(); url != "" {
			result["content"] = gin.H{"video_url": url}
			break
		}
	}
	c.JSON(buffered.status, result)
}

func seedanceVideoStatus(raw gjson.Result) string {
	status := strings.ToLower(raw.Get("status").String())
	if status == "" {
		status = strings.ToLower(raw.Get("task.status").String())
	}
	switch status {
	case "completed", "succeeded", "success":
		return "succeeded"
	case "failed", "failure", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "expired":
		return "expired"
	case "running", "processing", "in_progress":
		return "running"
	default:
		return "queued"
	}
}
