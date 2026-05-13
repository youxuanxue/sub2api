//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newBridgeStickyTestContext(t *testing.T, path string, headers http.Header) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, err := http.NewRequest(http.MethodPost, path, nil)
	require.NoError(t, err)
	if headers != nil {
		req.Header = headers
	}
	c.Request = req
	c.Set("api_key", &APIKey{ID: 11, Group: &Group{ID: 22, StickyRoutingMode: string(group.StickyRoutingModeAuto)}})
	return c
}

func TestBridgeStickyCoverage_OpenAIEmbeddingsAndImagesApplySticky(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Session-Id", "sticky-emb-img")
	c := newBridgeStickyTestContext(t, "/v1/embeddings", headers)
	account := &Account{ID: 1, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{}

	embBody := []byte(`{"model":"text-embedding-3-large","input":"hello"}`)
	embOut := applyStickyToNewAPIBridge(context.Background(), c, svc.settingService, account, embBody, "")
	require.Equal(t, "sticky-emb-img", gjson.GetBytes(embOut, "prompt_cache_key").String())
	require.Equal(t, "sticky-emb-img", c.Request.Header.Get("X-Session-Id"))

	imgBody := []byte(`{"model":"gpt-image-1","prompt":"draw"}`)
	imgOut := applyStickyToNewAPIBridge(context.Background(), c, svc.settingService, account, imgBody, "")
	require.Equal(t, "sticky-emb-img", gjson.GetBytes(imgOut, "prompt_cache_key").String())
	require.Equal(t, "sticky-emb-img", c.Request.Header.Get("X-Session-Id"))
}

func TestBridgeStickyCoverage_GatewayBridgeChatResponsesApplySticky(t *testing.T) {
	headers := http.Header{}
	headers.Set("session_id", "sticky-gw-chat-resp")
	c := newBridgeStickyTestContext(t, "/v1/messages", headers)
	account := &Account{ID: 2, Type: AccountTypeAPIKey}

	chatBody := []byte(`{"model":"glm-4.5","messages":[{"role":"user","content":"hi"}]}`)
	chatOut := applyStickyToNewAPIBridge(context.Background(), c, nil, account, chatBody, "glm-4.5")
	require.Equal(t, "sticky-gw-chat-resp", gjson.GetBytes(chatOut, "prompt_cache_key").String())
	require.Equal(t, "sticky-gw-chat-resp", c.Request.Header.Get("X-Session-Id"))

	respBody := []byte(`{"model":"glm-4.5","input":"hello"}`)
	respOut := applyStickyToNewAPIBridge(context.Background(), c, nil, account, respBody, "glm-4.5")
	require.Equal(t, "sticky-gw-chat-resp", gjson.GetBytes(respOut, "prompt_cache_key").String())
	require.Equal(t, "sticky-gw-chat-resp", c.Request.Header.Get("X-Session-Id"))
}

func TestBridgeStickyCoverage_AnthropicBridgeAppliesStickyBeforeDispatch(t *testing.T) {
	headers := http.Header{}
	headers.Set("X-Session-Id", "sticky-anth-bridge")
	c := newBridgeStickyTestContext(t, "/v1/messages", headers)
	account := &Account{ID: 3, Type: AccountTypeAPIKey}
	svc := &OpenAIGatewayService{}

	chatBody := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	out := applyStickyToNewAPIBridge(context.Background(), c, svc.settingService, account, chatBody, "claude-sonnet-4-5")
	require.Equal(t, "sticky-anth-bridge", gjson.GetBytes(out, "prompt_cache_key").String())
	require.Equal(t, "sticky-anth-bridge", c.Request.Header.Get("X-Session-Id"))
}
