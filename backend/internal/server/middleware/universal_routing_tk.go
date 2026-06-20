package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// 压缩请求体超过该大小时,跳过“解码取模型”以界定额外解码内存(取模型仅作平台偏好提示)。
const universalPeekMaxCompressedBytes = 256 << 10

// MaybeResolveUniversal 在认证流程内部、分组/订阅校验之前，把“全能 Key”按请求的入口端点 +
// 模型解析到一个后端组，并把请求“伪装”成绑定该后端组的普通 key（替换 apiKey.Group/GroupID）。
// 之后现成的分组可用性/权限/订阅/余额校验、调度、计费、转发全部作用在该后端组上，零改动。
//
// 返回值 handled：
//   - true  表示已写出协议正确的错误并 Abort，调用方应 return；
//   - false 表示“继续正常认证”（已成功替换为后端组，或本请求无需解析 / 非全能 key）。
//
// 不主动设置 ctxkey.ForcePlatform：替换后端组本身就让下游按该组平台派生（保留如 anthropic+
// antigravity 混合调度等“普通组 key”语义）。仅“读取”已有的 ForcePlatform（如 /antigravity 路由）
// 把候选限制在该平台内，从不覆盖显式 force。
func MaybeResolveUniversal(c *gin.Context, apiKey *service.APIKey, resolver *service.UniversalRoutingResolver) bool {
	if resolver == nil || apiKey == nil || !apiKey.IsUniversal() {
		return false
	}

	fullPath := c.FullPath()
	if fullPath == "" && c.Request != nil && c.Request.URL != nil {
		fullPath = c.Request.URL.Path
	}
	method := http.MethodGet
	if c.Request != nil {
		method = c.Request.Method
	}

	shape := service.UniversalShapeForRequest(fullPath, method)
	if shape == service.ShapeSkip {
		// 元数据端点（/v1/models、/v1beta/models GET 等）：不解析后端组，让全能 key
		// 以无分组继续（RequireGroupAssignment 已放行 universal key；handler 回落默认）。
		return false
	}

	forcedPlatform := ""
	if c.Request != nil {
		if v, ok := c.Request.Context().Value(ctxkey.ForcePlatform).(string); ok {
			forcedPlatform = strings.TrimSpace(v)
		}
	}

	model := peekUniversalModel(c, shape)

	backing, err := resolver.Resolve(c.Request.Context(), apiKey, shape, model, forcedPlatform)
	if err != nil {
		// 区分“真没有被授权的组”(403,业务语义) 与跨度加载失败等内部错误(500,可重试):
		// 后者不该被伪装成“该模型不在你的套餐内”。
		if errors.Is(err, service.ErrUniversalNoEntitledGroup) {
			writeUniversalRoutingError(c, shape, model)
		} else {
			writeUniversalRoutingInternalError(c, shape)
		}
		c.Abort()
		return true
	}

	// 伪装成绑定该后端组的普通 key。apiKey 是每请求新建的结构（snapshotToAPIKey），就地替换安全。
	apiKey.Group = backing
	apiKey.GroupID = &backing.ID
	return false
}

