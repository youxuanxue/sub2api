package service

import (
	"bytes"
	"context"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	cursorbridge "github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCursorToolContinuationCannotFailoverToAnotherSupply(t *testing.T) {
	for _, body := range []string{
		`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bf_local_test","content":"ok"}]}]}`,
		`{"messages":[{"role":"tool","tool_call_id":"toolu_bf_local_test","content":"ok"}]}`,
		`{"input":[{"type":"function_call_output","call_id":"toolu_bf_local_test","output":"ok"}]}`,
	} {
		request, err := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocolrouter.ProtocolChatCompletions, RequestedModel: "composer-2.5", Profile: protocolrouter.RequestProfile{ContentKinds: protocolrouter.ContentText}, Body: []byte(body)})
		require.NoError(t, err)
		ctx := WithProtocolRouting(context.Background(), protocolRoutingTestRouter(), request)
		ordinary := cursorTestAccount()
		ordinary.Extra = nil
		_, governed, err := protocolPlanForAccount(ctx, ordinary, "composer-2.5")
		require.True(t, governed)
		require.ErrorContains(t, err, "original supply")
	}
	require.False(t, cursorContinuationInBody([]byte(`{"messages":[{"role":"tool","tool_call_id":"call_other","content":"toolu_bf_local_text"}]}`)))
}

func cursorTestAccount() *Account {
	return &Account{ID: 42, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: 14,
		Extra: map[string]any{CursorSourceExtraKey: "cursor"}, Credentials: map[string]any{
			"base_url": "http://127.0.0.1:3927", "api_key": "cursor-user-key",
			"model_mapping":          map[string]any{"composer-2.5": "composer-2.5", "gpt-5.5": "gpt-5.5"},
			CursorModelParametersKey: map[string]any{"composer-2.5": []cursorbridge.Parameter{{ID: "fast", Value: "false"}}, "gpt-5.5": nil},
		}}
}
func cursorTestContext() *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	c.Set("api_key", &APIKey{ID: 8, UserID: 9})
	return c
}
func TestCursorTransportUsesTrustedIdentityAndCatalog(t *testing.T) {
	t.Setenv("CURSOR_BRIDGE_URL", "http://127.0.0.1:3927")
	t.Setenv("CURSOR_BRIDGE_SECRET", strings.Repeat("s", 32))
	c := cursorTestContext()
	c.Request.Header.Set(cursorbridge.TenantHeader, "attacker")
	req, err := http.NewRequest("POST", "http://127.0.0.1:3927/v1/messages", bytes.NewBufferString(`{"model":"composer-2.5","cursor_model":{"id":"other","params":[]}}`))
	require.NoError(t, err)
	require.NoError(t, prepareCursorUpstreamRequest(req, c, cursorTestAccount()))
	require.Equal(t, "user:9:key:8:account:42", req.Header.Get(cursorbridge.TenantHeader))
	body, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	require.Equal(t, "composer-2.5", gjson.GetBytes(body, "cursor_model.id").String())
	require.Equal(t, "false", gjson.GetBytes(body, "cursor_model.params.0.value").String())
	require.Equal(t, int64(len(body)), req.ContentLength)
	for _, pair := range [][2]string{{"http://attacker/v1/messages", "composer-2.5"}, {"http://127.0.0.1:3927/v1/messages", "unknown"}} {
		req, _ := http.NewRequest("POST", pair[0], strings.NewReader(`{"model":"`+pair[1]+`"}`))
		require.Error(t, prepareCursorUpstreamRequest(req, c, cursorTestAccount()))
		require.Empty(t, req.Header.Get(cursorbridge.SecretHeader))
	}
	c.Set("api_key", nil)
	require.Error(t, prepareCursorUpstreamRequest(req, c, cursorTestAccount()))
}
func TestCursorOwnerRelaySignatureBindsCredentialAndExpiry(t *testing.T) {
	secret := strings.Repeat("r", 32)
	t.Setenv("CURSOR_RELAY_SECRET", secret)
	c := cursorTestContext()
	c.Request.Header.Set("Authorization", "bearer edge-key")
	owner := "user:19:key:18"
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	c.Request.Header.Set(cursorRelayOwnerHeader, owner)
	c.Request.Header.Set(cursorRelayTimeHeader, stamp)
	c.Request.Header.Set(cursorRelaySignatureHeader, cursorRelaySignature(secret, owner, stamp, "edge-key"))
	actual, err := cursorTrustedOwner(c, cursorTestAccount())
	require.NoError(t, err)
	require.Equal(t, "relay:8:"+owner, actual)
	c.Request.Header.Set("Authorization", "Bearer wrong")
	_, err = cursorTrustedOwner(c, cursorTestAccount())
	require.Error(t, err)
	c.Request.Header.Set("Authorization", "Bearer edge-key")
	stamp = strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	c.Request.Header.Set(cursorRelayTimeHeader, stamp)
	c.Request.Header.Set(cursorRelaySignatureHeader, cursorRelaySignature(secret, owner, stamp, "edge-key"))
	_, err = cursorTrustedOwner(c, cursorTestAccount())
	require.Error(t, err)
}
func TestCursorDoesNotEnableNonClaudeModelsForOrdinaryAnthropic(t *testing.T) {
	account := cursorTestAccount()
	require.True(t, cursorMappedModelAllowed(account, "composer-2.5", "composer-2.5"))
	require.False(t, cursorMappedModelAllowed(account, "unknown", "composer-2.5"))
	delete(account.Extra, CursorSourceExtraKey)
	require.False(t, cursorMappedModelAllowed(account, "composer-2.5", "composer-2.5"))
}
