//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const (
	// 以 Codex 身份发出时上游按 Codex 约定回应，apply_patch / update_plan 是客户端
	// 工具注册表里真实存在的名字；被按 OpenCode 词表改成 edit / todowrite，客户端
	// 找不到该工具，Agent 循环就地断链。
	codexNativeApplyPatchSSEData = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"apply_patch","arguments":"{\"path\":\"/a/b.go\",\"old_string\":\"x\",\"new_string\":\"y\",\"replace_all\":true}"}}]}}]}`
	// 名字原样通过的合法 edit 调用：参数改写此前独立于名字映射是否命中，
	// 所以 path/old_string 照样被改成 filePath/oldString。
	legitEditCallSSEData = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"edit","arguments":"{\"path\":\"/a/b.go\",\"old_string\":\"x\"}"}}]}}]}`
	legitBashCallSSEData = `{"tool_calls":[{"function":{"name":"bash","arguments":"{\"command\":\"ls\",\"work_dir\":\"/tmp\"}"}}]}`
)

// newCodexIdentityTestContext 构造一个官方 Codex CLI 请求上下文（originator=codex_cli_rs）。
// 与 newOpenCodeIdentityTestContext 一样走真实的出站身份 owner。
func newCodexIdentityTestContext(t *testing.T) *gin.Context {
	t.Helper()
	return newToolCorrectionTestContext(t, "codex_cli_rs/0.104.0 (Mac OS 15.0; arm64) WezTerm", "codex_cli_rs")
}

// TestCodexToolCorrectionGateFollowsUpstreamOriginator 双向守卫：
// OpenCode 身份 → 词表还原生效；Codex 身份 → 名字与参数都不动。
func TestCodexToolCorrectionGateFollowsUpstreamOriginator(t *testing.T) {
	t.Run("opencode identity rewrites names and parameters", func(t *testing.T) {
		corrector := NewCodexToolCorrector()
		ginCtx := newOpenCodeIdentityTestContext(t)

		out, changed := corrector.CorrectToolCallsInSSEData(ginCtx, codexNativeApplyPatchSSEData)
		require.True(t, changed, "opencode 身份下 Codex 原生工具名必须还原成 OpenCode 词表")

		fn := "choices.0.delta.tool_calls.0.function"
		require.Equal(t, "edit", gjson.Get(out, fn+".name").String())
		args := gjson.Get(out, fn+".arguments").String()
		require.True(t, gjson.Get(args, "filePath").Exists())
		require.True(t, gjson.Get(args, "oldString").Exists())
		require.True(t, gjson.Get(args, "newString").Exists())
		require.True(t, gjson.Get(args, "replaceAll").Exists())
	})

	t.Run("codex identity leaves names and parameters untouched", func(t *testing.T) {
		corrector := NewCodexToolCorrector()
		ginCtx := newCodexIdentityTestContext(t)

		out, changed := corrector.CorrectToolCallsInSSEData(ginCtx, codexNativeApplyPatchSSEData)
		require.False(t, changed, "以 Codex 身份发出的请求，响应不得按 OpenCode 词表改写")
		require.Equal(t, codexNativeApplyPatchSSEData, out)
		require.Equal(t, 0, corrector.GetStats().TotalCorrected)
	})

	t.Run("codex identity keeps parameters of a legit edit call", func(t *testing.T) {
		corrector := NewCodexToolCorrector()
		ginCtx := newCodexIdentityTestContext(t)

		// 第 2 步：参数改写不再独立于名字映射执行。
		out, changed := corrector.CorrectToolCallsInSSEData(ginCtx, legitEditCallSSEData)
		require.False(t, changed, "名字原样通过的 edit 调用，其参数也不得被改写")
		require.Equal(t, legitEditCallSSEData, out)

		out, changed = corrector.CorrectToolCallsInSSEData(ginCtx, legitBashCallSSEData)
		require.False(t, changed, "bash 调用的 work_dir 也不得在 Codex 身份下被改名")
		require.Equal(t, legitBashCallSSEData, out)
	})

	t.Run("unknown identity does not rewrite", func(t *testing.T) {
		corrector := NewCodexToolCorrector()
		gin.SetMode(gin.TestMode)
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		// 出站身份未声明（API-key 账号、compat messages bridge）：不做任何还原。
		out, changed := corrector.CorrectToolCallsInSSEData(c, codexNativeApplyPatchSSEData)
		require.False(t, changed)
		require.Equal(t, codexNativeApplyPatchSSEData, out)

		// nil 上下文同样视为身份未知。
		out, changed = corrector.CorrectToolCallsInSSEData(nil, codexNativeApplyPatchSSEData)
		require.False(t, changed)
		require.Equal(t, codexNativeApplyPatchSSEData, out)
	})
}

