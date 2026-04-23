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

type fakeStickyGlobalProvider struct {
	enabled bool
}

func (f *fakeStickyGlobalProvider) IsStickyRoutingEnabled(_ context.Context) bool {
	return f.enabled
}

func newGinCtxWithAPIKey(t *testing.T, ak *APIKey, headers http.Header) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest("POST", "/v1/responses", nil)
	if headers != nil {
		req.Header = headers
	}
	c.Request = req
	if ak != nil {
		c.Set("api_key", ak)
	}
	return c
}

func TestUS201_ResolveStickyStrategyFromGin_DefaultsAuto(t *testing.T) {
	c := newGinCtxWithAPIKey(t, &APIKey{ID: 1, Group: &Group{ID: 9}}, nil)
	prov := &fakeStickyGlobalProvider{enabled: true}
	st := resolveStickyStrategyFromGin(context.Background(), c, prov)
	require.Equal(t, StickyModeAuto, st.Mode)
	require.True(t, st.GlobalEnabled)
	require.True(t, st.AllowsDerivation())
	require.True(t, st.AllowsInjection())
}

func TestUS201_ResolveStickyStrategyFromGin_GroupOff(t *testing.T) {
	c := newGinCtxWithAPIKey(t, &APIKey{
		ID:    1,
		Group: &Group{ID: 9, StickyRoutingMode: string(group.StickyRoutingModeOff)},
	}, nil)
	st := resolveStickyStrategyFromGin(context.Background(), c, &fakeStickyGlobalProvider{enabled: true})
	require.Equal(t, StickyModeOff, st.Mode)
	require.False(t, st.AllowsInjection())
}

func TestUS201_ResolveStickyStrategyFromGin_GlobalOffForcesPassthrough(t *testing.T) {
	c := newGinCtxWithAPIKey(t, &APIKey{
		ID:    1,
		Group: &Group{ID: 9, StickyRoutingMode: string(group.StickyRoutingModeAuto)},
	}, nil)
	st := resolveStickyStrategyFromGin(context.Background(), c, &fakeStickyGlobalProvider{enabled: false})
	require.Equal(t, StickyModeAuto, st.Mode)
	require.Equal(t, StickyModePassthrough, st.EffectiveMode())
	require.True(t, st.AllowsInjection())
	require.False(t, st.AllowsDerivation())
}

func TestUS201_BuildStickyInjectionRequestFromGin_PicksUpGroupID(t *testing.T) {
	headers := http.Header{}
	headers.Set("session_id", "client-session-xyz")
	c := newGinCtxWithAPIKey(t, &APIKey{
		ID:    42,
		Group: &Group{ID: 7, StickyRoutingMode: string(group.StickyRoutingModeAuto)},
	}, headers)
	req := buildStickyInjectionRequestFromGin(
		context.Background(), c,
		&fakeStickyGlobalProvider{enabled: true},
		"gpt-5.4",
		StickyAccountOpenAIOAuth,
		false,
	)
	require.Equal(t, int64(42), req.APIKeyID)
	require.Equal(t, int64(7), req.GroupID)
	require.Equal(t, "gpt-5.4", req.UpstreamModel)
	require.Equal(t, "client-session-xyz", req.Headers.Get("session_id"))

	key := DeriveStickyKey(req, []byte(`{}`))
	require.Equal(t, "client-session-xyz", key.Value)
	require.Equal(t, StickyKeySourceClientSessionID, key.Source)
}

func TestUS201_ApplyStickyToNewAPIChatBridge_InjectsBodyAndHeader(t *testing.T) {
	headers := http.Header{}
	c := newGinCtxWithAPIKey(t, &APIKey{ID: 99, Group: &Group{ID: 1}}, headers)
	body := []byte(`{"model":"glm-4-plus","messages":[{"role":"user","content":"hi"}],"system":"unique-system-for-glm"}`)
	out := applyStickyToNewAPIChatBridge(
		context.Background(), c,
		&fakeStickyGlobalProvider{enabled: true},
		&Account{ID: 1, Type: AccountTypeAPIKey},
		body,
		"glm-4-plus",
	)

	pkc := gjson.GetBytes(out, "prompt_cache_key").String()
	require.NotEmpty(t, pkc, "prompt_cache_key should be injected into body")
	require.Equal(t, pkc, c.Request.Header.Get("X-Session-Id"), "X-Session-Id should mirror prompt_cache_key")
}

