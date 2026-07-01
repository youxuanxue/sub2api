package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTkNormalizeCCGeoStegoText(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{
			name:    "shanghai slash date ascii apostrophe",
			in:      "# currentDate\nToday's date is 2026/06/30.\n\nIMPORTANT",
			want:    "# currentDate\nToday's date is 2026-06-30.\n\nIMPORTANT",
			changed: true,
		},
		{
			name:    "known mirror unicode apostrophe",
			in:      "Today\u2019s date is 2026/06/30.",
			want:    "Today's date is 2026-06-30.",
			changed: true,
		},
		{
			name:    "lab keyword apostrophe",
			in:      "Today\u02BCs date is 2026/06/30.",
			want:    "Today's date is 2026-06-30.",
			changed: true,
		},
		{
			name:    "both signals apostrophe",
			in:      "Today\u02B9s date is 2026/06/30.",
			want:    "Today's date is 2026-06-30.",
			changed: true,
		},
		{
			name:    "date change meta line",
			in:      "The date has changed. Today\u2019s date is now 2026/06/30.",
			want:    "The date has changed. Today's date is now 2026-06-30.",
			changed: true,
		},
		{
			name:    "already canonical",
			in:      "Today's date is 2026-06-30.",
			want:    "Today's date is 2026-06-30.",
			changed: false,
		},
		{
			name:    "unrelated text",
			in:      "hello world",
			want:    "hello world",
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := tkNormalizeCCGeoStegoText(tc.in)
			require.Equal(t, tc.changed, changed)
			require.Equal(t, tc.want, out)
		})
	}
}

func TestTkNormalizeAnthropicCCGeoStegoMessagesSystemReminder(t *testing.T) {
	in := []byte(`{
		"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK."}],
		"messages":[{"role":"user","content":[
			{"type":"text","text":"<system-reminder>\n# currentDate\nToday\u2019s date is 2026/06/30.\n</system-reminder>"}
		]}]
	}`)
	out, changed := tkNormalizeAnthropicCCGeoStego(in)
	require.True(t, changed)
	got := gjson.GetBytes(out, "messages.0.content.0.text").String()
	require.Contains(t, got, "Today's date is 2026-06-30.")
	require.NotContains(t, got, "\u2019")
	require.NotContains(t, got, "/06/")
}

func TestTkNormalizeAnthropicCCGeoStegoDateChangeAttachment(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":[
		{"type":"text","text":"hi","attachment":{"type":"date_change","newDate":"2026/06/30"}}
	]}]}`)
	out, changed := tkNormalizeAnthropicCCGeoStego(in)
	require.True(t, changed)
	require.Equal(t, "2026-06-30", gjson.GetBytes(out, "messages.0.content.0.attachment.newDate").String())
}

func TestTkNormalizeAnthropicCCGeoStegoNoOpWhenClean(t *testing.T) {
	in := []byte(`{"messages":[{"role":"user","content":"Today's date is 2026-06-30."}]}`)
	out, changed := tkNormalizeAnthropicCCGeoStego(in)
	require.False(t, changed)
	require.Equal(t, string(in), string(out))
}

func TestTkNormalizeAnthropicRequestBodyAppliesCCGeoStego(t *testing.T) {
	svc := newNormalizeTestService(t, "true")
	in := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"Today\u2019s date is 2026/06/30."}]}]}`)
	out := svc.tkNormalizeAnthropicRequestBody(context.Background(), nil, in, nil)
	require.Contains(t, string(out), "Today's date is 2026-06-30.")
}

func TestForwardCountTokensAnthropicAPIKeyPassthrough_NormalizesCCGeoStego(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":[{"type":"text","text":"Today\u2019s date is 2026/06/30."}]}]}`)
	parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-sonnet-4-6"}

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"input_tokens":10}`)),
		},
	}

	svc := newNormalizeTestService(t, "true")
	svc.cfg = &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}
	svc.httpUpstream = upstream

	err := svc.ForwardCountTokens(context.Background(), c, newAnthropicAPIKeyAccountForTest(), parsed)
	require.NoError(t, err)
	require.Contains(t, string(upstream.lastBody), "Today's date is 2026-06-30.")
	require.NotContains(t, string(upstream.lastBody), "\u2019")
	require.NotContains(t, string(upstream.lastBody), "/06/")
}

// TestTkProbeCCGeoGatewayCoverageJSONL delegates to the unified prompt-surface
// gateway coverage test (ops/anthropic/probe_prompt_surfaces.py).
func TestTkProbeCCGeoGatewayCoverageJSONL(t *testing.T) {
	TestTkProbePromptSurfaceGatewayCoverageJSONL(t)
}