// peekUniversalModel 取请求模型名（仅作平台偏好提示）。body 类端点读 body 后“原样还原”
// （连同原始头），让后续 handler 的 body 读取行为字节级不受影响；gemini 取 URL；
// images/edits 是 multipart（不读 body）。
//
// video：POST 提交（submit）是 JSON body，必须读出模型名 —— 候选平台 [openai, newapi]
// 跨多个后端组（如 google-vertex ch41 / volcengine ch45 才支持视频，而 deepseek ch43 /
// Qwen ch17 不支持）。不取模型则 resolver 退回“按 sort_order/id 确定性挑组”，会把视频
// 请求落到一个非视频渠道的组，selected account 的 channel_type 在 handler 视频门
// （engine.IsVideoSupportedForAccount）处被拒，表现为“全局视频 400”。读模型后既能经
// 「组已服务模型集」收敛到对的组、又能让平台 hint（veo→gemini、seedance→newapi）偏向。
// GET 轮询（poll）无 body、无模型：poll 用 VideoTaskCache 里 submit 时固定的上游路由，
// 不重新选号，故 model="" 安全（任一 openai-compat 组即满足路由层的平台门）。
func peekUniversalModel(c *gin.Context, shape service.UniversalShape) string {
	switch shape {
	case service.ShapeGemini:
		if ma := c.Param("modelAction"); ma != "" {
			return geminiModelFromAction(ma)
		}
		return c.Param("model")
	case service.ShapeOpenAIImagesEdit:
		return "" // multipart：无需模型
	case service.ShapeOpenAIVideo:
		if c.Request != nil && strings.EqualFold(c.Request.Method, http.MethodPost) {
			return peekModelFromJSONBody(c) // submit：JSON body 含 model
		}
		return "" // poll（GET）：无 body、无需模型
	default:
		return peekModelFromJSONBody(c)
	}
}

// peekModelFromJSONBody 读取请求体、原样还原，再在副本上解码并提取顶层 "model"。
// 关键：还原的是“原始字节 + 原始头”，绝不改动 Content-Encoding/Length，使 handler 后续
// 调用 ReadRequestBodyWithPrealloc 的行为与本中间件未运行时完全一致。
func peekModelFromJSONBody(c *gin.Context) string {
	if c.Request == nil || c.Request.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(c.Request.Body) // 受上游 bodyLimit(MaxBytesReader) 约束
	if err != nil {
		// body 可能已被部分消费；尽力还原已读到的字节，避免 handler 拿到空 body。
		c.Request.Body = io.NopCloser(bytes.NewReader(raw))
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))

	peekBytes := raw
	if enc := strings.ToLower(strings.TrimSpace(c.Request.Header.Get("Content-Encoding"))); enc != "" && enc != "identity" {
		// 仅对较小的压缩体解码取模型,界定额外解码内存(模型名是平台偏好提示,
		// 大体跳过只是退回确定性挑组,不影响功能)。原始体已还原,不受影响。
		if len(raw) > universalPeekMaxCompressedBytes {
			return ""
		}
		if decoded, derr := pkghttputil.DecodeContentEncodedBody(enc, raw); derr == nil {
			peekBytes = decoded
		}
	}

	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(peekBytes, &probe)
	return strings.TrimSpace(probe.Model)
}

// geminiModelFromAction 从 "{model}:{action}"（或纯 "{model}"）里取模型名。
func geminiModelFromAction(modelAction string) string {
	s := strings.TrimPrefix(modelAction, "/")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// writeUniversalRoutingError 按入口协议形状写出“该模型/平台不在你的套餐内”的 403。
func writeUniversalRoutingError(c *gin.Context, shape service.UniversalShape, model string) {
	const status = http.StatusForbidden
	msg := "No platform in your plan can serve this request."
	if model != "" {
		msg = "No platform in your plan can serve model \"" + model + "\"."
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonAPIKeyGroupUnassigned)

	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, msg)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, msg)
	default:
		// OpenAI 形状（chat/responses/embeddings/images/video）
		c.JSON(status, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "invalid_request_error",
				"code":    "universal_no_entitled_group",
			},
		})
	}
}

// writeUniversalRoutingInternalError 按入口协议形状写出 500：跨度加载/内部失败,而非授权问题。
// 区别于 writeUniversalRoutingError(403),避免把可重试的服务端错误伪装成“不在你的套餐内”。
func writeUniversalRoutingInternalError(c *gin.Context, shape service.UniversalShape) {
	const status = http.StatusInternalServerError
	const msg = "Failed to resolve a backing platform for this request. Please retry."
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, msg)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, msg)
	default:
		c.JSON(status, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "api_error",
				"code":    "universal_routing_internal_error",
			},
		})
	}
}
