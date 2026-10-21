//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroReliabilityGatewayFaultImpact(t *testing.T) {
	good := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"SAFE"}`))
	stop := buildKiroEventStreamMessage("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))
	bad := buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":"corrupt"}`))
	bad[len(bad)-1] ^= 1
	utf := append([]byte(`{"content":"`), 0xff)
	utf = append(utf, []byte(`"}`)...)
	cases := map[string][]byte{"bad_crc": append(append([]byte{}, bad...), stop...), "bad_json": append(append(append([]byte{}, good...), buildKiroEventStreamMessage("assistantResponseEvent", []byte(`{"content":`))...), stop...), "bad_utf8": append(buildKiroEventStreamMessage("assistantResponseEvent", utf), stop...), "after_stop": append(append(append([]byte{}, good...), stop...), good...)}
	for name, raw := range cases {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", name, stream), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				u := &kiroSequenceUpstream{bodies: [][]byte{raw}}
				s := NewKiroGatewayService(u, nil, nil)
				body, _ := json.Marshal(map[string]any{"model": "claude-opus-4-6", "stream": stream, "max_tokens": 32, "messages": []map[string]any{{"role": "user", "content": "hi"}}})
				r, e := s.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-6", Stream: stream}, time.Now())
				t.Logf("http=%d success=%t result=%t calls=%d message_stop=%t replacement=%t body=%q", rec.Code, e == nil, r != nil, u.calls, strings.Contains(rec.Body.String(), "event: message_stop"), strings.Contains(rec.Body.String(), "\ufffd"), rec.Body.String())
				require.Error(t, e)
				require.Nil(t, r)
				require.Equal(t, 1, u.calls)
				require.NotContains(t, rec.Body.String(), "event: message_stop")
				require.NotContains(t, rec.Body.String(), "\ufffd")
				if rec.Body.Len() == 0 {
					var failure *UpstreamFailoverError
					require.ErrorAs(t, e, &failure)
					require.Equal(t, http.StatusBadGateway, failure.StatusCode)
				} else {
					require.Contains(t, rec.Body.String(), "event: error")
				}
			})
		}
	}
}
func TestKiroReliabilityGatewayLateIdentity(t *testing.T) {
	raw := append(buildKiroEventStreamMessage("toolUseEvent", []byte(`{"name":"Read","input":"{\"path\":"}`)), buildKiroEventStreamMessage("toolUseEvent", []byte(`{"toolUseId":"late-id","input":"\"a.txt\"}","stop":true}`))...)
	raw = append(raw, buildKiroEventStreamMessage("metadataEvent", []byte(`{"stopReason":"TOOL_USE"}`))...)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			u := &kiroSequenceUpstream{bodies: [][]byte{raw}}
			s := NewKiroGatewayService(u, nil, nil)
			body, _ := json.Marshal(map[string]any{"model": "claude-opus-4-6", "stream": stream, "max_tokens": 32, "messages": []map[string]any{{"role": "user", "content": "read a.txt"}}})
			r, e := s.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-4-6", Stream: stream}, time.Now())
			t.Logf("result=%t error=%v calls=%d tool_delivered=%t", r != nil, e, u.calls, strings.Contains(rec.Body.String(), "late-id"))
			require.NoError(t, e)
			require.NotNil(t, r)
			require.Equal(t, 1, u.calls)
			if !stream {
				var response struct {
					Content []struct {
						ID    string
						Name  string
						Input map[string]any
					}
					StopReason string `json:"stop_reason"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				require.Len(t, response.Content, 1)
				require.Equal(t, "late-id", response.Content[0].ID)
				require.Equal(t, "Read", response.Content[0].Name)
				require.Equal(t, map[string]any{"path": "a.txt"}, response.Content[0].Input)
				require.Equal(t, "tool_use", response.StopReason)
			} else {
				require.Contains(t, rec.Body.String(), `"id":"late-id"`)
				require.Contains(t, rec.Body.String(), `"name":"Read"`)
				require.Contains(t, rec.Body.String(), `"stop_reason":"tool_use"`)
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
			}
		})
	}
}
