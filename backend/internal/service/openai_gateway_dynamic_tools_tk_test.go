//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsAnthropic_DynamicToolErrorsReturn400BeforeUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, block, want string
	}{
		{"removal", `{"type":"tool_removal","tool":{"type":"tool_reference","name":"late"}}`, "tool_removal"},
		{"unknown reference", `{"type":"tool_addition","tool":{"type":"tool_reference","name":"missing"}}`, "unknown tool_reference"},
		{"malformed payload", `{"type":"tool_addition","tool":"invalid"}`, "invalid dynamic tool history"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				body := []byte(fmt.Sprintf(`{"model":"gpt-5.4","max_tokens":32,"stream":%t,"messages":[{"role":"user","content":"hello"},{"role":"system","content":[%s]}]}`, stream, tc.block))
				assertDynamicToolForwardRejected(t, rawChatCompletionsTestAccount(), body, tc.want)
			})
		}
	}
}

func TestForwardAsAnthropic_DynamicToolsChatOnlyReturns400BeforeUpstream(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","max_tokens":32,"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"late","input_schema":{"type":"object"}}}}]}]}`)
	assertDynamicToolForwardRejected(t, forceChatMessagesFallbackAccount(), body, "Responses bridge")
}

func assertDynamicToolForwardRejected(t *testing.T, account *Account, body []byte, want string) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	upstream := &httpUpstreamRecorder{err: errors.New("unexpected upstream request")}
	svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
	result, err := svc.ForwardAsAnthropic(context.Background(), c, account, body, "", "")
	require.ErrorContains(t, err, want)
	require.Nil(t, result)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), want)
	require.Empty(t, upstream.requests, "rejected input must not contact the upstream")
}
