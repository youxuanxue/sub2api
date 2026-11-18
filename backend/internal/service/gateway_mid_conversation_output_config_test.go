//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ============================================================================
// message-level output_config ↔ mid-conversation-output-config / per-turn-control beta
// ============================================================================
//
// 背景：pi-ai（Harness 使用的 Anthropic provider）会为 opus5 生成形如
//
//	{"role":"system","content":[],"output_config":{"effort":"high"}}
//
// 的空控制消息，并在 anthropic-beta 中请求
// mid-conversation-output-config-2026-07-01。OAuth mimic 会用
// FullClaudeCodeMimicryBetas 覆盖客户端 beta；若该 beta 不在固定列表内，
// body 消息级 output_config 与最终 header 不再对称 → 上游 400：
//
//	"output_config: Extra inputs are not permitted"
//
// 修复策略（剥字段，不注入 beta）：
//   - 无正文的 role=system：sanitizeAnthropicEgressBody → stripEmptyAnthropicSystemMessages
//     （content 合法性，与 beta 无关）。
//   - header 缺 mid-conversation token：sanitizeAnthropicBodyForBetaTokens 仅为携带
//     message-level output_config 的消息剥此字段；system 有正文保留正文与其余字段；
//     user/assistant 只剥字段，绝不整条删除。
//   - header 含该 token：保留有正文消息上的 message-level output_config。
//   - 顶层 output_config / effort 不受该 beta 约束，本增量不触碰。
//
// 出站扇入：sanitizeAnthropicEgressBody = 空 system 清理 + beta sanitize。
// 本文件只覆盖 Anthropic 直连路径的该增量；header 侧固定列表（mimic betas）
// 的改动由并行增量负责。

// ============================================================================
// sanitizeAnthropicEgressBody / sanitizeAnthropicBodyForBetaTokens
// ============================================================================

// ★ 主场景：缺 beta 时，多条空控制 system 消息整条删除，且用户顺序 / 顶层 effort
// 不受影响；有正文 system 与 assistant/user 只剥字段。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_MultiMessage(t *testing.T) {
	body := []byte(`{
		"model":"claude-opus-5",
		"output_config":{"effort":"high"},
		"messages":[
			{"role":"system","content":[],"output_config":{"effort":"high"}},
			{"role":"user","content":"hello"},
			{"role":"system","content":"You are helpful.","output_config":{"effort":"high"},"cache_control":{"type":"ephemeral"}},
			{"role":"assistant","content":[{"type":"text","text":"hi"}],"output_config":{"effort":"high"}},
			{"role":"system","content":[{"type":"text","text":""}],"output_config":{"effort":"high"}},
			{"role":"system","content":null,"output_config":{"effort":"high"}},
			{"role":"system","content":"","output_config":{"effort":"high"}},
			{"role":"system","output_config":{"effort":"high"}}
		]
	}`)

	// 模拟 OAuth mimic / 默认 API-key beta：无 mid-conversation-output-config beta
	out, changed := sanitizeAnthropicBodyForBetaTokens(body, "claude-code-20250219,oauth-2025-04-20")
	require.True(t, changed, "存在 message-level output_config 且缺 beta → 必须发生净化")

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 3, "空控制 system 整条删除；仅保留 user + 有正文 system + assistant")

	require.Equal(t, "user", gjson.GetBytes(out, "messages.0.role").String())
	require.Equal(t, "hello", gjson.GetBytes(out, "messages.0.content").String(), "用户消息顺序不得改变")

	require.Equal(t, "system", gjson.GetBytes(out, "messages.1.role").String())
	require.Equal(t, "You are helpful.", gjson.GetBytes(out, "messages.1.content").String())
	require.False(t, gjson.GetBytes(out, "messages.1.output_config").Exists(),
		"有正文 system 只剥 output_config，不整条删除")
	require.True(t, gjson.GetBytes(out, "messages.1.cache_control").Exists(),
		"system 其余字段必须保留")

	require.Equal(t, "assistant", gjson.GetBytes(out, "messages.2.role").String())
	require.Equal(t, "hi", gjson.GetBytes(out, "messages.2.content.0.text").String())
	require.False(t, gjson.GetBytes(out, "messages.2.output_config").Exists(),
		"user/assistant 只剥字段，绝不整条删除")

	// 顶层 output_config / effort 不属于该 beta 保护范围，必须原样保留
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
	require.Equal(t, "claude-opus-5", gjson.GetBytes(out, "model").String())
}

