package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func volcEnginePlanTestAccount() *Account {
	return &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 45,
		Credentials: map[string]any{"base_url": newapiintegration.VolcEngineAgentPlanBaseURL, "api_key": "test-plan-key"}}
}

func TestVolcEnginePlanAliasesAndProviderIsolation(t *testing.T) {
	t.Parallel()
	account := volcEnginePlanTestAccount()
	mapping, ok := accountModelMappingForAccount(context.Background(), account, nil, nil, nil)
	require.True(t, ok)
	display := NewAPIModelDisplayIDsForAccount(account)
	for alias, target := range newAPIVolcEngineAgentPlanModelAliases() {
		require.Equal(t, target, mapping[alias])
		require.Equal(t, target, mapping[target], "aliases must resolve in one hop")
		require.NotContains(t, display, alias)
		billingAccount := volcEnginePlanTestAccount()
		rawMapping := make(map[string]any, len(mapping))
		for key, value := range mapping {
			rawMapping[key] = value
		}
		billingAccount.Credentials["model_mapping"] = rawMapping
		require.Equal(t, target, settleBillingOnAccountServedModel(billingAccount, alias, alias))
	}
	require.Contains(t, tkServedModelsManifestPresetIDsByChannelType(17), "glm-5.2")
	require.Contains(t, tkServedModelsManifestPresetIDsByChannelType(25), "kimi-k2.6")
	for _, model := range []string{"doubao-embedding-vision", "doubao-seedream-5.0-lite"} {
		require.Contains(t, display, model)
		require.Equal(t, model, mapping[model])
	}
	payg := volcEnginePlanTestAccount()
	payg.Credentials["base_url"] = "https://ark.cn-beijing.volces.com"
	require.NotContains(t, NewAPIModelMappingPresetIDsForAccount(payg), "doubao-seedream-5.0-lite")
}

func TestVolcEnginePlanNativeMediaForward(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, input, response, path string
		imageTokens, count                    int
	}{
		{"text", BridgeEndpointEmbeddings, `{"model":"doubao-embedding-vision","input":["hello","world"],"dimensions":1024}`, `{"data":[{"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":24,"total_tokens":24}}`, "embeddings", 0, 0},
		{"multimodal", BridgeEndpointEmbeddings, `{"model":"doubao-embedding-vision","input":[{"type":"text","text":"test"},{"type":"image_url","image_url":{"url":"https://example.com/test.png"}}]}`, `{"data":{"embedding":[0.1,0.2]},"usage":{"prompt_tokens":1339,"total_tokens":1339,"prompt_tokens_details":{"text_tokens":27,"image_tokens":1312}}}`, "embeddings/multimodal", 1312, 0},
		{"images", BridgeEndpointImages, `{"model":"doubao-seedream-5.0-lite","prompt":"test","n":3,"size":"2K"}`, `{"data":[{"url":"https://example.com/test.png"}],"usage":{"generated_images":1,"output_tokens":16384,"total_tokens":16384}}`, "images/generations", 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.input)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+tc.endpoint, bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(tc.response))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			account := volcEnginePlanTestAccount()
			require.False(t, svc.ShouldDispatchToNewAPIBridge(account, tc.endpoint))
			var result *OpenAIForwardResult
			var err error
			if tc.endpoint == BridgeEndpointImages {
				result, err = svc.ForwardAsImageGenerationsDispatched(context.Background(), c, account, body, "")
			} else {
				result, err = svc.ForwardAsEmbeddingsDispatched(context.Background(), c, account, body, "")
			}
			require.NoError(t, err)
			require.Equal(t, newapiintegration.VolcEngineAgentPlanBaseURL+"/"+tc.path, upstream.lastReq.URL.String())
			require.Equal(t, "Bearer test-plan-key", upstream.lastReq.Header.Get("Authorization"))
			require.Equal(t, tc.imageTokens, result.Usage.ImageInputTokens)
			require.Equal(t, tc.count, result.ImageCount, "bill returned images, not requested n")
		})
	}
}

func TestNormalizeVolcEnginePlanEmbeddingResponse(t *testing.T) {
	got, err := normalizeVolcEnginePlanEmbeddingResponse([]byte(`{"data":{"embedding":[0.1,0.2]},"usage":{"prompt_tokens":10,"prompt_tokens_details":{"image_tokens":8}}}`))
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(got, "data").IsArray())
	require.Equal(t, int64(8), gjson.GetBytes(got, "usage.prompt_tokens_details.image_tokens").Int())
	for _, invalid := range []string{`{}`, `{"data":{}}`, `{"data":{"embedding":[]}}`, `{"data":{"embedding":["bad"]}}`, `{"data":{"embedding":[1e1000]}}`} {
		_, err := normalizeVolcEnginePlanEmbeddingResponse([]byte(invalid))
		require.Error(t, err)
	}
}

func TestVolcEnginePlanRejectsUnsupportedMediaBeforeSending(t *testing.T) {
	for _, body := range []string{
		`{"model":"doubao-seedream-5.0-lite","prompt":"test","stream":true}`,
		`{"model":"doubao-embedding-vision","input":[{"type":"text","text":"hello"}],"encoding_format":"base64"}`,
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
		svc := &OpenAIGatewayService{}
		_, err := svc.ForwardAsEmbeddings(context.Background(), c, volcEnginePlanTestAccount(), []byte(body), "")
		require.Error(t, err)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}

type volcEnginePlanTrackedBody struct {
	io.Reader
	closed bool
}

func (b *volcEnginePlanTrackedBody) Close() error {
	b.closed = true
	return nil
}

func TestVolcEnginePlanLargeImageResponseAndBodyRelease(t *testing.T) {
	image := strings.Repeat("A", 2_400_000)
	response := `{"data":[{"b64_json":"` + image + `"}],"usage":{"generated_images":1,"output_tokens":16384,"total_tokens":16384}}`
	body := &volcEnginePlanTrackedBody{Reader: strings.NewReader(response)}
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: body}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	request := `{"model":"doubao-seedream-5.0-lite","prompt":"test","response_format":"b64_json"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(request))
	result, err := svc.ForwardAsImageGenerationsDispatched(context.Background(), c, volcEnginePlanTestAccount(), []byte(request), "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	require.Equal(t, image, gjson.Get(recorder.Body.String(), "data.0.b64_json").String())
	require.True(t, body.closed, "the original network body must close after replacement")
}
