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

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsAnthropic_VolcKimiMessagesVideoBecomesChatVideoURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stubVolcKimiVideoProbe(t, func(context.Context, string) error { return nil })

	dataURL := "data:video/mp4;base64," + strings.Repeat("A", ((volcKimiMinDataVideoBytes+2)/3)*4)
	body := []byte(`{
		"model":"kimi-k3",
		"max_tokens":32,
		"stream":false,
		"messages":[{
			"role":"user",
			"content":[
				{"type":"video","source":{"type":"url","url":"` + dataURL + `"}},
				{"type":"text","text":"describe the video"}
			]
		}]
	}`)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"chatcmpl_video","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":4,"total_tokens":124,"prompt_tokens_details":{"cached_tokens":0}}}`,
		)),
	}}
	account := volcAgentPlanKimiAccountForTest()
	account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)

	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, strings.HasSuffix(upstream.lastReq.URL.String(), "/api/plan/v3/chat/completions"))

	content := gjson.GetBytes(upstream.lastBody, "messages.0.content")
	require.True(t, content.IsArray(), "media must stay as content parts: %s", content.Raw)
	require.Equal(t, "video_url", content.Get("0.type").String())
	require.Equal(t, dataURL, content.Get("0.video_url.url").String())
	require.Equal(t, "text", content.Get("1.type").String())
	require.Equal(t, "describe the video", content.Get("1.text").String())
	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 0, gjson.Get(rec.Body.String(), "usage.cache_read_input_tokens").Int())
}

func TestForwardAsAnthropic_VolcKimiMessagesEmptyVideoURLRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{
		"model":"kimi-k3",
		"max_tokens":16,
		"messages":[{"role":"user","content":[{"type":"video_url","video_url":{"url":""}}]}]
	}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))

	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	account := volcAgentPlanKimiAccountForTest()
	account.Extra[openai_compat.ExtraKeyResponsesMode] = string(openai_compat.ResponsesSupportModeForceChatCompletions)

	_, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "nonempty video URL")
	require.Nil(t, upstream.lastReq)
}
