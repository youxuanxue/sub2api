//go:build unit

package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpsKiroContentFilterOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name string
		body []byte
	}{
		{
			name: "messages invalid_request_error",
			body: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Request was blocked by upstream content filtering"}}`),
		},
		{
			name: "chat completions content_filter_error",
			body: []byte(`{"error":{"type":"content_filter_error","message":"Request was blocked by upstream content filtering"}}`),
		},
		{
			name: "responses content_filter code",
			body: []byte(`{"error":{"code":"content_filter","message":"Request was blocked by upstream content filtering"}}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			parsed := parseOpsErrorResponse(tt.body)
			errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)

			phase, owner, source := classifyOpsErrorLog(c, errType, parsed.Message, parsed.Code, http.StatusBadRequest)

			require.False(t, hasOpsUpstreamErrorContext(c))
			require.Equal(t, "request", phase)
			require.Equal(t, "client", owner)
			require.Equal(t, "client_request", source)
		})
	}
}
