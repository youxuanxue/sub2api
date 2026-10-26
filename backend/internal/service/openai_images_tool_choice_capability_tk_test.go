//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ChatGPT can report a missing tools parameter even though our Images adapter
// sent it. Treat this as account capability loss only for self-built requests.
func TestForwardImages_ToolChoiceMustBeSpecifiedFailsOver(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const errorBody = `{"error":{"code":null,"message":"Tool choice 'image_generation' must be specified with 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`
	for _, endpoint := range []string{"generations", "edits"} {
		t.Run(endpoint, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
			if endpoint == "edits" {
				body = []byte(`{"model":"gpt-image-2","prompt":"draw a cat","images":[{"image_url":"data:image/png;base64,aW1hZ2U="}]}`)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/images/"+endpoint, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req
			repo := &modelNotFoundAccountRepoStub{}
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusBadRequest,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader(errorBody)),
			}}
			svc := &OpenAIGatewayService{
				rateLimitService: &RateLimitService{accountRepo: repo},
				httpUpstream:     upstream,
			}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{ID: 205, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "test-token"}}

			before := time.Now()
			result, err := svc.ForwardImages(withOpenAIImagesForceResponses(context.Background()), c, account, body, parsed, "")

			require.Nil(t, result)
			// Verify the actual outbound request, not just the builder in isolation.
			require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tool_choice.type").String())
			require.Equal(t, "image_generation", gjson.GetBytes(upstream.lastBody, "tools.0.type").String())
			var failoverErr *UpstreamFailoverError
			require.ErrorAs(t, err, &failoverErr)
			require.False(t, failoverErr.RetryableOnSameAccount)
			require.False(t, c.Writer.Written(), "leave the response open so the handler can try another account")
			require.Len(t, repo.modelRateLimitCalls, 1)
			call := repo.modelRateLimitCalls[0]
			require.Equal(t, account.ID, call.accountID)
			require.Equal(t, openAIImageGenerationRateLimitKey, call.scope)
			require.Equal(t, openAIImageCapabilityLossReason, call.reason)
			require.WithinDuration(t, before.Add(openAIImageCapabilityLossCooldown), call.resetAt, time.Second)
			_, wholeAccountBlocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
			require.False(t, wholeAccountBlocked)
		})
	}
}

func TestToolChoiceMustBeSpecified_ClientRequestsDoNotCoolOrFailover(t *testing.T) {
	body := []byte(`{"error":{"message":"Tool choice 'image_generation' must be specified with 'tools' parameter.","param":"tool_choice","type":"invalid_request_error"}}`)
	repo := &modelNotFoundAccountRepoStub{}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	account := &Account{ID: 206, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadRequest, extractUpstreamErrorMessage(body), body))
	require.False(t, svc.handleOpenAIAccountUpstreamError(context.Background(), account, http.StatusBadRequest, http.Header{}, body, "gpt-5.5"))
	require.Empty(t, repo.modelRateLimitCalls)
	_, wholeAccountBlocked := svc.openaiAccountRuntimeBlockUntil.Load(account.ID)
	require.False(t, wholeAccountBlocked)
}
