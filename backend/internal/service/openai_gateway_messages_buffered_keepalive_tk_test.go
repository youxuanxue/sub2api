//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardAsAnthropic_BufferedHeaderWaitPreservesJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streamField := range []string{"", `,"stream":false`} {
		for _, failed := range []bool{false, true} {
			name := "omitted"
			if streamField != "" {
				name = "false"
			}
			if failed {
				name += "/upstream_error"
			}
			t.Run(name, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}]` + streamField + `}`)
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
				payload := "data: " + `{"type":"response.completed","response":{"id":"resp_buffered","object":"response","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":5,"output_tokens":2,"total_tokens":7}}}` + "\n\n"
				if failed {
					payload = buildResponsesFailedSSEStream("invalid_request_error", "Content policy violation")
				}
				upstream := &httpUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(payload)),
				}}
				cfg := rawChatCompletionsTestConfig()
				cfg.Gateway.StreamKeepaliveInterval = 1
				svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &delayingHTTPUpstream{delay: 1100 * time.Millisecond, inner: upstream}}
				result, err := svc.ForwardAsAnthropic(context.Background(), c, rawChatCompletionsTestAccount(), body, "", "")
				require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
				var response map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response), "buffered clients must receive one JSON document")
				if failed {
					require.Error(t, err)
					require.Equal(t, http.StatusBadRequest, rec.Code)
					require.Equal(t, "error", response["type"])
				} else {
					require.NoError(t, err)
					require.Equal(t, http.StatusOK, rec.Code)
					require.Equal(t, "end_turn", response["stop_reason"])
					require.Contains(t, rec.Body.String(), `"text":"ok"`)
					require.NotNil(t, result)
					require.False(t, result.Stream)
					require.Equal(t, 5, result.Usage.InputTokens)
					require.Equal(t, 2, result.Usage.OutputTokens)
				}
			})
		}
	}
}