// TestCodexToolCorrectionGateHonorsExplicitOriginatorHeader 客户端显式 originator
// 头是出站身份的最高优先级来源（resolveOpenAIUpstreamOriginator），门控必须尊重它。
func TestCodexToolCorrectionGateHonorsExplicitOriginatorHeader(t *testing.T) {
	tests := []struct {
		name          string
		userAgent     string
		originator    string
		wantRewritten bool
	}{
		{
			name: "explicit opencode header on a codex UA rewrites",
			// 非官方客户端一律以 opencode 身份出站，显式头同样落到 opencode。
			userAgent:     "codex_cli_rs/0.104.0 (Mac OS 15.0; arm64) WezTerm",
			originator:    openAIOpenCodeOriginator,
			wantRewritten: true,
		},
		{
			name:          "explicit codex header on an unofficial UA does not rewrite",
			userAgent:     "some-third-party-agent/1.0",
			originator:    "codex_cli_rs",
			wantRewritten: false,
		},
		{
			name:          "no header on an unofficial UA falls back to opencode and rewrites",
			userAgent:     "some-third-party-agent/1.0",
			originator:    "",
			wantRewritten: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", tt.userAgent)
			if tt.originator != "" {
				c.Request.Header.Set("originator", tt.originator)
			}
			// 身份由真实 owner 判定，门控消费同一个结果。
			// 与 openai_gateway_forward.go:224 的 isCodexCLI 判定同源。
			isOfficial := openai.IsCodexOfficialClientByHeaders(tt.userAgent, tt.originator)
			resolveOpenAIUpstreamOriginator(c, isOfficial)

			corrector := NewCodexToolCorrector()
			out, changed := corrector.CorrectToolCallsInSSEData(c, codexNativeApplyPatchSSEData)
			require.Equal(t, tt.wantRewritten, changed)
			if !tt.wantRewritten {
				require.Equal(t, codexNativeApplyPatchSSEData, out)
			}
		})
	}
}

// TestCodexToolCorrectionSuppressionObservedOnce 第 3 步守卫：门控放行本来会被
// 改写的载荷时留下可观测证据，每个请求只记一次。
func TestCodexToolCorrectionSuppressionObservedOnce(t *testing.T) {
	corrector := NewCodexToolCorrector()
	ginCtx := newCodexIdentityTestContext(t)

	_, _ = corrector.CorrectToolCallsInSSEData(ginCtx, codexNativeApplyPatchSSEData)
	_, suppressed := ginCtx.Get(openAIToolCorrectionSuppressedKey)
	require.True(t, suppressed, "命中映射表源名的载荷被放行时必须留下错配证据")
}

// TestCodexToolCorrectionSuppressionSilentOnBenignTraffic 守卫的另一方向：正常
// Codex 流量（映射表目标名、普通文本事件、客户端自定义工具）不得触发错配记录。
// 这是「反向映射」之所以不可行的根据：edit/read/webfetch 等目标名可以是客户端
// 合法声明的工具，不构成错配信号。
func TestCodexToolCorrectionSuppressionSilentOnBenignTraffic(t *testing.T) {
	benign := []struct {
		name string
		data string
	}{
		{"legit edit call", legitEditCallSSEData},
		{"legit bash call", legitBashCallSSEData},
		{"legit read call", `{"tool_calls":[{"function":{"name":"read","arguments":"{\"filePath\":\"/a\"}"}}]}`},
		{"legit webfetch call", `{"tool_calls":[{"function":{"name":"webfetch","arguments":"{\"url\":\"https://x\"}"}}]}`},
		{"client custom tool", `{"tool_calls":[{"function":{"name":"my_project_lint","arguments":"{}"}}]}`},
		{"plain text delta", `{"type":"response.output_text.delta","delta":"run apply_patch on the file"}`},
		{"mapping source name only inside text", `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"edit","arguments":"{\"filePath\":\"/a\",\"newString\":\"call apply_patch here\"}"}}]}}]}`},
	}

	for _, tt := range benign {
		t.Run(tt.name, func(t *testing.T) {
			corrector := NewCodexToolCorrector()
			ginCtx := newCodexIdentityTestContext(t)

			out, changed := corrector.CorrectToolCallsInSSEData(ginCtx, tt.data)
			require.False(t, changed)
			require.Equal(t, tt.data, out)
			_, suppressed := ginCtx.Get(openAIToolCorrectionSuppressedKey)
			require.False(t, suppressed, "正常 Codex 流量不得被记为身份/词表错配")
		})
	}
}
