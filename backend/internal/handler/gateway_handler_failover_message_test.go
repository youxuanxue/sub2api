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
	message := recordGatewayFailoverMessage(t, func(c *gin.Context) {
		(&GatewayHandler{}).handleCCFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
		}, false)
	})

	require.Contains(t, message, "Upstream request could not be completed")
	require.NotContains(t, strings.ToLower(message), "all available accounts exhausted")
}

func TestHandleResponsesFailoverExhaustedUsesTruthfulClientMessage(t *testing.T) {
	message := recordGatewayFailoverMessage(t, func(c *gin.Context) {
		(&GatewayHandler{}).handleResponsesFailoverExhausted(c, &service.UpstreamFailoverError{
			StatusCode: http.StatusBadGateway,
		}, false)
	})

	require.Contains(t, message, "Upstream request could not be completed")
	require.NotContains(t, strings.ToLower(message), "all available accounts exhausted")
}

func recordGatewayFailoverMessage(t *testing.T, write func(*gin.Context)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	write(c)

	require.Equal(t, http.StatusBadGateway, recorder.Code)
	var response struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	return response.Error.Message
}
