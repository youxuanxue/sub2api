package service

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// --- Pure rewrite functions ---------------------------------------------------

func TestTkNormalizeAnthropicToolChoiceString(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantBody  string
		wantPatch bool
	}{
		{"auto string to object", `{"tool_choice":"auto"}`, `{"tool_choice":{"type":"auto"}}`, true},
		{"required maps to any", `{"tool_choice":"required"}`, `{"tool_choice":{"type":"any"}}`, true},
		{"none string to object", `{"tool_choice":"none"}`, `{"tool_choice":{"type":"none"}}`, true},
		{"unknown string passes through", `{"tool_choice":"foo"}`, `{"tool_choice":"foo"}`, false},
		{"already object", `{"tool_choice":{"type":"any"}}`, `{"tool_choice":{"type":"any"}}`, false},
		{"missing field", `{}`, `{}`, false},
		{"null field", `{"tool_choice":null}`, `{"tool_choice":null}`, false},
		{"empty string is unknown", `{"tool_choice":""}`, `{"tool_choice":""}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, patched := tkNormalizeAnthropicToolChoiceString([]byte(tc.in))
			require.Equal(t, tc.wantPatch, patched, "patched flag")
			require.JSONEq(t, tc.wantBody, string(out))
		})
	}
}

func TestTkNormalizeAnthropicThinkingForcesToolUse(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantBody  string
		wantPatch bool
	}{
		{
			"thinking + any -> strip thinking",
			`{"thinking":{"type":"enabled","budget_tokens":10000},"tool_choice":{"type":"any"}}`,
			`{"tool_choice":{"type":"any"}}`,
			true,
		},
		{
			"thinking + tool -> strip thinking, keep name",
			`{"thinking":{"type":"enabled"},"tool_choice":{"type":"tool","name":"foo"}}`,
			`{"tool_choice":{"type":"tool","name":"foo"}}`,
			true,
		},
		{
			"thinking + auto -> keep both",
			`{"thinking":{"type":"enabled"},"tool_choice":{"type":"auto"}}`,
			`{"thinking":{"type":"enabled"},"tool_choice":{"type":"auto"}}`,
			false,
		},
		{
			"thinking + none -> keep both",
			`{"thinking":{"type":"enabled"},"tool_choice":{"type":"none"}}`,
			`{"thinking":{"type":"enabled"},"tool_choice":{"type":"none"}}`,
			false,
		},
		{
			"thinking only -> keep",
			`{"thinking":{"type":"enabled"}}`,
			`{"thinking":{"type":"enabled"}}`,
			false,
		},
		{
			"thinking disabled + any -> keep (disabled is not enabled)",
			`{"thinking":{"type":"disabled"},"tool_choice":{"type":"any"}}`,
			`{"thinking":{"type":"disabled"},"tool_choice":{"type":"any"}}`,
			false,
		},
		{
			"tool_choice still string -> no match (caller runs string-normalize first)",
			`{"thinking":{"type":"enabled"},"tool_choice":"required"}`,
			`{"thinking":{"type":"enabled"},"tool_choice":"required"}`,
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, patched := tkNormalizeAnthropicThinkingForcesToolUse([]byte(tc.in))
			require.Equal(t, tc.wantPatch, patched, "patched flag")
			require.JSONEq(t, tc.wantBody, string(out))
		})
	}
}

// --- GatewayService integration with settingService + ops events -------------

func newNormalizeTestService(t *testing.T, settingValue string) *GatewayService {
	t.Helper()
	repo := &gatewayTTLSettingRepo{data: map[string]string{}}
	if settingValue != "" {
		repo.data[SettingKeyAnthropicRequestNormalizeEnabled] = settingValue
	}
	// Reset the shared gatewayForwardingCache between tests so each test sees
	// its own settingValue without leaking 60s TTL state across cases.
	gatewayForwardingCache.Store(&cachedGatewayForwardingSettings{})
	return &GatewayService{settingService: NewSettingService(repo, &config.Config{})}
}

func newGinTestContext() *gin.Context {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
	return c
}

func TestTkNormalizeAnthropicRequestBody_CombinedRewrite(t *testing.T) {
	// Client sends OpenAI-style string AND thinking; both rules fire in series.
	in := []byte(`{"tool_choice":"required","thinking":{"type":"enabled","budget_tokens":10000},"tools":[]}`)

	svc := newNormalizeTestService(t, "true")
	c := newGinTestContext()

	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), c, in)

	// Step 1 turned "required" into {"type":"any"}; step 2 then stripped thinking.
	require.JSONEq(t, `{"tool_choice":{"type":"any"},"tools":[]}`, string(out))

	// Both changes recorded as a single ops upstream-errors event.
	ev := normalizeTestEventsFor(c)
	require.Len(t, ev, 1)
	require.Equal(t, "request_normalized", ev[0].Kind)
	require.Equal(t, string(PlatformAnthropic), ev[0].Platform)
	require.Equal(t,
		string(tkNormalizeChangeToolChoiceStringToObject)+","+string(tkNormalizeChangeThinkingForcesToolUse),
		ev[0].Message,
	)
}

func TestTkNormalizeAnthropicRequestBody_NoChangeWhenAlreadyValid(t *testing.T) {
	in := []byte(`{"tool_choice":{"type":"auto"},"thinking":{"type":"enabled"}}`)

	svc := newNormalizeTestService(t, "true")
	c := newGinTestContext()

	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), c, in)

	require.JSONEq(t, string(in), string(out))
	require.Empty(t, normalizeTestEventsFor(c), "no normalize event when nothing changed")
}

func TestTkNormalizeAnthropicRequestBody_SettingDisabledIsNoop(t *testing.T) {
	// Bad body the normalizer would otherwise fix.
	in := []byte(`{"tool_choice":"required","thinking":{"type":"enabled"}}`)

	svc := newNormalizeTestService(t, "false")
	c := newGinTestContext()

	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), c, in)

	require.JSONEq(t, string(in), string(out), "setting=false must leave body untouched")
	require.Empty(t, normalizeTestEventsFor(c), "no event when normalize disabled")
}

func TestTkNormalizeAnthropicRequestBody_DefaultEnabledWhenSettingMissing(t *testing.T) {
	// Missing setting key falls back to enabled-by-default.
	in := []byte(`{"tool_choice":"auto"}`)

	svc := newNormalizeTestService(t, "")
	c := newGinTestContext()

	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), c, in)

	require.Equal(t, "auto", gjson.GetBytes(out, "tool_choice.type").String())
	ev := normalizeTestEventsFor(c)
	require.Len(t, ev, 1)
}

func TestTkNormalizeAnthropicRequestBody_EmptyBodyPassesThrough(t *testing.T) {
	svc := newNormalizeTestService(t, "true")
	c := newGinTestContext()

	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), c, nil)
	require.Nil(t, out)
	require.Empty(t, normalizeTestEventsFor(c))
}

func TestTkNormalizeAnthropicRequestBody_NilContextStillRewrites(t *testing.T) {
	// Even without a gin.Context, the body must still be normalized — only the
	// ops event recording is skipped.
	in := []byte(`{"tool_choice":"required"}`)

	svc := newNormalizeTestService(t, "true")
	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), nil, in)

	require.JSONEq(t, `{"tool_choice":{"type":"any"}}`, string(out))
}

// normalizeTestEventsFor returns the OpsUpstreamErrorEvent list recorded on the
// gin.Context by appendOpsUpstreamError.
func normalizeTestEventsFor(c *gin.Context) []*OpsUpstreamErrorEvent {
	if c == nil {
		return nil
	}
	v, ok := c.Get(OpsUpstreamErrorsKey)
	if !ok {
		return nil
	}
	events, ok := v.([]*OpsUpstreamErrorEvent)
	if !ok {
		return nil
	}
	return events
}
