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

	tests := []struct {
		name    string
		body    []byte
		message string
		code    string
	}{
		{
			name: "anthropic sampling params conflict flattened as api_error",
			body: []byte("{\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"`temperature` and `top_p` cannot both be specified for this model. Please use only one.\"}}"),
		},
		{
			name:    "prod compact fingerprint without separator still matches",
			message: "temperatureandtop_p cannot both be specified for this model. Please use only one.",
		},
		{
			name:    "top_k non-default deprecated sampling parameter",
			message: "Setting top_k to a non-default value returns a 400 error for this model.",
		},
		{
			name:    "top_p extra inputs validator",
			message: "top_p: Extra inputs are not permitted",
		},
		{
			name:    "unsupported temperature parameter",
			message: "Unsupported parameter: temperature",
		},
		{
			name:    "invalid value for temperature parameter",
			message: "Invalid value for parameter 'temperature'",
		},
		{
			name:    "request schema field validation",
			message: "thinking.budget_tokens: Input should be greater than or equal to 1024",
		},
		{
			name: "structured client validation code",
			code: "unknown_parameter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			errType, message, code := "api_error", tt.message, tt.code
			if len(tt.body) > 0 {
				parsed := parseOpsErrorResponse(tt.body)
				errType = normalizeOpsErrorType(parsed.ErrorType, parsed.Code)
				message = parsed.Message
				code = parsed.Code
			}

			phase, _, errorOwner, errorSource := classifyOpsErrorLog(c, errType, message, code, http.StatusBadRequest)

			require.Equal(t, "api_error", errType)
			require.Equal(t, "request", phase)
			require.Equal(t, "client", errorOwner)
			require.Equal(t, "client_request", errorSource)
		})
	}
}

func TestClassifyOpsFinalClientValidationAPIErrorDoesNotOvermatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name    string
		message string
		status  int
	}{
		{
			name:    "same validation phrase with non-4xx final status stays platform",
			message: "`temperature` and `top_p` cannot both be specified for this model. Please use only one.",
			status:  http.StatusBadGateway,
		},
		{
			name:    "generic final 400 api_error stays platform",
			message: "Internal server error",
			status:  http.StatusBadRequest,
		},
		{
			name:    "account-level 400 credit balance stays platform without upstream context",
			message: "Your credit balance is too low to access the API.",
			status:  http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			phase, _, errorOwner, errorSource := classifyOpsErrorLog(c, "api_error", tt.message, "", tt.status)

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

	phase, _, errorOwner, errorSource := classifyOpsErrorLog(c, "api_error",
		"`temperature` and `top_p` cannot both be specified for this model. Please use only one.",
		"", http.StatusBadRequest)

	require.Equal(t, "routing", phase)
	require.Equal(t, "platform", errorOwner)
	require.Equal(t, "gateway", errorSource)
}