// 表驱动：system 消息 content「无正文」的各种形态都必须整条删除。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_EmptySystemVariants(t *testing.T) {
	cases := []struct {
		name string
		msg  string
	}{
		{"missing_content", `{"role":"system","output_config":{"effort":"high"}}`},
		{"null_content", `{"role":"system","content":null,"output_config":{"effort":"high"}}`},
		{"empty_string_content", `{"role":"system","content":"","output_config":{"effort":"high"}}`},
		{"empty_array_content", `{"role":"system","content":[],"output_config":{"effort":"high"}}`},
		{"only_empty_text_block", `{"role":"system","content":[{"type":"text","text":""}],"output_config":{"effort":"high"}}`},
		{"only_empty_text_blocks", `{"role":"system","content":[{"type":"text","text":""},{"type":"text","text":""}],"output_config":{"effort":"high"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[` + tc.msg + `,{"role":"user","content":"hi"}],"output_config":{"effort":"high"}}`)
			out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
			require.True(t, changed)

			msgs := gjson.GetBytes(out, "messages").Array()
			require.Len(t, msgs, 1, "无正文的 system 控制消息必须整条删除")
			require.Equal(t, "user", msgs[0].Get("role").String())
			require.Equal(t, "hi", msgs[0].Get("content").String())
			require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String(),
				"顶层 effort 不得被连带修改")
		})
	}
}

// 表驱动：system 有正文（含未来非 text 内容块 / 未知块）时不得整条删除，
// 只剥 message-level output_config，正文原样保留。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_SystemWithBodyKept(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"non_empty_string", `"You are helpful."`},
		{"text_block", `[{"type":"text","text":"hi"}]`},
		{"text_with_empty_sibling", `[{"type":"text","text":""},{"type":"text","text":"hi"}]`},
		{"future_non_text_block", `[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA=="}}]`},
		{"unknown_block", `[{"foo":"bar"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"messages":[{"role":"system","content":` + tc.content + `,"output_config":{"effort":"high"}}]}`)
			out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
			require.True(t, changed)

			msgs := gjson.GetBytes(out, "messages").Array()
			require.Len(t, msgs, 1, "有正文 system 不得整条删除")
			require.Equal(t, "system", msgs[0].Get("role").String())
			require.False(t, msgs[0].Get("output_config").Exists(),
				"有正文 system 必须剥掉 message-level output_config")
			require.Equal(t, tc.content, msgs[0].Get("content").Raw,
				"正文必须原样保留；非 text / 未知内容块不得被误当空而删除")
		})
	}
}

// header 含该 beta 时仍必须删除空 system；有正文 system 的 message-level
// output_config 则保留（prod user16：带 beta 也不能豁免空 content）。
// 走 egress 扇入（content 合法性 + beta sanitize），不直接调 beta-only 函数。
func TestSanitizeAnthropicEgressBody_DropsEmptySystemWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
		`{"role":"system","content":"You are helpful.","output_config":{"effort":"high"}},` +
		`{"role":"user","content":"hi"}]}`)
	beta := "claude-code-20250219,oauth-2025-04-20," + claude.BetaMidConversationOutputConfig

	// 分区：beta-only sanitize 在含 beta 时不碰空 system（职责边界）。
	betaOnly, betaChanged := sanitizeAnthropicBodyForBetaTokens(body, beta)
	require.False(t, betaChanged, "beta sanitize 不得承担 content 合法性清理")
	require.True(t, bytes.Equal(body, betaOnly))

	out, changed := sanitizeAnthropicEgressBody(body, beta)
	require.True(t, changed, "空 system 必须删除，即使 header 含 mid-conversation beta")

	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 2)
	require.Equal(t, "system", msgs[0].Get("role").String())
	require.Equal(t, "You are helpful.", msgs[0].Get("content").String())
	require.True(t, msgs[0].Get("output_config").Exists(),
		"有正文 system 在含 beta 时必须保留 message-level output_config")
	require.Equal(t, "user", msgs[1].Get("role").String())
	require.Equal(t, "hi", msgs[1].Get("content").String())
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())
}

