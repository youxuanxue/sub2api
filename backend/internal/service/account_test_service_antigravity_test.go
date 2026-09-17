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

func TestMapAntigravityModel_ConvergedSurfaceFloor(t *testing.T) {
	mapping, ok := accountModelMappingForAccount(context.Background(), &Account{Platform: PlatformAntigravity}, nil, nil, nil)
	require.True(t, ok)
	account := &Account{Platform: PlatformAntigravity, Credentials: map[string]any{"model_mapping": modelMappingToAny(mapping)}}
	require.Equal(t, "gemini-3.8-flash-medium", MapAntigravityModel(account, "gemini-3.8-flash"))
	require.Equal(t, "gemini-3.8-flash-medium", MapAntigravityModel(account, "gemini-3-flash-preview"))
	require.Equal(t, "gemini-3.6-flash-tiered", MapAntigravityModel(account, "gemini-3.5-flash-lite"))
	require.Empty(t, MapAntigravityModel(account, "claude-sonnet-4-6"))
}

func TestAntigravityDefaultTestModelID_IsGeminiWire(t *testing.T) {
	require.True(t, len(AntigravityDefaultTestModelID) > len("gemini-"))
	require.Contains(t, AntigravityDefaultTestModelID, "gemini-")
}

func TestBuildGeminiTestRequest_LeavesBudgetForVisibleText(t *testing.T) {
	payload, err := (&AntigravityGatewayService{}).buildGeminiTestRequest("project-1", "gemini-3.6-flash-tiered", "")
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

func TestBuildGeminiTestRequest_ImageModelRequestsModalities(t *testing.T) {
	payload, err := (&AntigravityGatewayService{}).buildGeminiTestRequest("project-1", "gemini-3.1-flash-image", "draw a red square")
	require.NoError(t, err)

	var wrapped struct {
		Model   string `json:"model"`
		Request struct {
			Contents []struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				ResponseModalities []string `json:"responseModalities"`
				ImageConfig        struct {
					AspectRatio string `json:"aspectRatio"`
				} `json:"imageConfig"`
			} `json:"generationConfig"`
		} `json:"request"`
	}
	require.NoError(t, json.Unmarshal(payload, &wrapped))
	require.Equal(t, "gemini-3.1-flash-image", wrapped.Model)
	require.Equal(t, "draw a red square", wrapped.Request.Contents[0].Parts[0].Text)
	require.Equal(t, []string{"TEXT", "IMAGE"}, wrapped.Request.GenerationConfig.ResponseModalities)
	require.Equal(t, "1:1", wrapped.Request.GenerationConfig.ImageConfig.AspectRatio)
}

func TestExtractImagesFromSSEResponse(t *testing.T) {
	body := []byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"QUJD\"}}]}}]}}\n\n")
	images := extractImagesFromSSEResponse(body)
	require.Len(t, images, 1)
	require.Equal(t, "image/png", images[0].MimeType)
	require.Equal(t, "QUJD", images[0].Data)
}

func TestApplyErrorPolicy_ReadOnlySkipsAccountMutation(t *testing.T) {
	svc := &AntigravityGatewayService{}
	handled, status, err := svc.applyErrorPolicy(antigravityRetryLoopParams{
		readOnlyAccountState: true,
		handleError:          testConnectionHandleError,
	}, http.StatusTooManyRequests, nil, []byte(`{"error":{"status":"RESOURCE_EXHAUSTED"}}`))
	require.False(t, handled)
	require.Equal(t, http.StatusTooManyRequests, status)
	require.NoError(t, err)
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
