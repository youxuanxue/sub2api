//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestChatInBandErrorPreservesSemanticStatusWithoutDiagnostics(t *testing.T) {
	for _, status := range []int{401, 403, 429, 529} {
		for _, path := range openAIStreamErrorStatusPaths {
			payload := []byte(`{"error":{"type":"upstream_error","message":"failed","steer":{"message":"tenant-canary"}}}`)
			// These paths are the existing classifier's compatibility inputs.
			payload, err := sjson.SetBytes(payload, path, status)
			require.NoError(t, err)
			detected := detectChatStreamError(string(payload))
			require.NotNil(t, detected)
			require.Equal(t, status, openAIStreamFailedEventSemanticStatus(detected.payload, extractOpenAISSEErrorMessage(detected.payload)))
			require.NotContains(t, string(detected.payload), "tenant-canary")
			public, _ := sanitizeOpenAIResponseFailedEventForClient(detected.payload, "error", false)
			require.False(t, gjson.GetBytes(public, "error.status_code").Exists())
		}
	}
}

func TestChatInBandErrorPreservesFailureAndUsage(t *testing.T) {
	for _, path := range []string{"raw", "responses", "buffered", "messages"} {
		for _, output := range []bool{false, true} {
			t.Run(path+"/"+map[bool]string{false: "before", true: "after"}[output], func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				wire := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"
				if output {
					wire += "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n"
				}
				wire += "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":2}}\n\ndata: {\"error\":{\"type\":\"server_error\",\"message\":\"temporary server failure\"}}\n\ndata: [DONE]\n\n"
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(wire))}
				svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				account := rawChatCompletionsTestAccount()
				var result *OpenAIForwardResult
				var err error
				switch path {
				case "raw":
					result, err = svc.streamRawChatCompletions(c, resp, account, "test", "test", "test", nil, nil, time.Now(), 0)
				case "responses":
					result, err = svc.streamChatCompletionsAsResponses(c, resp, "test", nil, nil, false, nil, "test", "test", nil, nil, time.Now(), account)
				case "buffered":
					result, err = svc.bufferStreamChatCompletionsAsResponses(c, resp, "test", "test", "test", nil, nil, time.Now(), account)
				case "messages":
					result, err = svc.streamChatCompletionsAsAnthropic(c, resp, account, "test", "test", "test", nil, nil, time.Now())
				}
				require.Error(t, err)
				require.NotNil(t, result)
				require.Equal(t, 7, result.Usage.InputTokens)
				require.Equal(t, 2, result.Usage.OutputTokens)
				var failover *UpstreamFailoverError
				if !output || path == "buffered" {
					require.True(t, errors.As(err, &failover), "%v", err)
					require.Empty(t, rec.Body.String())
				} else {
					require.False(t, errors.As(err, &failover))
					require.Contains(t, rec.Body.String(), "partial")
					require.Contains(t, rec.Body.String(), "error")
				}
				require.NotContains(t, rec.Body.String(), "response.completed")
			})
		}
	}
}