func TestUS201_ApplyStickyToNewAPIResponsesBridge_InjectsBodyAndHeader(t *testing.T) {
	// Bug B-6: the responses-shape injector currently writes to the same
	// prompt_cache_key root key as the chat-completions injector; the
	// observable behaviour must match for now, but the call site is
	// distinct so future protocol drift stays localised.
	headers := http.Header{}
	c := newGinCtxWithAPIKey(t, &APIKey{ID: 99, Group: &Group{ID: 1}}, headers)
	body := []byte(`{"model":"glm-4-plus","input":[{"role":"user","content":"hi"}]}`)
	out := applyStickyToNewAPIResponsesBridge(
		context.Background(), c,
		&fakeStickyGlobalProvider{enabled: true},
		&Account{ID: 1, Type: AccountTypeAPIKey},
		body,
		"glm-4-plus",
	)

	pkc := gjson.GetBytes(out, "prompt_cache_key").String()
	require.NotEmpty(t, pkc, "prompt_cache_key should be injected into body for responses path too")
	require.Equal(t, pkc, c.Request.Header.Get("X-Session-Id"))
}

func TestUS201_ApplyStickyToNewAPIChatBridge_OffStrategyNoOp(t *testing.T) {
	c := newGinCtxWithAPIKey(t, &APIKey{
		ID:    99,
		Group: &Group{ID: 1, StickyRoutingMode: string(group.StickyRoutingModeOff)},
	}, http.Header{})
	body := []byte(`{"model":"glm-4-plus","messages":[]}`)
	out := applyStickyToNewAPIChatBridge(
		context.Background(), c,
		&fakeStickyGlobalProvider{enabled: true},
		&Account{ID: 1, Type: AccountTypeAPIKey},
		body,
		"glm-4-plus",
	)
	require.Equal(t, body, out)
	require.Empty(t, c.Request.Header.Get("X-Session-Id"))
}

func TestUS201_ApplyStickyToNewAPIResponsesBridge_OffStrategyNoOp(t *testing.T) {
	c := newGinCtxWithAPIKey(t, &APIKey{
		ID:    99,
		Group: &Group{ID: 1, StickyRoutingMode: string(group.StickyRoutingModeOff)},
	}, http.Header{})
	body := []byte(`{"model":"glm-4-plus","input":[]}`)
	out := applyStickyToNewAPIResponsesBridge(
		context.Background(), c,
		&fakeStickyGlobalProvider{enabled: true},
		&Account{ID: 1, Type: AccountTypeAPIKey},
		body,
		"glm-4-plus",
	)
	require.Equal(t, body, out)
	require.Empty(t, c.Request.Header.Get("X-Session-Id"))
}

func TestUS201_OpenAIStickyAccountKind(t *testing.T) {
	require.Equal(t, StickyAccountOpenAIOAuth, openAIStickyAccountKind(nil))
	require.Equal(t, StickyAccountOpenAIAPIKey, openAIStickyAccountKind(&Account{Type: AccountTypeAPIKey}))
	require.Equal(t, StickyAccountOpenAIOAuth, openAIStickyAccountKind(&Account{Type: AccountTypeOAuth}))
}

func TestUS201_AnthropicStickyAccountKind(t *testing.T) {
	require.Equal(t, StickyAccountAnthropicOAuth, anthropicStickyAccountKind(nil))
	require.Equal(t, StickyAccountAnthropicAPIKey, anthropicStickyAccountKind(&Account{Type: AccountTypeAPIKey}))
	require.Equal(t, StickyAccountAnthropicOAuth, anthropicStickyAccountKind(&Account{Type: AccountTypeOAuth}))
}