// 无正文 system（无 message-level output_config）也必须经 egress 删除；仅 user 仍 no-op。
func TestSanitizeAnthropicEgressBody_StripEmptySystemWithoutOutputConfig(t *testing.T) {
	withEmptySystem := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[]},{"role":"user","content":"hi"}]}`)

	// 分区：无 message-level output_config 时 beta sanitize 字节 no-op。
	betaOnly, betaChanged := sanitizeAnthropicBodyForBetaTokens(withEmptySystem, "oauth-2025-04-20")
	require.False(t, betaChanged)
	require.True(t, bytes.Equal(withEmptySystem, betaOnly))

	out, changed := sanitizeAnthropicEgressBody(withEmptySystem, "oauth-2025-04-20")
	require.True(t, changed)
	msgs := gjson.GetBytes(out, "messages").Array()
	require.Len(t, msgs, 1)
	require.Equal(t, "user", msgs[0].Get("role").String())
	require.Equal(t, "high", gjson.GetBytes(out, "output_config.effort").String())

	userOnly := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	out, changed = sanitizeAnthropicEgressBody(userOnly, "oauth-2025-04-20")
	require.False(t, changed, "无可净化字段/空 system → 字节 no-op")
	require.True(t, bytes.Equal(userOnly, out))
}

// 幂等：净化后的 body 再跑一次必须是 no-op。
func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_Idempotent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
		`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
		`{"role":"system","content":"sys","output_config":{"effort":"high"}},` +
		`{"role":"user","content":"hi"}]}`)

	first, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
	require.True(t, changed)

	second, changedAgain := sanitizeAnthropicBodyForBetaTokens(first, "oauth-2025-04-20")
	require.False(t, changedAgain, "二次净化必须为 no-op（幂等）")
	require.True(t, bytes.Equal(first, second))
}

// ============================================================================
// 真实 request 联动
// ============================================================================

// OAuth mimic 路径真实 request 联动：空控制 system 一律删除；两 case 都只剩 user。
// 期望为显式常量，不引用 FullClaudeCodeMimicryBetas（避免把实现列表当 expected）。
func TestBuildUpstreamRequestOAuthMimic_MidConversationOutputConfig(t *testing.T) {
	cases := []struct {
		name              string
		policyFilterDrops bool
		wantHeaderHasBeta bool
		wantMsgLen        int
		wantFieldOnFirst  bool
	}{
		// TK fingerprint SSOT (FullClaudeCodeMimicryBetas / CC capture) does not
		// yet include mid-conversation-output-config. OAuth mimic therefore
		// strips messages[].output_config until the next cc fingerprint refresh
		// admits the beta. Upstream's default_mimic_keeps_beta expectation is
		// deferred to that follow-up.
		{"default_mimic_strips_without_fingerprint_beta", false, false, 1, false},
		{"policy_filter_drops_beta", true, false, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			if tc.policyFilterDrops {
				c.Set(betaPolicyFilterSetKey, map[string]struct{}{claude.BetaMidConversationOutputConfig: {}})
			}

			account := &Account{ID: 701, Platform: PlatformAnthropic, Type: AccountTypeOAuth,
				Credentials: map[string]any{"access_token": "oauth-tok"},
				Status:      StatusActive,
				Schedulable: true,
			}
			body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
				`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
				`{"role":"user","content":"hello"}]}`)

			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequest(
				context.Background(), c, account, body,
				"oauth-tok", "oauth", "claude-opus-5", false, true, // mimicClaudeCode=true
			)
			require.NoError(t, err)

			outBody := readUpstreamBodyForTest(t, req)
			outBeta := getHeaderRaw(req.Header, "anthropic-beta")
			require.Equalf(t, tc.wantHeaderHasBeta,
				anthropicBetaTokensContains(outBeta, claude.BetaMidConversationOutputConfig),
				"outgoing anthropic-beta 必须与 filter 结果一致（outgoing beta=%q）", outBeta)

			msgs := gjson.GetBytes(outBody, "messages").Array()
			require.Len(t, msgs, tc.wantMsgLen, "空控制 system 消息必须整条删除")
			require.Equal(t, tc.wantFieldOnFirst, msgs[0].Get("output_config").Exists())

			var userContent string
			for _, m := range msgs {
				if m.Get("role").String() == "user" {
					userContent = m.Get("content").String()
				}
			}
			require.Equal(t, "hello", userContent, "用户消息文本必须始终保留")

			require.Equal(t, "high", gjson.GetBytes(outBody, "output_config.effort").String(),
				"顶层 effort 不受该 beta 约束")
		})
	}
}

// API-key passthrough：空 system 一律删除；有正文 system 上的 message-level
// output_config 与客户端 mid-conversation beta 同进同退。
func TestBuildUpstreamRequestAnthropicAPIKeyPassthrough_MidConversationOutputConfigConsistentWithClientHeader(t *testing.T) {
	cases := []struct {
		name       string
		clientBeta string
		wantField  bool
		wantMsgLen int
	}{
		{
			name:       "client_header_has_beta",
			clientBeta: "oauth-2025-04-20," + claude.BetaMidConversationOutputConfig,
			wantField:  true,
			wantMsgLen: 2,
		},
		{
			name:       "client_header_missing_beta",
			clientBeta: "oauth-2025-04-20",
			wantField:  false,
			wantMsgLen: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", tc.clientBeta)

			body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[` +
				`{"role":"system","content":[],"output_config":{"effort":"high"}},` +
				`{"role":"system","content":"You are helpful.","output_config":{"effort":"high"}},` +
				`{"role":"user","content":"hello"}]}`)

			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequestAnthropicAPIKeyPassthrough(
				context.Background(), c, newAnthropicAPIKeyPassthroughAccountForBetaTest(), body, "token",
			)
			require.NoError(t, err)

			outBody := readUpstreamBodyForTest(t, req)
			outBeta := getHeaderRaw(req.Header, "anthropic-beta")

			require.Equalf(t, tc.wantField, anthropicBetaTokensContains(outBeta, claude.BetaMidConversationOutputConfig),
				"出站 header 必须与客户端传入的 beta 一致（outgoing beta=%q）", outBeta)
			msgs := gjson.GetBytes(outBody, "messages").Array()
			require.Len(t, msgs, tc.wantMsgLen, "空控制 system 必须删除；有正文 system + user 保留")
			require.Equal(t, "system", msgs[0].Get("role").String())
			require.Equal(t, "You are helpful.", msgs[0].Get("content").String())
			require.Equal(t, tc.wantField, msgs[0].Get("output_config").Exists(),
				"有正文 system 的 message-level output_config 与 header beta 同进同退")
			require.Equal(t, "user", msgs[1].Get("role").String())
			require.Equal(t, "hello", msgs[1].Get("content").String())
		})
	}
}

