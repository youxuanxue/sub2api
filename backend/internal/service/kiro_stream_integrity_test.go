//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestKiroGatewayService_PreservesIncrementalText(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var frames []byte
			for _, chunk := range []string{"scre", "en", "\n", "foo", "foo", "\n", "sha256=", "1111", "1111", "2222", "2222"} {
				payload, err := json.Marshal(map[string]string{"content": chunk})
				require.NoError(t, err)
				frames = append(frames, buildKiroEventStreamMessage("assistantResponseEvent", payload)...)
			}
			upstream := &kiroSequenceUpstream{bodies: [][]byte{appendKiroTerminalStop(frames, "END_TURN")}}
			svc := NewKiroGatewayService(upstream, nil, nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(), newKiroParsedRequestForTest(streaming), time.Now())
			require.NoError(t, err)
			require.Equal(t, 1, upstream.calls)
			var got strings.Builder
			if streaming {
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					var event struct {
						Type  string `json:"type"`
						Delta struct {
							Text string `json:"text"`
						} `json:"delta"`
					}
					require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event))
					if event.Type == "content_block_delta" {
						got.WriteString(event.Delta.Text)
					}
				}
			} else {
				var response struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
				for _, block := range response.Content {
					got.WriteString(block.Text)
				}
			}
			require.Equal(t, "screen\nfoofoo\nsha256=1111111122222222", got.String())
		})
	}
}
