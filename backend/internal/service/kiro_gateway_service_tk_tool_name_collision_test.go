//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func kiroToolNameCollisionRequestBody(t *testing.T, names ...string) *ParsedRequest {
	t.Helper()
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{
			"name":         name,
			"input_schema": map[string]any{"type": "object"},
		})
	}
	body, err := json.Marshal(map[string]any{
		"model":      "claude-opus-5.5",
		"messages":   []map[string]any{{"role": "user", "content": "hi"}},
		"max_tokens": 16,
		"stream":     false,
		"tools":      tools,
	})
	require.NoError(t, err)
	return &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5.5", Stream: false}
}

// Kiro normalizes tool names to camelCase, so `Read` and `read` collapse into
// one. The reverse map keeps only the last writer, so the upstream would get
// two specs named "read" and the client could receive a tool_use for a name it
// never declared. The whole request must fail instead, before any rewriting.
func TestKiroGatewayService_Forward_RejectsCollidingToolNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	upstream := &kiroFakeUpstream{}
	svc := NewKiroGatewayService(upstream, nil, nil)

	result, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
		kiroToolNameCollisionRequestBody(t, "Read", "read"), time.Now())

	require.Nil(t, result)
	var invalidRequestErr *KiroInvalidRequestError
	require.ErrorAs(t, err, &invalidRequestErr)
	require.Equal(t, http.StatusBadRequest, invalidRequestErr.StatusCode)
	require.Contains(t, invalidRequestErr.Message, "rename one of the tools")
	require.False(t, upstream.gotRequest, "a colliding tool set must not reach Kiro upstream")
}

// Benign direction: a distinct tool set is forwarded normally, so the guard
// does not block ordinary requests.
func TestKiroGatewayService_Forward_AcceptsDistinctToolNames(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	upstream := &kiroFakeUpstream{}
	svc := NewKiroGatewayService(upstream, nil, nil)

	_, err := svc.Forward(context.Background(), c, newKiroAccountForTest(),
		kiroToolNameCollisionRequestBody(t, "Read", "Write", "bash"), time.Now())

	var invalidRequestErr *KiroInvalidRequestError
	require.NotErrorAs(t, err, &invalidRequestErr, "a distinct tool set must not be rejected")
	require.True(t, upstream.gotRequest, "a valid request must reach Kiro upstream")
}
