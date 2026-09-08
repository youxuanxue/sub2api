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
