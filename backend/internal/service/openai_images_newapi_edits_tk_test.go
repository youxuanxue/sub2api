package service

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNewAPIOpenAIImageEditsAccountRequiresOpenAIChannelAndBaseURL(t *testing.T) {
	relay := &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai", "api_key": "test-key"},
	}
	require.True(t, NewAPIOpenAIImageEditsAccount(relay))

	ali := *relay
	ali.ChannelType = newapiconstant.ChannelTypeAli
	require.False(t, NewAPIOpenAIImageEditsAccount(&ali))

	missingBase := *relay
	missingBase.Credentials = map[string]any{"api_key": "test-key"}
	require.False(t, NewAPIOpenAIImageEditsAccount(&missingBase))
	require.Equal(t, "", newAPIOpenAIImageEditsBaseURL(&ali))
}

func TestCandidateNewAPIOpenAIImageEditsAccountUsesSelectedAccount(t *testing.T) {
	relay := &Account{
		ID:          454,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": "https://agent.tokensea.ai"},
	}
	ctx := context.WithValue(context.Background(), candidateRequestContextKey{}, &CandidateRequest{
		current: &candidateExecutionPath{account: relay},
	})
	require.Equal(t, int64(454), CandidateNewAPIOpenAIImageEditsAccount(ctx).ID)

	ctx = context.WithValue(context.Background(), candidateRequestContextKey{}, &CandidateRequest{
		current: &candidateExecutionPath{account: &Account{Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli}},
	})
	require.Nil(t, CandidateNewAPIOpenAIImageEditsAccount(ctx))
}

func TestForwardImages_NewAPIOpenAIChannelEditUsesAccountBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "replace background"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
		httpUpstream: &httpUpstreamRecorder{
			resp: &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Content-Type": []string{"application/json"},
					"X-Request-Id": []string{"req_img_edit_newapi"},
				},
				Body: io.NopCloser(strings.NewReader(`{"created":1710000008,"data":[{"b64_json":"ZWRpdGVk"}]}`)),
			},
		},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)

	account := &Account{
		ID:          454,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://agent.tokensea.ai",
		},
	}

	result, err := svc.ForwardImages(context.Background(), c, account, body.Bytes(), parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)

	upstream, ok := svc.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.Equal(t, "https://agent.tokensea.ai/v1/images/edits", upstream.lastReq.URL.String())
	require.Equal(t, "Bearer test-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Contains(t, upstream.lastReq.Header.Get("Content-Type"), "multipart/form-data")
	require.Contains(t, string(upstream.lastBody), "gpt-image-2")
	require.Equal(t, "ZWRpdGVk", gjson.Get(rec.Body.String(), "data.0.b64_json").String())
}

func TestForwardImages_NewAPIOpenAIChannelGenerationDoesNotFillBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)

	body := []byte(`{"model":"gpt-image-2","prompt":"a cat"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: &httpUpstreamRecorder{},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body)
	require.NoError(t, err)

	account := &Account{
		ID:          454,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{
			"api_key":  "test-api-key",
			"base_url": "https://agent.tokensea.ai",
		},
	}

	_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, GatewayFailureReason("newapi_image_upstream_unsupported"), failover.Reason)
	upstream, ok := svc.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.Nil(t, upstream.lastReq)
}

func TestForwardImages_OtherNewAPIChannelDoesNotUseOfficialOpenAI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "a cat"))
	imagePart, err := writer.CreateFormFile("image", "source.png")
	require.NoError(t, err)
	_, err = imagePart.Write([]byte("png-image-content"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: &httpUpstreamRecorder{},
	}
	parsed, err := svc.ParseOpenAIImagesRequest(c, body.Bytes())
	require.NoError(t, err)

	account := &Account{
		ID:          17,
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"api_key":  "ali-key",
			"base_url": "https://dashscope.aliyuncs.com",
		},
	}

	_, err = svc.ForwardImages(context.Background(), c, account, body.Bytes(), parsed, "")
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.Equal(t, GatewayFailureReason("newapi_image_upstream_unsupported"), failover.Reason)
	upstream, ok := svc.httpUpstream.(*httpUpstreamRecorder)
	require.True(t, ok)
	require.Nil(t, upstream.lastReq)
}
