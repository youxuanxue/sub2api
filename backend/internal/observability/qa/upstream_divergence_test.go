//go:build unit

package qa

// traj v2 via cc-edges（非 passthrough 网关路径）：转发前的请求侧改写
//（normalize / alias strip / signature-preempt 剥 thinking 等）会让「捕获的
// 客户端请求」≠「产生该响应的真实上游请求」。本文件钉住失真捕获契约：
//   - captureUpstreamRequestBody 仅在 opt-in 且字节真不等时取值（零误标）
//   - buildBlob 把 divergent 上游体落进 request.upstream_body（同套脱敏 +
//     thinking signature 保留）并标 request.upstream_divergent
//   - gin context key 字面量与 service.OpsUpstreamRequestBodyKey 一致
//（双保险：scripts/sentinels/trajectory.json required_file_hooks 同样钉此项）

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpsUpstreamRequestBodyKeyLiteral_MatchesService(t *testing.T) {
	require.Equal(t, service.OpsUpstreamRequestBodyKey, opsUpstreamRequestBodyContextKey,
		"qa duplicates the gin key literal (import cycle); the two constants must stay identical")
}

func newSynthOptInContext(t *testing.T, optIn bool) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	if optIn {
		c.Request.Header.Set("X-Synth-Session", "sess-1")
	}
	return c
}

func TestCaptureUpstreamRequestBody(t *testing.T) {
	client := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}]}`)
	mutated := []byte(`{"model":"m","messages":[{"role":"user","content":"q"}],"tool_choice":{"type":"auto"}}`)

	t.Run("opt-in + divergent bytes -> captured", func(t *testing.T) {
		c := newSynthOptInContext(t, true)
		c.Set(opsUpstreamRequestBodyContextKey, mutated)
		require.Equal(t, mutated, captureUpstreamRequestBody(c, client))
	})
	t.Run("opt-in + identical bytes -> nil (no-op rewrite helpers return input unchanged)", func(t *testing.T) {
		c := newSynthOptInContext(t, true)
		c.Set(opsUpstreamRequestBodyContextKey, client)
		require.Nil(t, captureUpstreamRequestBody(c, client))
	})
	t.Run("opt-in + key absent -> nil", func(t *testing.T) {
		c := newSynthOptInContext(t, true)
		require.Nil(t, captureUpstreamRequestBody(c, client))
	})
	t.Run("not opt-in -> nil even when divergent", func(t *testing.T) {
		c := newSynthOptInContext(t, false)
		c.Set(opsUpstreamRequestBodyContextKey, mutated)
		require.Nil(t, captureUpstreamRequestBody(c, client))
	})
	t.Run("string-typed stash handled", func(t *testing.T) {
		c := newSynthOptInContext(t, true)
		c.Set(opsUpstreamRequestBodyContextKey, string(mutated))
		require.Equal(t, mutated, captureUpstreamRequestBody(c, client))
	})
}

func decodeBlobPayload(t *testing.T, compressed []byte) map[string]any {
	t.Helper()
	dec, err := zstd.NewReader(nil)
	require.NoError(t, err)
	defer dec.Close()
	raw, err := dec.DecodeAll(compressed, nil)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}

// divergent 上游体落 blob：同套脱敏 + thinking signature 保留（opt-in Anthropic）。
func TestBuildBlob_UpstreamDivergent(t *testing.T) {
	svc := &Service{bodyMaxBytes: 256 * 1024, optInBodyMaxBytes: 1024 * 1024}
	upstream := []byte(`{"model":"m","messages":[{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"t","signature":"UPSTREAM_SIG"}]}]}`)
	input := CaptureInput{
		RequestID:           "req-div-1",
		Platform:            "anthropic",
		DialogSynth:         true,
		InboundEndpoint:     "/v1/messages",
		StatusCode:          200,
		CreatedAt:           time.Now().UTC(),
		RequestBody:         []byte(`{"model":"m","messages":[{"role":"user","content":"q"}]}`),
		UpstreamRequestBody: upstream,
		ResponseBody:        []byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`),
	}

	compressed, _, _, _, err := svc.buildBlob(input)
	require.NoError(t, err)
	payload := decodeBlobPayload(t, compressed)

	req, ok := payload["request"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, req["upstream_divergent"])
	encoded, err := json.Marshal(req["upstream_body"])
	require.NoError(t, err)
	require.Equal(t, "UPSTREAM_SIG",
		gjson.GetBytes(encoded, "messages.0.content.0.signature").String(),
		"upstream_body must go through the same thinking-signature preservation")
}

func TestBuildBlob_NoUpstreamBody_KeysAbsent(t *testing.T) {
	svc := &Service{bodyMaxBytes: 256 * 1024}
	input := CaptureInput{
		RequestID:       "req-div-0",
		InboundEndpoint: "/v1/messages",
		StatusCode:      200,
		CreatedAt:       time.Now().UTC(),
		RequestBody:     []byte(`{"model":"m"}`),
		ResponseBody:    []byte(`{"type":"message"}`),
	}
	compressed, _, _, _, err := svc.buildBlob(input)
	require.NoError(t, err)
	payload := decodeBlobPayload(t, compressed)
	req, ok := payload["request"].(map[string]any)
	require.True(t, ok)
	_, hasBody := req["upstream_body"]
	_, hasFlag := req["upstream_divergent"]
	require.False(t, hasBody)
	require.False(t, hasFlag)
}
