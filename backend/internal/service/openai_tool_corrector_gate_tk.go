package service

import (
	"bytes"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

// 出站身份与响应侧词表必须同源。`resolveOpenAIUpstreamOriginator` 决定本次请求
// 以什么 originator 发给上游（官方 Codex 客户端 → codex-tui；其余一律 "opencode"，
// 客户端显式 originator 头可覆盖）。codexToolNameMapping 则把 Codex 原生工具名
// 还原成 OpenCode 词表——只有在确实以 OpenCode 身份发出时，上游按 OpenCode 约定
// 回应，这个还原才成立。两件事此前各自为政：以 Codex 身份发出的响应照样被按
// OpenCode 词表改写，客户端收到自己工具注册表里不存在的 `edit`，Agent 循环断链。
//
// 这里把身份判定的结果记在请求上下文里，五条响应路径共同消费同一个决定，
// 而不是每处各长一个条件。
const (
	openAIUpstreamOriginatorContextKey = "openai_upstream_originator_tk"
	openAIOpenCodeOriginator           = "opencode"
	openAIToolCorrectionSuppressedKey  = "openai_tool_correction_suppressed_tk"
)

// rememberOpenAIUpstreamOriginator 由出站身份的单一 owner 调用，记录本次请求
// 实际发给上游的 originator。未记录 == 本次请求没有声明 Codex/OpenCode 身份
// （API-key 账号、compat messages bridge 等），此时不做任何词表还原。
func rememberOpenAIUpstreamOriginator(c *gin.Context, originator string) {
	if c == nil {
		return
	}
	trimmed := strings.TrimSpace(originator)
	if trimmed == "" {
		return
	}
	c.Set(openAIUpstreamOriginatorContextKey, trimmed)
}

// publishOpenAIClientOutboundIdentity 在 isCodexCLI 判定处消费出站身份 owner，
// 让响应侧词表门控对所有账号类型都有身份可读——API-key 账号不发 originator 头，
// 出站身份 owner 不会在 header 路径上被调用，但客户端身份（官方 Codex vs 其余）
// 依然决定上游会按哪套词表回应。
func publishOpenAIClientOutboundIdentity(c *gin.Context, isCodexCLI bool) {
	_ = resolveOpenAIUpstreamOriginator(c, isCodexCLI)
}

func openAIUpstreamOriginatorFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, ok := c.Get(openAIUpstreamOriginatorContextKey)
	if !ok {
		return ""
	}
	originator, _ := value.(string)
	return originator
}

// openAIToolCorrectionUsesOpenCodeVocabulary 回答「本次请求的响应该不该按 OpenCode
// 词表还原」。只有以 OpenCode 身份出站时才为真；官方 Codex 身份（含客户端显式
// originator 头覆盖）为假，身份未知亦为假。
func openAIToolCorrectionUsesOpenCodeVocabulary(c *gin.Context) bool {
	return strings.EqualFold(openAIUpstreamOriginatorFromContext(c), openAIOpenCodeOriginator)
}

// observeSuppressedCodexToolCorrection 留下错配证据：门控生效后，本来会被改写的
// 载荷（即旧代码会改坏的那一批）在非 OpenCode 身份上被放行。只在命中映射表
// **源名**时触发——这些名字从来不是客户端声明的工具，而是模型吐出来的 Codex 原生
// 名，所以不会在正常流量上误报。每个请求只记一次，避免长流刷日志。
//
// 反方向（Codex 身份的响应里出现 `edit`/`read`/`bash` 等映射**目标名**）不作为错配
// 信号：客户端完全可以合法声明这些名字的工具，而显式 originator 头允许任何客户端
// 以 Codex 身份出站，该判据会在正常流量上误报。
func observeSuppressedCodexToolCorrection(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	if _, already := c.Get(openAIToolCorrectionSuppressedKey); already {
		return
	}
	source, target, found := firstCodexToolMappingSourceInPayload(data)
	if !found {
		return
	}
	c.Set(openAIToolCorrectionSuppressedKey, true)

	requestID := ""
	if c.Request != nil {
		requestID, _ = c.Request.Context().Value(ctxkey.RequestID).(string)
	}
	model, _ := c.Get(OpsModelKey)
	logger.L().Info("gateway.codex_tool_correction_suppressed",
		zap.String("component", "audit.tool_correction"),
		zap.String("request_id", requestID),
		zap.Any("model", model),
		zap.String("upstream_originator", openAIUpstreamOriginatorFromContext(c)),
		zap.String("tool_name", source),
		zap.String("opencode_vocabulary_name", target),
	)
}

// firstCodexToolMappingSourceInPayload 在已验证的 tool-call 载荷里找出第一个命中
// 映射表源名的 function.name。先用字节过滤避开 JSON 遍历，再按工具调用节点精确取名，
// 避免把正文里出现的同名字符串当成工具调用。
func firstCodexToolMappingSourceInPayload(data []byte) (string, string, bool) {
	candidate := false
	for source := range codexToolNameMapping {
		if bytes.Contains(data, []byte(`"`+source+`"`)) {
			candidate = true
			break
		}
	}
	if !candidate {
		return "", "", false
	}
	for _, name := range collectCodexToolCallFunctionNames(data) {
		if target, ok := codexToolNameMapping[name]; ok {
			return name, target, true
		}
	}
	return "", "", false
}

// collectCodexToolCallFunctionNames 收集载荷里工具调用节点的 function.name，
// 路径集合与 CorrectToolCallsInSSEBytes 保持一致。
func collectCodexToolCallFunctionNames(data []byte) []string {
	names := make([]string, 0, 4)
	appendName := func(functionPath string) {
		result := gjson.GetBytes(data, functionPath+".name")
		if result.Type != gjson.String {
			return
		}
		if name := strings.TrimSpace(result.Str); name != "" {
			names = append(names, name)
		}
	}
	appendArray := func(toolCallsPath string) {
		gjson.GetBytes(data, toolCallsPath).ForEach(func(_, value gjson.Result) bool {
			if name := strings.TrimSpace(value.Get("function.name").String()); name != "" {
				names = append(names, name)
			}
			return true
		})
	}

	appendArray("tool_calls")
	appendName("function_call")
	appendArray("delta.tool_calls")
	appendName("delta.function_call")
	gjson.GetBytes(data, "choices").ForEach(func(key, _ gjson.Result) bool {
		prefix := "choices." + key.String()
		appendArray(prefix + ".message.tool_calls")
		appendName(prefix + ".message.function_call")
		appendArray(prefix + ".delta.tool_calls")
		appendName(prefix + ".delta.function_call")
		return true
	})
	return names
}
