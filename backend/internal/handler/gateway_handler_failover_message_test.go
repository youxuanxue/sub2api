//go:build unit

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestHandleCCFailoverExhaustedUsesTruthfulClientMessage(t *testing.T) {
	message := recordGatewayFailoverMessage(t, http.StatusBadGateway, func(c *gin.Context) {
		(&GatewayHandler{}).handleCCFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
		}, false)
	})

	require.Contains(t, message, "Upstream request could not be completed")
	require.NotContains(t, strings.ToLower(message), "all available accounts exhausted")
}

func TestHandleResponsesFailoverExhaustedUsesTruthfulClientMessage(t *testing.T) {
	message := recordGatewayFailoverMessage(t, http.StatusBadGateway, func(c *gin.Context) {
		(&GatewayHandler{}).handleResponsesFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
		}, false)
	})

	require.Contains(t, message, "Upstream request could not be completed")
	require.NotContains(t, strings.ToLower(message), "all available accounts exhausted")
}

func TestFailoverExhaustedUsesCapacityClientMetadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*gin.Context, *service.UpstreamFailoverError)
	}{
		{
			name: "chat_completions",
			write: func(c *gin.Context, err *service.UpstreamFailoverError) {
				(&GatewayHandler{}).handleCCFailoverExhausted(c, err, false)
			},
		},
		{
			name: "responses",
			write: func(c *gin.Context, err *service.UpstreamFailoverError) {
				(&GatewayHandler{}).handleResponsesFailoverExhausted(c, err, false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			failoverErr := &service.UpstreamFailoverError{
				StatusCode:       http.StatusServiceUnavailable,
				ClientStatusCode: http.StatusTooManyRequests,
				ClientMessage:    service.AntigravityRelayCapacityClientMessage,
				ResponseHeaders: http.Header{
					"Retry-After": []string{service.NoAvailableAccountsRetryAfterSeconds},
				},
			}

			tc.write(c, failoverErr)

			require.Equal(t, http.StatusTooManyRequests, recorder.Code)
			require.Equal(t, service.NoAvailableAccountsRetryAfterSeconds, recorder.Header().Get("Retry-After"))
			require.Contains(t, recorder.Body.String(), service.AntigravityRelayCapacityClientMessage)
		})
	}
}

func TestFailoverExhaustedPreservesUnclassifiedProvider503(t *testing.T) {
	for _, tc := range []struct {
		name  string
		write func(*gin.Context, *service.UpstreamFailoverError)
	}{
		{
			name: "chat_completions",
			write: func(c *gin.Context, err *service.UpstreamFailoverError) {
				(&GatewayHandler{}).handleCCFailoverExhausted(c, err, false)
			},
		},
		{
			name: "responses",
			write: func(c *gin.Context, err *service.UpstreamFailoverError) {
				(&GatewayHandler{}).handleResponsesFailoverExhausted(c, err, false)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)

			tc.write(c, &service.UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable})

			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
			require.Empty(t, recorder.Header().Get("Retry-After"))
		})
	}
}

func recordGatewayFailoverMessage(t *testing.T, expectedStatus int, write func(*gin.Context)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	write(c)

	require.Equal(t, expectedStatus, recorder.Code)
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	return response.Error.Message
}
