//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroContentFilterOpsCase struct {
	name       string
	body       []byte
	normalized string
}

func kiroContentFilterOpsCases() []kiroContentFilterOpsCase {
	return []kiroContentFilterOpsCase{
		{
			name:       "messages invalid_request_error",
			body:       []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Request was blocked by upstream content filtering"}}`),
			normalized: "invalid_request_error",
		},
		{
			name:       "chat completions content_filter_error",
			body:       []byte(`{"error":{"type":"content_filter_error","message":"Request was blocked by upstream content filtering"}}`),
			normalized: "content_filter_error",
		},
		{
			name:       "responses content_filter code",
			body:       []byte(`{"error":{"code":"content_filter","message":"Request was blocked by upstream content filtering"}}`),
			normalized: "content_filter_error",
		},
	}
}

func TestClassifyOpsKiroContentFilterOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tt := range kiroContentFilterOpsCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			service.MarkOpsClientContentFiltered(c)
			parsed := parseOpsErrorResponse(tt.body)
			errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)

			phase, owner, source := classifyOpsErrorLog(c, errType, parsed.Message, parsed.Code, http.StatusBadRequest)

			require.False(t, hasOpsUpstreamErrorContext(c))
			require.Equal(t, tt.normalized, errType)
			require.Equal(t, "request", phase)
			require.Equal(t, "client", owner)
			require.Equal(t, "client_request", source)
			require.Equal(t, "P3", classifyOpsSeverity(errType, http.StatusBadRequest))
		})
	}
}

func TestClassifyOpsKiroContentFilterAfterPriorFailoverStillOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range kiroContentFilterOpsCases() {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(service.OpsUpstreamStatusCodeKey, http.StatusBadGateway)
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{
				Platform:           service.PlatformKiro,
				UpstreamStatusCode: http.StatusBadGateway,
				Kind:               "failover",
				Message:            "edge unavailable",
			}})
			service.MarkOpsClientContentFiltered(c)
			parsed := parseOpsErrorResponse(tt.body)
			errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)

			phase, owner, source := classifyOpsErrorLog(c, errType, parsed.Message, parsed.Code, http.StatusBadRequest)

			require.True(t, hasOpsUpstreamErrorContext(c), "prior failover evidence must remain available")
			require.Equal(t, tt.normalized, errType)
			require.Equal(t, "request", phase)
			require.Equal(t, "client", owner)
			require.Equal(t, "client_request", source)
		})
	}
}

func TestNormalizeOpsContentFilterCodeDoesNotOverrideExplicitErrorType(t *testing.T) {
	require.Equal(t, "authentication_error", normalizeOpsErrorType("authentication_error", "content_filter"))
}
