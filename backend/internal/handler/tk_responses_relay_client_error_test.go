//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiservice "github.com/QuantumNous/new-api/service"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const deepSeekBudgetError = "Invalid 'max_output_tokens': must be greater than or equal to 16, got 8."

func deepSeekResponsesRelayError() error {
	body, _ := json.Marshal(gin.H{"error": gin.H{
		"message": deepSeekBudgetError, "type": "invalid_request_error",
		"code": "invalid_value", "param": "max_output_tokens",
	}})
	apiErr := newapiservice.RelayErrorHandler(context.Background(), &http.Response{
		StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(string(body))),
	}, false)
	return fmt.Errorf("responses dispatch: %w", &service.NewAPIRelayError{Err: apiErr})
}

func TestResponsesRelayClientError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handlers := []struct {
		name    string
		forward func(*gin.Context, error, bool) bool
	}{
		{"universal", (&GatewayHandler{}).ensureForwardErrorResponseForError},
		{"openai", (&OpenAIGatewayHandler{}).ensureForwardErrorResponseForError},
	}
	for _, handler := range handlers {
		t.Run(handler.name, func(t *testing.T) {
			for _, route := range []string{"/responses", "/v1/responses"} {
				t.Run(route, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					requestCtx, _ := gin.CreateTestContext(recorder)
					requestCtx.Request = httptest.NewRequest(http.MethodPost, route, nil)
					requestCtx.Header("Content-Type", "text/event-stream")
					require.True(t, handler.forward(requestCtx, deepSeekResponsesRelayError(), false))
					require.Equal(t, http.StatusBadRequest, recorder.Code)
					require.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
					var response struct {
						Error newapitypes.OpenAIError `json:"error"`
					}
					require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
					require.Equal(t, deepSeekBudgetError, response.Error.Message)
					require.Equal(t, "invalid_request_error", response.Error.Type)
					require.Equal(t, "invalid_value", response.Error.Code)
					require.Equal(t, "max_output_tokens", response.Error.Param)
				})
			}
			t.Run("heartbeat", func(t *testing.T) {
				recorder := httptest.NewRecorder()
				requestCtx, _ := gin.CreateTestContext(recorder)
				requestCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				requestCtx.Header("Content-Type", "text/event-stream")
				_, writeErr := requestCtx.Writer.WriteString(": ping\n\n")
				require.NoError(t, writeErr)
				requestCtx.Writer.Flush()
				require.True(t, handler.forward(requestCtx, deepSeekResponsesRelayError(), false))
				require.Equal(t, http.StatusOK, recorder.Code)
				body := recorder.Body.String()
				require.Equal(t, 1, strings.Count(body, "event: response.failed\n"))
				data := strings.TrimSuffix(strings.SplitN(body, "data: ", 2)[1], "\n\n")
				var event responsesFailedEvent
				require.NoError(t, json.Unmarshal([]byte(data), &event))
				require.Equal(t, "failed", event.Response.Status)
				require.Equal(t, "invalid_value", event.Response.Error.Code)
				require.Equal(t, deepSeekBudgetError, event.Response.Error.Message)
			})
			t.Run("committed", func(t *testing.T) {
				recorder := httptest.NewRecorder()
				requestCtx, _ := gin.CreateTestContext(recorder)
				requestCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				requestCtx.JSON(http.StatusBadRequest, gin.H{"error": "already written"})
				service.MarkResponseCommitted(requestCtx)
				before := recorder.Body.String()
				require.False(t, handler.forward(requestCtx, deepSeekResponsesRelayError(), false))
				require.Equal(t, before, recorder.Body.String())
			})
			t.Run("canceled", func(t *testing.T) {
				recorder := httptest.NewRecorder()
				requestCtx, _ := gin.CreateTestContext(recorder)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				requestCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				require.False(t, handler.forward(requestCtx, deepSeekResponsesRelayError(), false))
				require.Equal(t, statusClientClosedRequest, requestCtx.Writer.Status())
				require.Empty(t, recorder.Body.String())
			})
			t.Run("account faults and local errors", func(t *testing.T) {
				for _, relayErr := range []error{
					&service.NewAPIRelayError{Err: newapitypes.WithOpenAIError(newapitypes.OpenAIError{Message: "private account failure"}, http.StatusUnauthorized)},
					&service.NewAPIRelayError{Err: newapitypes.WithOpenAIError(newapitypes.OpenAIError{Message: "private account failure"}, http.StatusPaymentRequired)},
					&service.NewAPIRelayError{Err: newapitypes.WithOpenAIError(newapitypes.OpenAIError{Message: "private account failure"}, http.StatusForbidden)},
					&service.NewAPIRelayError{Err: newapitypes.NewErrorWithStatusCode(errors.New("missing credential"), newapitypes.ErrorCodeChannelInvalidKey, http.StatusBadRequest)},
				} {
					recorder := httptest.NewRecorder()
					requestCtx, _ := gin.CreateTestContext(recorder)
					requestCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					require.True(t, handler.forward(requestCtx, relayErr, false))
					require.Equal(t, http.StatusBadGateway, recorder.Code)
					require.Contains(t, recorder.Body.String(), "Upstream request failed")
					require.NotContains(t, recorder.Body.String(), "private account failure")
					require.NotContains(t, recorder.Body.String(), "missing credential")
				}
			})
		})
	}
}
