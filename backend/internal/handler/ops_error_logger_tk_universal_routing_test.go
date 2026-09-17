package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type unavailableUniversalSpan struct{}

func (unavailableUniversalSpan) GetAvailableGroups(context.Context, int64) ([]service.Group, error) {
	return nil, errors.New("protocol capability unavailable")
}

func TestOpsUniversalRoutingFailureIsPlatformOwned(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/chat/completions", "/v1beta/models/gemini-3-flash-preview:generateContent"} {
		t.Run(path, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"opus","messages":[{"role":"user","content":"hello"}]}`))
			key := &service.APIKey{ID: 1, UserID: 1, RoutingMode: service.RoutingModeUniversal}
			resolver := service.NewUniversalRoutingResolver(unavailableUniversalSpan{})
			require.True(t, middleware.MaybeResolveUniversal(c, key, resolver))
			require.Equal(t, http.StatusInternalServerError, recorder.Code)
			parsed := parseOpsErrorResponse(recorder.Body.Bytes())
			phase, limited, owner, source := classifyOpsErrorLog(c, parsed.ErrorType, parsed.Message, parsed.Code, recorder.Code)
			require.Equal(t, "routing", phase)
			require.Equal(t, "platform", owner)
			require.Equal(t, "gateway", source)
			require.False(t, limited)
			require.NotEqual(t, "permission_error", parsed.ErrorType)
			require.False(t, hasOpsUpstreamErrorContext(c))
		})
	}
}

func TestOpsUniversalRoutingCapacityRecordsRequestedModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, tc := range []struct {
		name         string
		path         string
		route        string
		body         string
		params       gin.Params
		wantModel    string
		wantPlatform string
	}{
		{
			name:         "openai_chat_completions",
			path:         "/v1/chat/completions",
			route:        "/v1/chat/completions",
			body:         `{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:    "gemini-3.8-flash",
			wantPlatform: service.PlatformAntigravity, // capacity hint; OpenAI shape does not pin platform
		},
		{
			name:         "anthropic_messages",
			path:         "/v1/messages",
			route:        "/v1/messages",
			body:         `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`,
			wantModel:    "claude-sonnet-4-6",
			wantPlatform: service.PlatformAnthropic, // shape hint wins before capacity platform
		},
		{
			name:         "gemini_generate_content",
			path:         "/v1beta/models/gemini-3.8-flash:generateContent",
			route:        "/v1beta/models/:modelAction",
			body:         `{"contents":[{"parts":[{"text":"hi"}]}]}`,
			params:       gin.Params{{Key: "modelAction", Value: "gemini-3.8-flash:generateContent"}},
			wantModel:    "gemini-3.8-flash",
			wantPlatform: service.PlatformGemini,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 4)
			router := gin.New()
			router.Use(OpsErrorLoggerMiddleware(ops))
			router.POST(tc.route, func(c *gin.Context) {
				if len(tc.params) > 0 {
					c.Params = tc.params
				}
				key := &service.APIKey{ID: 420, UserID: 1, RoutingMode: service.RoutingModeUniversal}
				resolver := service.NewUniversalRoutingResolver(capacityUniversalSpan{})
				resolver.SetCandidateEvaluator(service.NewProtocolRouter(), func(context.Context, service.Group, string, service.UniversalShape) (service.GroupCandidateEligibility, error) {
					return service.GroupCandidateEligibility{Supported: true}, nil
				})
				require.True(t, middleware.MaybeResolveUniversal(c, key, resolver))
			})

			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			require.Equal(t, http.StatusTooManyRequests, recorder.Code)

			require.Equal(t, int64(1), OpsErrorLogQueueLength())
			job := <-opsErrorLogQueue
			require.NotNil(t, job.entry)
			require.Equal(t, tc.wantModel, job.entry.Model, "empty-pool 429 must record peeked model for pool supply attribution")
			require.Equal(t, tc.wantModel, job.entry.RequestedModel)
			require.Equal(t, "routing", job.entry.ErrorPhase)
			require.Equal(t, "platform", job.entry.ErrorOwner)
			require.True(t, job.entry.IsBusinessLimited)
			require.Equal(t, tc.wantPlatform, job.entry.Platform)
			require.Nil(t, job.entry.AccountID)
		})
	}
}

func TestOpsUniversalRoutingCapacityWithoutModelStaysEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupOpsErrorLogTestQueue(t, 2)

	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.Use(OpsErrorLoggerMiddleware(ops))
	router.POST("/v1/chat/completions", func(c *gin.Context) {
		key := &service.APIKey{ID: 420, UserID: 1, RoutingMode: service.RoutingModeUniversal}
		resolver := service.NewUniversalRoutingResolver(capacityUniversalSpan{})
		resolver.SetCandidateEvaluator(service.NewProtocolRouter(), func(context.Context, service.Group, string, service.UniversalShape) (service.GroupCandidateEligibility, error) {
			return service.GroupCandidateEligibility{Supported: true}, nil
		})
		_ = middleware.MaybeResolveUniversal(c, key, resolver)
		if !c.IsAborted() {
			c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "model required", "type": "invalid_request_error"}})
		}
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if OpsErrorLogQueueLength() == 0 {
		return
	}
	job := <-opsErrorLogQueue
	require.NotNil(t, job.entry)
	require.Empty(t, job.entry.Model, "must not invent a model when the body has none")
	require.Empty(t, job.entry.RequestedModel)
}

type capacityUniversalSpan struct{}

func (capacityUniversalSpan) GetAvailableGroups(context.Context, int64) ([]service.Group, error) {
	mk := func(id int64, platform string) service.Group {
		return service.Group{ID: id, Name: platform, Platform: platform, Status: service.StatusActive}
	}
	return []service.Group{
		mk(21, service.PlatformAntigravity),
		mk(22, service.PlatformAnthropic),
		mk(23, service.PlatformGemini),
	}, nil
}
