package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpsFinalClientValidationAPIErrorOwnedByClient(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("anthropic sampling params conflict flattened as api_error", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		body := []byte("{\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"`temperature` and `top_p` cannot both be specified for this model. Please use only one.\"}}")

		parsed := parseOpsErrorResponse(body)
		errType := normalizeOpsErrorType(parsed.ErrorType, parsed.Code)
		phase, errorOwner, errorSource := classifyOpsErrorLog(c, errType, parsed.Message, parsed.Code, http.StatusBadRequest)

		require.Equal(t, "api_error", errType)
		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
		require.Equal(t, "client_request", errorSource)
	})

	t.Run("prod compact fingerprint without separator still matches", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		phase, errorOwner, errorSource := classifyOpsErrorLog(c, "api_error",
			"temperatureandtop_p cannot both be specified for this model. Please use only one.",
			"", http.StatusBadRequest)

		require.Equal(t, "request", phase)
		require.Equal(t, "client", errorOwner)
		require.Equal(t, "client_request", errorSource)
	})
}

func TestClassifyOpsFinalClientValidationAPIErrorDoesNotOvermatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		message string
		status  int
	}{
		{
			name:    "same validation phrase with non-400 final status stays platform",
			message: "`temperature` and `top_p` cannot both be specified for this model. Please use only one.",
			status:  http.StatusBadGateway,
		},
		{
			name:    "generic final 400 api_error stays platform",
			message: "Internal server error",
			status:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			phase, errorOwner, errorSource := classifyOpsErrorLog(c, "api_error", tt.message, "", tt.status)

			require.Equal(t, "internal", phase)
			require.Equal(t, "platform", errorOwner)
			require.Equal(t, "gateway", errorSource)
		})
	}
}

func TestClassifyOpsRoutingCapacityWinsOverFinalClientValidationAPIError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	markOpsRoutingCapacityLimited(c)

	phase, errorOwner, errorSource := classifyOpsErrorLog(c, "api_error",
		"`temperature` and `top_p` cannot both be specified for this model. Please use only one.",
		"", http.StatusBadRequest)

	require.Equal(t, "routing", phase)
	require.Equal(t, "platform", errorOwner)
	require.Equal(t, "gateway", errorSource)
}
