//go:build unit

package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDispatchResponsesAliPreservesTokenBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, budget := range []int{8, 16, 32} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			var upstreamBudget int
			var upstreamPath string
			supplier := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				upstreamPath = request.URL.Path
				var payload struct {
					MaxOutputTokens int `json:"max_output_tokens"`
				}
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
					return
				}
				upstreamBudget = payload.MaxOutputTokens
				writer.Header().Set("Content-Type", "application/json")
				if upstreamBudget < 16 {
					writer.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(writer).Encode(gin.H{"error": gin.H{
						"message": "Invalid 'max_output_tokens': must be greater than or equal to 16, got 8.",
						"param":   "max_output_tokens", "type": "invalid_request_error", "code": "invalid_value",
					}})
					return
				}
				_ = json.NewEncoder(writer).Encode(gin.H{
					"id": "resp_budget", "object": "response", "status": "completed", "model": "deepseek-v4.1-flash",
					"output": []any{}, "usage": gin.H{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
				})
			}))
			defer supplier.Close()
			body, err := json.Marshal(gin.H{"model": "deepseek-v4.1-flash", "input": "hi", "max_output_tokens": budget})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			requestCtx, _ := gin.CreateTestContext(recorder)
			requestCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
			requestCtx.Request.Header.Set("Content-Type", "application/json")
			outcome, apiErr := DispatchResponses(context.Background(), requestCtx, ChannelContextInput{
				ChannelType: newapiconstant.ChannelTypeAli, ChannelID: 1, BaseURL: supplier.URL, APIKey: "test-key",
			}, body)
			require.Equal(t, "/api/v2/apps/protocols/compatible-mode/v1/responses", upstreamPath)
			require.Equal(t, budget, upstreamBudget)
			if budget == 8 {
				require.NotNil(t, apiErr)
				require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
				require.Equal(t, "max_output_tokens", apiErr.ToOpenAIError().Param)
				require.Nil(t, outcome)
				return
			}
			require.Nil(t, apiErr)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, 1, outcome.Usage.CompletionTokens)
		})
	}
}
