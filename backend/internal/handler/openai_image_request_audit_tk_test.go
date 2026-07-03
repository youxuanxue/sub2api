package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestLogOpenAIImageGenerationRequestAuditCapturesPromptTrace(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	groupID := int64(7)
	prompt := "东京雨夜的霓虹小巷，电影感，浅景深 api_key=secret-token"
	body := []byte(fmt.Sprintf(`{"model":"imagen-4.0-generate-001","prompt":%q,"size":"1:1","n":2,"quality":"high"}`, prompt))
	forwardBody := []byte(fmt.Sprintf(`{"model":"imagen-upstream","prompt":%q,"size":"1:1","n":2}`, prompt))
	c := newImageAuditTestContext("/v1/images/generations")
	c.Request.Header.Set(tkStudioSourceHeader, "studio.bakeoff.image")
	c.Request.Header.Set(tkStudioRunIDHeader, "run-123")
	c.Request.Header.Set(tkStudioPanelHeader, "imagen-4.0-generate-001")

	logOpenAIImageGenerationRequestAudit(
		c,
		&service.APIKey{ID: 103, GroupID: &groupID},
		1,
		&service.Account{ID: 57, Platform: service.PlatformOpenAI},
		body,
		forwardBody,
	)

	event := requireImageAuditEvent(t, logSink)
	require.Equal(t, tkImageRequestAuditComponent, event.Component)
	require.Equal(t, tkImageRequestAuditMessage, event.Message)
	require.Equal(t, "req-img-1", event.Fields["request_id"])
	require.Equal(t, "creq-img-1", event.Fields["client_request_id"])
	require.Equal(t, int64(1), event.Fields["user_id"])
	require.Equal(t, int64(103), event.Fields["api_key_id"])
	require.Equal(t, int64(57), event.Fields["account_id"])
	require.Equal(t, service.PlatformOpenAI, event.Fields["platform"])
	require.Equal(t, "imagen-4.0-generate-001", event.Fields["requested_model"])
	require.Equal(t, "imagen-upstream", event.Fields["forward_model"])
	require.Equal(t, "1:1", event.Fields["size"])
	require.Equal(t, int64(2), event.Fields["n"])
	require.Equal(t, "studio.bakeoff.image", event.Fields["studio_source"])
	require.Equal(t, "run-123", event.Fields["studio_run_id"])
	require.Equal(t, "imagen-4.0-generate-001", event.Fields["studio_panel_id"])
	require.Equal(t, tkSHA256Hex([]byte(prompt)), event.Fields["prompt_sha256"])
	require.Contains(t, fmt.Sprint(event.Fields["prompt_preview"]), "东京雨夜的霓虹小巷")
	require.Contains(t, fmt.Sprint(event.Fields["prompt_preview"]), "api_key=***")
	require.NotContains(t, fmt.Sprint(event.Fields["prompt_preview"]), "secret-token")
}

func TestLogOpenAIStudioChatImageRequestAuditRequiresStudioImageHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	body := []byte(`{"model":"gemini-3.1-flash-image","messages":[{"role":"user","content":[{"type":"text","text":"make it neon"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}],"stream":false}`)
	c := newImageAuditTestContext("/v1/chat/completions")
	apiKey := &service.APIKey{ID: 104}
	account := &service.Account{ID: 58, Platform: service.PlatformOpenAI}

	logOpenAIStudioChatImageRequestAudit(c, apiKey, 1, account, body, body)
	require.Nil(t, findImageAuditEvent(logSink), "plain chat without Studio image trace should not be audited")

	c.Request.Header.Set(tkStudioSourceHeader, "studio.image")
	c.Request.Header.Set(tkStudioRunIDHeader, "run-chat-1")
	logOpenAIStudioChatImageRequestAudit(c, apiKey, 1, account, body, body)

	event := requireImageAuditEvent(t, logSink)
	require.Equal(t, "chat.completions", event.Fields["surface"])
	require.Equal(t, "messages.user.content[].text", event.Fields["prompt_source"])
	require.Equal(t, "gemini-3.1-flash-image", event.Fields["requested_model"])
	require.Contains(t, fmt.Sprint(event.Fields["prompt_preview"]), "make it neon")
	require.NotContains(t, fmt.Sprint(event.Fields["prompt_preview"]), "data:image")
	require.Equal(t, "studio.image", event.Fields["studio_source"])
	require.Equal(t, "run-chat-1", event.Fields["studio_run_id"])
}

func newImageAuditTestContext(path string) *gin.Context {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, path, nil)
	ctx := context.WithValue(req.Context(), ctxkey.RequestID, "req-img-1")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "creq-img-1")
	c.Request = req.WithContext(ctx)
	return c
}

func requireImageAuditEvent(t *testing.T, sink *handlerInMemoryLogSink) *logger.LogEvent {
	t.Helper()
	event := findImageAuditEvent(sink)
	require.NotNil(t, event)
	return event
}

func findImageAuditEvent(sink *handlerInMemoryLogSink) *logger.LogEvent {
	if sink == nil {
		return nil
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	for _, event := range sink.events {
		if event == nil {
			continue
		}
		component := strings.TrimSpace(event.Component)
		if component == "" && event.Fields != nil {
			component = strings.TrimSpace(fmt.Sprint(event.Fields["component"]))
		}
		if component == tkImageRequestAuditComponent {
			return event
		}
	}
	return nil
}
