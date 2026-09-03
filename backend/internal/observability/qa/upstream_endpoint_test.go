package qa

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCaptureUpstreamEndpointPrefersActualProtocolRoute(t *testing.T) {
	const actualEndpoint = "/api/v1/chat/completions"
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, qaEndpointChatCompletions, nil)
	service.SetActualOpenAIUpstreamEndpoint(c, actualEndpoint)

	require.Equal(t, actualEndpoint, captureUpstreamEndpoint(
		qaEndpointChatCompletions,
		domain.PlatformNewAPI,
		c,
	))
}

func TestCaptureUpstreamEndpointFallsBackWithoutActualRoute(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, qaEndpointChatCompletions, nil)

	require.Equal(t, qaEndpointResponses, captureUpstreamEndpoint(
		qaEndpointChatCompletions,
		domain.PlatformNewAPI,
		c,
	))
}
