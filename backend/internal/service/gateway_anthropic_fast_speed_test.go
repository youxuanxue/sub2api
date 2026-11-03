//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func anthropicFastSpeedAccount() *Account {
	return &Account{
		ID:          8488,
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-test"},
		Status:      StatusActive,
	}
}

// fastSpeedSettingRepoStub 在 beta 策略 stub 上补齐转发设置的批量读取。
// buildUpstreamRequest 会读这些开关；缺省空表走现有默认值，不另造计费规则。
type fastSpeedSettingRepoStub struct {
	betaPolicySettingRepoStub
}

func (s *fastSpeedSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		if v, ok := s.values[key]; ok {
			out[key] = v
		}
	}
	return out, nil
}

func anthropicFastSpeedService(t *testing.T, rules []BetaPolicyRule, useDefault bool) *GatewayService {
	t.Helper()
	values := map[string]string{}
	if !useDefault {
		raw, err := json.Marshal(&BetaPolicySettings{Rules: rules})
		require.NoError(t, err)
		values[SettingKeyBetaPolicySettings] = string(raw)
	}
	return &GatewayService{
		cfg: &config.Config{},
		settingService: NewSettingService(&fastSpeedSettingRepoStub{
			betaPolicySettingRepoStub: betaPolicySettingRepoStub{values: values},
		}, &config.Config{}),
	}
}

func anthropicFastSpeedContext(t *testing.T, betaHeader string) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	if betaHeader != "" {
		c.Request.Header.Set("anthropic-beta", betaHeader)
	}
	BindResponseUsageCostBillingWithTier(c, time.Time{}, ChannelUsageFields{}, "fast")
	return c
}

func TestAnthropicFastSpeedPolicy_FilterAndBlockStripOutboundAndBilling(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","speed":"fast","metadata":{"speed":"fast"},"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	account := anthropicFastSpeedAccount()
	header := claude.BetaFastMode + ",oauth-2025-04-20"

	cases := []struct {
		name       string
		useDefault bool
		rules      []BetaPolicyRule
	}{
		{name: "default filter", useDefault: true},
		{
			name: "explicit filter",
			rules: []BetaPolicyRule{{
				BetaToken: claude.BetaFastMode,
				Action:    BetaPolicyActionFilter,
				Scope:     BetaPolicyScopeAll,
			}},
		},
		{
			name: "block",
			rules: []BetaPolicyRule{{
				BetaToken: claude.BetaFastMode,
				Action:    BetaPolicyActionBlock,
				Scope:     BetaPolicyScopeAll,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := anthropicFastSpeedContext(t, header)
			parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Speed: "fast"}
			svc := anthropicFastSpeedService(t, tc.rules, tc.useDefault)

			req, wire, err := svc.buildUpstreamRequest(
				context.Background(), c, account, body,
				"sk-test", "apikey", "claude-opus-5", false, false,
			)
			require.NoError(t, err)

			got := readRequestBodyForTest(t, req)
			require.False(t, gjson.GetBytes(got, "speed").Exists(), "filter/block 后出站 JSON 不能再带顶层 speed=fast")
			require.Equal(t, "fast", gjson.GetBytes(got, "metadata.speed").String(), "只删除顶层 speed")
			require.Equal(t, "claude-opus-5", gjson.GetBytes(got, "model").String())
			require.JSONEq(t, string(wire), string(got))

			if tc.rules == nil || (len(tc.rules) == 1 && tc.rules[0].Action == BetaPolicyActionFilter) || tc.useDefault {
				require.False(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), claude.BetaFastMode),
					"filter 必须同时摘掉 header token")
			}

			// 入站 parsed.Speed 仍是 fast；计费必须看记录下来的出站 body。
			require.Equal(t, "fast", parsed.Speed)
			require.NotNil(t, anthropicSpeedServiceTier(account, parsed.Speed, "claude-opus-5"))
			tier := anthropicSpeedServiceTierFromOutboundBody(account, anthropicBillingOutboundBody(c, parsed), "claude-opus-5")
			require.Nil(t, tier, "删掉出站 speed 之后不能再按 fast 计费")

			snap, ok := responseUsageCostBillingFromContext(c)
			require.True(t, ok)
			require.Empty(t, snap.ServiceTier, "响应侧预估也必须跟着出站 body 离开 fast")
		})
	}
}

