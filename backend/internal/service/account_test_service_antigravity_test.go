//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestMapAntigravityModel_LiveAccountAllowsOnlyLiveClaudeSubset(t *testing.T) {
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	account := &Account{
		Platform: PlatformAntigravity,
		Credentials: map[string]any{
			"model_mapping": modelMappingToAny(mapping),
		},
	}

	require.NotEmpty(t, MapAntigravityModel(account, AntigravityDefaultTestModelID))
	require.Equal(t, "claude-sonnet-4-6", MapAntigravityModel(account, "claude-sonnet-4-6"))
	require.Equal(t, "claude-opus-4-6-thinking", MapAntigravityModel(account, "claude-opus-4-6"))
	require.Empty(t, MapAntigravityModel(account, "claude-sonnet-4-5"))
	require.Empty(t, MapAntigravityModel(account, "claude-opus-4-8"))
	require.Empty(t, MapAntigravityModel(account, "gpt-oss-120b-medium"))
}

func TestAntigravityDefaultTestModelID_IsGeminiWire(t *testing.T) {
	require.True(t, len(AntigravityDefaultTestModelID) > len("gemini-"))
	require.Contains(t, AntigravityDefaultTestModelID, "gemini-")
}

func TestBuildGeminiTestRequest_LeavesBudgetForVisibleText(t *testing.T) {
	payload, err := (&AntigravityGatewayService{}).buildGeminiTestRequest("project-1", "gemini-3.6-flash-tiered")
	require.NoError(t, err)

	var wrapped struct {
		Request struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(payload, &wrapped))
	require.Equal(t, defaultGeminiTextTestPrompt, wrapped.Request.Contents[0].Parts[0].Text)
	require.Equal(t, antigravityConnectionTestMaxOutputTokens, wrapped.Request.GenerationConfig.MaxOutputTokens)
	require.Greater(t, wrapped.Request.GenerationConfig.MaxOutputTokens, 1)
}

func TestCompleteAntigravityAccountTest_ReportsSuccessfulEmptyResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/5/test", nil)

	(&AccountTestService{}).completeAntigravityAccountTest(ctx, "")

	body := recorder.Body.String()
	require.Contains(t, body, `"type":"status"`)
	require.Contains(t, body, antigravityEmptyTextStatus)
	require.Contains(t, body, `"type":"test_complete"`)
	require.NotContains(t, body, `"type":"content"`)
}
