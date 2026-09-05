package handler

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type openRouterProviderResponseCapture struct {
	gin.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (w *openRouterProviderResponseCapture) WriteHeader(code int) {
	w.status = code
	w.wroteHeader = true
}

func (w *openRouterProviderResponseCapture) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.body.Write(b)
}

func (w *openRouterProviderResponseCapture) capturedStatus() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func openRouterProviderAPIBase(c *gin.Context) string {
	scheme := "https"
	if c.Request.TLS == nil {
		if xf := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); xf != "" {
			scheme = strings.ToLower(xf)
		}
	}
	host := strings.TrimSpace(c.Request.Host)
	if host == "" {
		host = "api.tokenkey.dev"
	}
	return scheme + "://" + host
}

func (h *GatewayHandler) authorizeOpenRouterProviderInference(c *gin.Context) (service.OpenRouterProviderConfig, bool) {
	if h == nil || h.settingService == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider inference unavailable", "code": 503}})
		return service.OpenRouterProviderConfig{}, true
	}
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "missing api key", "code": 401}})
		return service.OpenRouterProviderConfig{}, true
	}
	cfg, err := h.settingService.GetOpenRouterProviderConfig(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "load openrouter provider config", "code": 500}})
		return service.OpenRouterProviderConfig{}, true
	}
	if !cfg.Enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider disabled", "code": 503}})
		return cfg, true
	}
	userID := apiKey.UserID
	if apiKey.User != nil && apiKey.User.ID > 0 {
		userID = apiKey.User.ID
	}
	if !cfg.AllowsInferenceAPIKey(apiKey.ID, userID, apiKey.Name) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"message": "api key not authorized for openrouter provider inference", "code": 403}})
		return cfg, true
	}
	return cfg, false
}

// OpenRouterProviderImages serves POST /openrouter/v1/images for OpenRouter provider inference.
func (h *Handlers) OpenRouterProviderImages(c *gin.Context) {
	if h == nil || h.Gateway == nil || h.OpenAIGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider images unavailable", "code": 503}})
		return
	}
	if _, handled := h.Gateway.authorizeOpenRouterProviderInference(c); handled {
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "read request body", "code": 400}})
		return
	}

	model := strings.TrimSpace(gjsonGetString(body, "model"))
	route := service.OpenRouterProviderImageRoute(model)

	var (
		forwardBody       []byte
		translateResponse func([]byte) ([]byte, error)
		dispatch          func(*gin.Context)
	)
	switch route {
	case service.OpenRouterImageRouteAntigravityChat:
		forwardBody, err = service.TranslateOpenRouterImageToChatCompletions(body)
		translateResponse = service.TranslateChatCompletionsImageResponseToOpenRouter
		dispatch = h.Gateway.ChatCompletions
	case service.OpenRouterImageRouteGrok:
		forwardBody, err = service.TranslateOpenRouterImageRequestToOpenAI(body)
		translateResponse = service.TranslateOpenAIImageResponseToOpenRouter
		dispatch = h.OpenAIGateway.GrokImages
	default:
		forwardBody, err = service.TranslateOpenRouterImageRequestToOpenAI(body)
		translateResponse = service.TranslateOpenAIImageResponseToOpenRouter
		dispatch = h.OpenAIGateway.ImageGenerations
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "code": 400}})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(forwardBody))
	c.Request.ContentLength = int64(len(forwardBody))

	capture := &openRouterProviderResponseCapture{ResponseWriter: c.Writer}
	c.Writer = capture
	dispatch(c)
	c.Writer = capture.ResponseWriter

	status := capture.capturedStatus()
	if status >= 400 || gjsonHasError(capture.body.Bytes()) {
		writeCapturedResponse(c, capture)
		return
	}
	orBody, err := translateResponse(capture.body.Bytes())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "translate image response", "code": 500}})
		return
	}
	c.Data(status, "application/json", orBody)
}

// OpenRouterProviderVideoSubmit serves POST /openrouter/v1/videos for OpenRouter provider inference.
func (h *Handlers) OpenRouterProviderVideoSubmit(c *gin.Context) {
	if h == nil || h.Gateway == nil || h.OpenAIGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider videos unavailable", "code": 503}})
		return
	}
	if _, handled := h.Gateway.authorizeOpenRouterProviderInference(c); handled {
		return
	}

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "read request body", "code": 400}})
		return
	}
	translated, err := service.TranslateOpenRouterVideoRequestToOpenAI(body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": err.Error(), "code": 400}})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(translated))
	c.Request.ContentLength = int64(len(translated))

	capture := &openRouterProviderResponseCapture{ResponseWriter: c.Writer}
	c.Writer = capture
	h.OpenAIGateway.VideoSubmit(c)
	c.Writer = capture.ResponseWriter

	status := capture.capturedStatus()
	if status >= 400 || gjsonHasError(capture.body.Bytes()) {
		writeCapturedResponse(c, capture)
		return
	}
	taskID := strings.TrimSpace(gjsonGetString(capture.body.Bytes(), "id"))
	if taskID == "" {
		taskID = strings.TrimSpace(gjsonGetString(capture.body.Bytes(), "task_id"))
	}
	orBody, err := service.BuildOpenRouterVideoSubmitResponse(taskID, openRouterProviderAPIBase(c))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": "video submit missing task id", "code": 502}})
		return
	}
	c.Data(http.StatusAccepted, "application/json", orBody)
}

// OpenRouterProviderVideoFetch serves GET /openrouter/v1/videos/:id for OpenRouter provider polling.
func (h *Handlers) OpenRouterProviderVideoFetch(c *gin.Context) {
	if h == nil || h.Gateway == nil || h.OpenAIGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"message": "openrouter provider videos unavailable", "code": 503}})
		return
	}
	if _, handled := h.Gateway.authorizeOpenRouterProviderInference(c); handled {
		return
	}

	taskID := strings.TrimSpace(c.Param("id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "id is required", "code": 400}})
		return
	}
	c.Params = gin.Params{{Key: "task_id", Value: taskID}}

	capture := &openRouterProviderResponseCapture{ResponseWriter: c.Writer}
	c.Writer = capture
	h.OpenAIGateway.VideoFetch(c)
	c.Writer = capture.ResponseWriter

	status := capture.capturedStatus()
	if status >= 400 || gjsonHasError(capture.body.Bytes()) {
		writeCapturedResponse(c, capture)
		return
	}
	orBody, err := service.TranslateOpenAIVideoFetchToOpenRouter(taskID, capture.body.Bytes(), openRouterProviderAPIBase(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "translate video response", "code": 500}})
		return
	}
	c.Data(status, "application/json", orBody)
}

func writeCapturedResponse(c *gin.Context, capture *openRouterProviderResponseCapture) {
	if capture == nil {
		return
	}
	status := capture.capturedStatus()
	if len(capture.body.Bytes()) == 0 {
		c.Status(status)
		return
	}
	c.Data(status, "application/json", capture.body.Bytes())
}

func gjsonHasError(body []byte) bool {
	return len(body) > 0 && (gjsonGetString(body, "error.message") != "" || gjsonGetString(body, "error.type") != "")
}

func gjsonGetString(body []byte, path string) string {
	if len(body) == 0 {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(body, path).String())
}