func TestAnthropicFastSpeedPolicy_PassKeepsFastAndOtherSpeedsStay(t *testing.T) {
	account := anthropicFastSpeedAccount()
	pass := []BetaPolicyRule{{
		BetaToken: claude.BetaFastMode,
		Action:    BetaPolicyActionPass,
		Scope:     BetaPolicyScopeAll,
	}}
	filter := []BetaPolicyRule{{
		BetaToken: claude.BetaFastMode,
		Action:    BetaPolicyActionFilter,
		Scope:     BetaPolicyScopeAll,
	}}

	t.Run("pass keeps speed and bills fast", func(t *testing.T) {
		body := []byte(`{"model":"claude-opus-5","speed":"fast","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
		c := anthropicFastSpeedContext(t, claude.BetaFastMode)
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Speed: "fast"}
		svc := anthropicFastSpeedService(t, pass, false)

		req, _, err := svc.buildUpstreamRequest(
			context.Background(), c, account, body,
			"sk-test", "apikey", "claude-opus-5", false, false,
		)
		require.NoError(t, err)
		got := readRequestBodyForTest(t, req)
		require.Equal(t, "fast", gjson.GetBytes(got, "speed").String())
		require.True(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), claude.BetaFastMode))

		tier := anthropicSpeedServiceTierFromOutboundBody(account, anthropicBillingOutboundBody(c, parsed), "claude-opus-5")
		require.NotNil(t, tier)
		require.Equal(t, "fast", *tier)
		snap, ok := responseUsageCostBillingFromContext(c)
		require.True(t, ok)
		require.Equal(t, "fast", snap.ServiceTier)
	})

	t.Run("filter does not delete other speed values", func(t *testing.T) {
		for _, speed := range []string{"standard", "slow", "fast-mode", "1"} {
			body := []byte(`{"model":"claude-opus-5","speed":"` + speed + `","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
			c := anthropicFastSpeedContext(t, "")
			svc := anthropicFastSpeedService(t, filter, false)
			req, _, err := svc.buildUpstreamRequest(
				context.Background(), c, account, body,
				"sk-test", "apikey", "claude-opus-5", false, false,
			)
			require.NoError(t, err)
			got := readRequestBodyForTest(t, req)
			require.Equal(t, speed, gjson.GetBytes(got, "speed").String(), "speed=%s 不应被删除", speed)
			tier := anthropicSpeedServiceTierFromOutboundBody(account, got, "claude-opus-5")
			require.Nil(t, tier)
		}
	})
}

func TestStripAnthropicTopLevelFastSpeedOnly(t *testing.T) {
	kept, changed := stripAnthropicTopLevelFastSpeed([]byte(`{"speed":"standard","metadata":{"speed":"fast"}}`))
	require.False(t, changed)
	require.Equal(t, "standard", gjson.GetBytes(kept, "speed").String())

	stripped, changed := stripAnthropicTopLevelFastSpeed([]byte(`{"speed":" FAST ","metadata":{"speed":"fast"}}`))
	require.True(t, changed)
	require.False(t, gjson.GetBytes(stripped, "speed").Exists())
	require.Equal(t, "fast", gjson.GetBytes(stripped, "metadata.speed").String())

	numeric, changed := stripAnthropicTopLevelFastSpeed([]byte(`{"speed":1}`))
	require.False(t, changed)
	require.Equal(t, float64(1), gjson.GetBytes(numeric, "speed").Float())
}

func TestAnthropicFastSpeedPolicy_CountTokensPassthroughAndBedrockShareStrip(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","speed":" FAST ","metadata":{"speed":"fast"},"max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`)
	account := anthropicFastSpeedAccount()
	filter := []BetaPolicyRule{{
		BetaToken: claude.BetaFastMode,
		Action:    BetaPolicyActionFilter,
		Scope:     BetaPolicyScopeAll,
	}}
	svc := anthropicFastSpeedService(t, filter, false)

	t.Run("count tokens strips and clears fast hold", func(t *testing.T) {
		c := anthropicFastSpeedContext(t, claude.BetaFastMode)
		req, wire, err := svc.buildCountTokensRequest(
			context.Background(), c, account, body,
			"sk-test", "apikey", "claude-opus-5", false,
		)
		require.NoError(t, err)
		got := readRequestBodyForTest(t, req)
		require.False(t, gjson.GetBytes(got, "speed").Exists())
		require.Equal(t, "fast", gjson.GetBytes(got, "metadata.speed").String())
		require.JSONEq(t, string(wire), string(got))
		require.False(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), claude.BetaFastMode))
		snap, ok := responseUsageCostBillingFromContext(c)
		require.True(t, ok)
		require.Empty(t, snap.ServiceTier)
	})

	t.Run("passthrough strips outbound speed", func(t *testing.T) {
		c := anthropicFastSpeedContext(t, claude.BetaFastMode)
		req, wire, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "sk-test")
		require.NoError(t, err)
		got := readRequestBodyForTest(t, req)
		require.False(t, gjson.GetBytes(got, "speed").Exists())
		require.Equal(t, "fast", gjson.GetBytes(got, "metadata.speed").String())
		require.JSONEq(t, string(wire), string(got))
		snap, ok := responseUsageCostBillingFromContext(c)
		require.True(t, ok)
		require.Empty(t, snap.ServiceTier)
	})

	t.Run("count tokens passthrough strips", func(t *testing.T) {
		c := anthropicFastSpeedContext(t, "")
		req, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(context.Background(), c, account, body, "sk-test")
		require.NoError(t, err)
		got := readRequestBodyForTest(t, req)
		require.False(t, gjson.GetBytes(got, "speed").Exists())
		require.Equal(t, "fast", gjson.GetBytes(got, "metadata.speed").String())
	})

	t.Run("bedrock strips before credential failure", func(t *testing.T) {
		c := anthropicFastSpeedContext(t, claude.BetaFastMode)
		bedrock := anthropicFastSpeedAccount()
		bedrock.Type = AccountTypeBedrock
		parsed := &ParsedRequest{Body: NewRequestBodyRef(body), Model: "claude-opus-5", Speed: "fast"}
		_, err := svc.forwardBedrock(context.Background(), c, bedrock, parsed, time.Now())
		require.Error(t, err)
		outbound := anthropicBillingOutboundBody(c, parsed)
		require.False(t, gjson.GetBytes(outbound, "speed").Exists(), "Bedrock 最终出站 body 不能再带顶层 speed=fast")
		snap, ok := responseUsageCostBillingFromContext(c)
		require.True(t, ok)
		require.Empty(t, snap.ServiceTier)
		tier := anthropicSpeedServiceTierFromOutboundBody(bedrock, outbound, "claude-opus-5")
		require.Nil(t, tier)
	})
}
