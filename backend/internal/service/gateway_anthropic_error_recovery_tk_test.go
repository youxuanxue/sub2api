//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

const testTokenseaContextError = "输入内容过长，请减少 Prompt 中的 token 数量后重试 (cch_session_id: 3ca32a58-2a77-4627-9051-f36e7e8142e2) (request id: request-123)"

func TestUS062_AnthropicContextErrorRecovery(t *testing.T) {
	for _, slow := range []bool{false, true} {
		t.Run(fmt.Sprintf("heartbeat=%t", slow), func(t *testing.T) {
			body := []byte(`{"model":"claude-fable-5-1","max_tokens":32,"stream":true,"messages":[{"role":"user","content":"hello"}]}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
			cfg := rawChatCompletionsTestConfig()
			cfg.Gateway.StreamKeepaliveInterval = 1
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"type":"<nil>","message":"` + testTokenseaContextError + `"}}`))}}
			delay := time.Duration(0)
			if slow {
				delay = 1100 * time.Millisecond
			}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &delayingHTTPUpstream{delay: delay, inner: upstream}}
			result, err := svc.ForwardAsAnthropic(context.Background(), c, tokenseaNativeMessagesAccount(), body, "", "")
			require.Error(t, err)
			require.Nil(t, result)
			require.Len(t, upstream.bodies, 1, "context rejection must not be retried unchanged")
			events, _ := c.Get(OpsUpstreamErrorsKey)
			require.Equal(t, testTokenseaContextError, events.([]*OpsUpstreamErrorEvent)[0].Message, "retain supplier evidence")
			payload := rec.Body.String()
			if slow {
				require.Equal(t, 200, rec.Code)
				require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
				require.Contains(t, payload, "event: ping\n")
				require.Equal(t, 1, strings.Count(payload, "event: error\n"))
				require.NotContains(t, payload, "message_stop")
				for _, frame := range strings.Split(strings.TrimSpace(payload), "\n\n") {
					require.True(t, strings.HasPrefix(frame, "event: "), "no bare JSON in SSE: %q", frame)
				}
				streamErr, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 400, streamErr.IntendedStatus)
				require.True(t, streamErr.CountTowardsSLA)
				_, payload, _ = strings.Cut(payload, "event: error\ndata: ")
			} else {
				require.Equal(t, 400, rec.Code)
				require.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			}
			require.Equal(t, "invalid_request_error", gjson.Get(payload, "error.type").String())
			require.Equal(t, "prompt is too long: "+testTokenseaContextError, gjson.Get(payload, "error.message").String())
		})
	}
}

func TestUS062_AnthropicContextErrorMatchBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, message string
		status        int
		want          bool
	}{
		{"exact", "输入内容过长，请减少 Prompt 中的 token 数量后重试", 400, true},
		{"supplier_ids", testTokenseaContextError, 400, true},
		{"english", "prompt is too long: 1031462 tokens > 1000000 maximum", 400, false},
		{"different_400", "输入内容不合法", 400, false},
		{"quoted", "Invalid value: " + testTokenseaContextError, 400, false},
		{"suffix", testTokenseaContextError + " but another failure occurred", 400, false},
		{"wrong_status", testTokenseaContextError, 429, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			writeAnthropicError(c, tt.status, "invalid_request_error", tt.message)
			want := tt.message
			if tt.want {
				want = "prompt is too long: " + tt.message
			}
			require.Equal(t, want, gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
			require.Equal(t, tt.status, rec.Code)
		})
	}
}

func TestUS062_AnthropicNativeErrorFraming(t *testing.T) {
	for _, tt := range []struct {
		name, body, wantType string
	}{
		{"pretty_json", "{\n  \"error\": {\"type\": \"invalid_request_error\", \"message\": \"bad schema\", \"param\": \"tools\"},\n  \"request_id\": \"req_123\"\n}", "invalid_request_error"},
		{"wrong_type", `{"type":"error","error":{"type":"permission_error","message":"` + testTokenseaContextError + `"}}`, "permission_error"},
		{"non_json", "upstream rejected request\ninvalid body", "invalid_request_error"},
	} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%t", tt.name, committed), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Header("Content-Type", "text/event-stream")
				if committed {
					_, _ = c.Writer.WriteString("event: ping\ndata: {}\n\n")
					c.Writer.Flush()
				}
				tkWriteAnthropicPassthrough400(c, []byte(tt.body))
				if !committed {
					require.Equal(t, 400, rec.Code)
					require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
					require.Equal(t, tt.body, rec.Body.String(), "unrelated raw 400 remains byte-for-byte intact")
					return
				}
				require.Equal(t, 200, rec.Code)
				require.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
				frames := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
				require.Len(t, frames, 2)
				require.True(t, strings.HasPrefix(frames[1], "event: error\ndata: "))
				payload := strings.TrimPrefix(frames[1], "event: error\ndata: ")
				require.True(t, json.Valid([]byte(payload)))
				require.NotContains(t, payload, "\n", "one complete SSE data line")
				require.Equal(t, "error", gjson.Get(payload, "type").String())
				require.Equal(t, tt.wantType, gjson.Get(payload, "error.type").String())
				if tt.name == "pretty_json" {
					require.Equal(t, "tools", gjson.Get(payload, "error.param").String())
					require.Equal(t, "req_123", gjson.Get(payload, "request_id").String())
				} else if tt.name == "wrong_type" {
					require.Equal(t, testTokenseaContextError, gjson.Get(payload, "error.message").String())
				}
				failure, ok := GetOpsStreamError(c)
				require.True(t, ok)
				require.Equal(t, 400, failure.IntendedStatus)
				require.Equal(t, tt.wantType, failure.ErrType)
			})
		}
	}
}
