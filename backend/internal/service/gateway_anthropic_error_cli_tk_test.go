//go:build unit

package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Opt-in loopback fixture for the real Claude CLI. Only synthetic conversations
// are accepted by the accompanying driver; no upstream network transport exists.
func TestClaudeContextCLIFixture(t *testing.T) {
	path := os.Getenv("TK_CLAUDE_CONTEXT_FIXTURE_ADDR_FILE")
	if path == "" {
		t.Skip("real CLI acceptance fixture is opt-in")
	}
	gin.SetMode(gin.TestMode)
	var mu sync.Mutex
	state := struct {
		Reject      int  `json:"reject"`
		Delay       bool `json:"delay"`
		InputTokens int  `json:"input_tokens"`
	}{InputTokens: 100}
	var seen []map[string]any
	transport := &claudeContextFixtureUpstream{respond: func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		messages := gjson.GetBytes(body, "messages").Array()
		last := ""
		if len(messages) > 0 {
			last = strings.ToLower(messages[len(messages)-1].Raw)
		}
		summary := strings.Contains(last, "your task is to create a detailed summary") || strings.Contains(last, "summarize the conversation")
		mu.Lock()
		reject := state.Reject > 0
		if reject {
			state.Reject--
		}
		delay, tokens := state.Delay, state.InputTokens
		seen = append(seen, map[string]any{"messages": len(messages), "bytes": len(body), "summary": summary, "reject": reject, "stream": gjson.GetBytes(body, "stream").Bool()})
		mu.Unlock()
		if reject {
			if delay {
				time.Sleep(1100 * time.Millisecond)
			}
			payload, _ := json.Marshal(gin.H{"type": "error", "error": gin.H{"type": "<nil>", "message": testTokenseaContextError}})
			return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
		}
		answer := "SYNTHETIC_OK " + strings.Repeat("The fictional project uses blue squares and green circles. ", 100)
		if summary {
			answer = "<summary>Synthetic conversation about blue squares and green circles. Continue the fictional task.</summary>"
		}
		if !gjson.GetBytes(body, "stream").Bool() {
			payload, _ := json.Marshal(gin.H{"id": "msg_synthetic", "type": "message", "role": "assistant", "model": gjson.GetBytes(body, "model").String(), "content": []gin.H{{"type": "text", "text": answer}}, "stop_reason": "end_turn", "usage": gin.H{"input_tokens": tokens, "output_tokens": 32}})
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
		}
		msg := gin.H{"id": "msg_synthetic", "type": "message", "role": "assistant", "model": gjson.GetBytes(body, "model").String(), "content": []any{}, "stop_reason": nil, "usage": gin.H{"input_tokens": tokens, "output_tokens": 0}}
		var stream strings.Builder
		for _, event := range []gin.H{
			{"type": "message_start", "message": msg},
			{"type": "content_block_start", "index": 0, "content_block": gin.H{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 0, "delta": gin.H{"type": "text_delta", "text": answer}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": gin.H{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": gin.H{"output_tokens": 32}},
			{"type": "message_stop"},
		} {
			data, _ := json.Marshal(event)
			stream.WriteString("event: " + event["type"].(string) + "\ndata: " + string(data) + "\n\n")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream.String()))}, nil
	}}
	cfg := rawChatCompletionsTestConfig()
	cfg.Gateway.StreamKeepaliveInterval = 1
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: transport}
	router := gin.New()
	done := make(chan struct{})
	router.GET("/fixture", func(c *gin.Context) { mu.Lock(); defer mu.Unlock(); c.JSON(200, seen) })
	router.POST("/fixture", func(c *gin.Context) {
		mu.Lock()
		defer mu.Unlock()
		require.NoError(t, json.NewDecoder(c.Request.Body).Decode(&state))
		seen = nil
		c.JSON(200, gin.H{"ok": true})
	})
	router.POST("/stop", func(c *gin.Context) { c.Status(204); close(done) })
	router.POST("/v1/messages", func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		_, _ = svc.forwardAnthropicViaNativeMessages(c.Request.Context(), c, tokenseaNativeMessagesAccount(), body, "")
	})
	router.POST("/v1/messages/count_tokens", func(c *gin.Context) { c.JSON(200, gin.H{"input_tokens": 100}) })
	server := httptest.NewServer(router)
	defer server.Close()
	require.NoError(t, os.WriteFile(path, []byte(server.URL), 0600))
	select {
	case <-done:
	case <-time.After(12 * time.Minute):
		t.Fatal("CLI fixture timed out")
	}
}

type claudeContextFixtureUpstream struct {
	HTTPUpstream
	respond func(*http.Request) (*http.Response, error)
}

func (u *claudeContextFixtureUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	return u.respond(req)
}