func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_ByteNoopWhenBetaPresent(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[],"output_config":{"effort":"high"}},{"role":"user","content":"hi"}]}`)
	for _, beta := range []string{
		claude.BetaMidConversationOutputConfig,
		"per-turn-control-2026-07-01",
		"per-turn-control-2026-07-01," + claude.BetaMidConversationOutputConfig,
	} {
		t.Run(beta, func(t *testing.T) {
			out, changed := sanitizeAnthropicBodyForBetaTokens(body, "claude-code-20250219, "+beta+" ")
			require.False(t, changed, "a supported beta must preserve message-level output_config")
			require.True(t, bytes.Equal(body, out), "含 beta 时必须字节 no-op")
		})
	}
}

func TestForward_PerTurnControlMessageOutputConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const perTurnBeta = "per-turn-control-2026-07-01"
	const body = `{"model":"claude-opus-5","max_tokens":256,"output_config":{"effort":"high"},` +
		`"metadata":{"user_id":"user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account_550e8400-e29b-41d4-a716-446655440000_session_123e4567-e89b-12d3-a456-426614174000"},` +
		`"messages":[{"role":"user","content":"Hello"},{"role":"assistant","content":"Hi"},` +
		`{"role":"system","content":[{"type":"text","text":"Turn policy"}],"output_config":{"effort":"low"}},` +
		`{"role":"user","content":"Summarize that in one sentence."}]}`
	for _, mode := range []string{"oauth_claude_code", "oauth_mimic", "apikey", "apikey_passthrough"} {
		for _, withBeta := range []bool{true, false} {
			name := "without_beta"
			if withBeta {
				name = "with_beta"
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
				c.Request.Header.Set("User-Agent", "claude-cli/2.1.321")
				if mode == "oauth_mimic" {
					c.Request.Header.Set("User-Agent", "third-party-client/1.0")
				}
				if withBeta {
					c.Request.Header.Set("Anthropic-Beta", "claude-code-20250219,"+perTurnBeta)
				}
				account := newAnthropicAPIKeyAccountForTest()
				account.Extra["anthropic_passthrough"] = mode == "apikey_passthrough"
				if strings.HasPrefix(mode, "oauth") {
					account = newAnthropicOAuthAccountForPartialUsageTest()
				}
				upstream := &anthropicHTTPUpstreamRecorder{resp: &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body: io.NopCloser(strings.NewReader(`{"id":"msg_test","type":"message","role":"assistant",` +
						`"model":"claude-opus-5","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn",` +
						`"usage":{"input_tokens":10,"output_tokens":1}}`)),
				}}
				parsed, err := ParseGatewayRequest(NewRequestBodyRef([]byte(body)), PlatformAnthropic)
				require.NoError(t, err)
				svc := newForwardPartialUsageServiceForTest(upstream)
				_, err = svc.Forward(context.Background(), c, account, parsed)
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)

				beta := getHeaderRaw(upstream.lastReq.Header, "anthropic-beta")
				require.Equal(t, withBeta && mode != "oauth_mimic", anthropicBetaTokensContains(beta, perTurnBeta))
				wantControl := withBeta && mode != "oauth_mimic"
				messages := gjson.GetBytes(upstream.lastBody, "messages").Array()
				if wantControl {
					require.Len(t, messages, 4)
					require.Equal(t, "system", messages[2].Get("role").String())
					require.Equal(t, "Turn policy", messages[2].Get("content.0.text").String())
					require.Equal(t, "low", messages[2].Get("output_config.effort").String())
				} else {
					require.Len(t, messages, 4)
					require.False(t, messages[2].Get("output_config").Exists())
				}
				require.Equal(t, "Hello", messages[0].Get("content").String())
				require.Equal(t, "Hi", messages[1].Get("content").String())
				require.Equal(t, "Summarize that in one sentence.", messages[len(messages)-1].Get("content").String())
				require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "output_config.effort").String())
			})
		}
	}
}

func TestBuildUpstreamRequest_PerTurnControlPolicyFilter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const perTurnBeta = "per-turn-control-2026-07-01"
	const midConversationBeta = "mid-conversation-output-config-2026-07-01"
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"Hello"},` +
		`{"role":"system","content":[{"type":"text","text":"Turn policy"}],"output_config":{"effort":"low"}}]}`)
	for _, tc := range []struct {
		name string
		drop []string
		keep bool
	}{
		{"drop_per_turn_only", []string{perTurnBeta}, true},
		{"drop_mid_conversation_only", []string{midConversationBeta}, true},
		{"drop_both", []string{perTurnBeta, midConversationBeta}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Request.Header.Set("Anthropic-Beta", "claude-code-20250219,"+perTurnBeta+","+midConversationBeta)
			c.Set(betaPolicyFilterSetKey, buildBetaTokenSet(tc.drop))
			svc := &GatewayService{cfg: &config.Config{}}
			req, _, err := svc.buildUpstreamRequest(context.Background(), c,
				newAnthropicOAuthAccountForPartialUsageTest(), body,
				"oauth-token", "oauth", "claude-opus-5", false, false)
			require.NoError(t, err)
			for _, token := range tc.drop {
				require.False(t, anthropicBetaTokensContains(getHeaderRaw(req.Header, "anthropic-beta"), token))
			}
			out := readUpstreamBodyForTest(t, req)
			require.Equal(t, tc.keep, gjson.GetBytes(out, "messages.1.output_config").Exists())
			if tc.keep {
				require.Equal(t, "low", gjson.GetBytes(out, "messages.1.output_config.effort").String())
			} else {
				require.Len(t, gjson.GetBytes(out, "messages").Array(), 2)
				require.Equal(t, "Turn policy", gjson.GetBytes(out, "messages.1.content.0.text").String())
			}
		})
	}
}

func TestSanitizeAnthropicBodyForBetaTokens_MidConversationOutputConfig_NoFieldByteNoop(t *testing.T) {
	bodies := [][]byte{
		// 只有顶层 output_config（effort），消息无该字段
		[]byte(`{"model":"claude-opus-5","output_config":{"effort":"high"},"messages":[{"role":"system","content":[]},{"role":"user","content":"hi"}]}`),
		// 完全没有 output_config
		[]byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`),
	}
	for i, body := range bodies {
		out, changed := sanitizeAnthropicBodyForBetaTokens(body, "oauth-2025-04-20")
		require.Falsef(t, changed, "case %d: 无 message-level output_config → 不得改动", i)
		require.Truef(t, bytes.Equal(body, out), "case %d: 必须字节 no-op", i)
	}
}
