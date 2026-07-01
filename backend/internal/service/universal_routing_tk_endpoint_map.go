package service

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// Universal Key（全能 Key）端点形状映射。
//
// 一个全能 key 的请求要落到哪个后端平台，由“入口端点形状”给出候选平台集合，
// 再与 key 主人的权限跨度求交、按确定规则挑出后端组（见 universal_routing_tk_resolver.go）。
// 候选集合统一从 engine 单一真值（OpenAICompatPlatforms / capability.go）派生，
// 避免硬编码漂移（preflight 的 newapi compat-pool 漂移门要求如此）。
//
// 设计文档：docs/approved/universal-key-routing.md。

// UniversalShape 是入口端点的“协议/模态形状”。
type UniversalShape int

const (
	ShapeSkip                 UniversalShape = iota // 元数据/无需调度端点：全能 key 不解析后端组
	ShapeAnthropicMessages                          // POST /v1/messages
	ShapeAnthropicCountTokens                       // POST /v1/messages/count_tokens
	ShapeOpenAIChat                                 // POST /v1/chat/completions、/v1/responses
	ShapeOpenAIEmbeddings                           // POST /v1/embeddings
	ShapeOpenAIImages                               // POST /v1/images/generations
	ShapeOpenAIImagesEdit                           // POST /v1/images/edits（仅 openai）
	ShapeOpenAIVideo                                // POST /v1/video/generations + GET poll
	ShapeGemini                                     // POST /v1beta/models/{model}:action
)

// UniversalShapeForRequest 由 gin 路由模式（c.FullPath()，handler 前即稳定可得）+ HTTP 方法
// 推断端点形状。匹配顺序很重要（count_tokens 先于 messages；images/edits 先于 images/generations）。
func UniversalShapeForRequest(fullPath, method string) UniversalShape {
	p := fullPath
	isPost := strings.EqualFold(method, http.MethodPost)
	switch {
	case strings.Contains(p, "/messages/count_tokens"):
		return ShapeAnthropicCountTokens
	case strings.Contains(p, "/messages"):
		return ShapeAnthropicMessages
	case strings.Contains(p, "/chat/completions"):
		return ShapeOpenAIChat
	case strings.Contains(p, "/responses"):
		if !isPost {
			return ShapeSkip // GET /responses 是 WebSocket，无模型、无需解析
		}
		return ShapeOpenAIChat
	case strings.Contains(p, "/embeddings"):
		return ShapeOpenAIEmbeddings
	case strings.Contains(p, "/images/edits"):
		return ShapeOpenAIImagesEdit
	case strings.Contains(p, "/images/generations"):
		return ShapeOpenAIImages
	case strings.Contains(p, "/video/generations"), strings.Contains(p, "/videos"):
		// submit(POST) 与 poll(GET /…/:task_id) 都要落到 openai-compat 处理器 —— 视频派发器
		// (tkOpenAICompatVideoFetchHandler)按 getGroupPlatform(c) 路由,GET 也必须解析出一个
		// openai-compat 后端组,否则 404。GET poll 无模型(resolver 以空模型按确定规则挑组);
		// 解析只在用户已授权的跨度内挑组,poll 不产生实质计费,故不存在误拒/串费。
		return ShapeOpenAIVideo
	case strings.Contains(p, "/v1beta/models"):
		if !isPost {
			return ShapeSkip // GET 列表/单模型元数据：无需解析
		}
		return ShapeGemini
	default:
		return ShapeSkip // /v1/models、/v1/usage 等元数据端点
	}
}

// universalCandidatePlatforms 给出某形状下的候选平台集合。
//   - forcedPlatform 非空（如 /antigravity 路由）→ 仅在该平台内解析。
//   - openai-compat 集合从 OpenAICompatPlatforms() 派生；embeddings/images/video
//     从 engine.capability 派生；anthropic/gemini 原生形状用原生平台常量。
//   - hasMessagesDispatch：跨度内存在开了 messages-dispatch 的组时，/v1/messages
//     才把 openai-compat 平台并入候选（用 Claude 名映射到 GPT 的场景）。
func universalCandidatePlatforms(shape UniversalShape, forcedPlatform string, hasMessagesDispatch bool, model string) []string {
	if forcedPlatform != "" {
		return []string{forcedPlatform}
	}
	switch shape {
	case ShapeAnthropicMessages:
		out := []string{PlatformAnthropic, PlatformAntigravity}
		if hasMessagesDispatch {
			out = append(out, OpenAICompatPlatforms()...)
		}
		return out
	case ShapeAnthropicCountTokens:
		return []string{PlatformAnthropic, PlatformAntigravity}
	case ShapeOpenAIChat:
		out := OpenAICompatPlatforms()
		// Gemini-native image models (gemini-*-image, nano-banana) ride
		// /v1/chat/completions but are served by the antigravity pool — not
		// openai-compat. Without antigravity in candidates, universal keys
		// land on openai/Codex and upstream rejects with "not supported when
		// using Codex with a ChatGPT account".
		if antigravity.IsImageModel(model) {
			out = append(out, PlatformAntigravity)
		}
		return out
	case ShapeOpenAIEmbeddings:
		return capabilityPlatforms(engine.BridgeEndpointEmbeddings)
	case ShapeOpenAIImages:
		return capabilityPlatforms(engine.BridgeEndpointImages)
	case ShapeOpenAIImagesEdit:
		return []string{PlatformOpenAI} // /v1/images/edits 仅 openai（handler 层硬门）
	case ShapeOpenAIVideo:
		out := capabilityPlatforms(engine.BridgeEndpointVideoSubmit)
		// grok-imagine-video rides the native xAI OAuth video API (channel_type=0),
		// not the new-api task-adaptor bridge. Without grok in candidates, universal
		// keys land on openai/newapi and fail at submit.
		if universalModelPlatformHint(model) == PlatformGrok {
			out = append(out, PlatformGrok)
		}
		return out
	case ShapeGemini:
		return []string{PlatformGemini, PlatformAntigravity}
	default:
		return nil
	}
}

// capabilityPlatforms 从 engine 能力注册表取某 bridge 端点的调度平台集合，
// 派生失败时回退到 OpenAICompatPlatforms() 的稳定子集 [openai, newapi]。
func capabilityPlatforms(bridgeEndpoint string) []string {
	if cap, ok := engine.CapabilityForEndpoint(bridgeEndpoint); ok && len(cap.SchedulingPlatforms) > 0 {
		return cap.SchedulingPlatforms
	}
	return []string{PlatformOpenAI, PlatformNewAPI}
}

// universalModelPlatformHint 由模型名给出“偏好平台”的廉价启发（仅作偏好，非硬过滤）。
// 用于在同一形状跨多个候选平台时（如 openai-compat 含 openai/newapi/grok）把请求
// 偏向正确平台：grok-4→grok、gpt-5→openai、doubao-seedream→newapi。未命中返回 ""，
// 退回确定性排序（resolver）。这是 best-effort 提示；真正“能否服务该模型”由下游调度器裁决。
func universalModelPlatformHint(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(m, "claude"):
		return PlatformAnthropic
	case strings.HasPrefix(m, "grok"):
		return PlatformGrok
	case antigravity.IsImageModel(m):
		// Served via antigravity generateContent, not gemini-platform / openai-compat.
		return PlatformAntigravity
	case strings.HasPrefix(m, "gemini"), strings.HasPrefix(m, "imagen"), strings.HasPrefix(m, "veo"):
		return PlatformGemini
	case strings.HasPrefix(m, "gpt"), strings.HasPrefix(m, "o1"), strings.HasPrefix(m, "o3"),
		strings.HasPrefix(m, "o4"), strings.HasPrefix(m, "chatgpt"), strings.HasPrefix(m, "dall-e"),
		strings.HasPrefix(m, "sora"), strings.HasPrefix(m, "text-embedding"), strings.HasPrefix(m, "whisper"):
		return PlatformOpenAI
	case strings.HasPrefix(m, "doubao"), strings.HasPrefix(m, "seedream"), strings.HasPrefix(m, "seedance"),
		strings.HasPrefix(m, "qwen"), strings.HasPrefix(m, "deepseek"), strings.HasPrefix(m, "glm"),
		strings.HasPrefix(m, "kimi"), strings.HasPrefix(m, "moonshot"), strings.HasPrefix(m, "ernie"),
		strings.HasPrefix(m, "hunyuan"), strings.HasPrefix(m, "step-"), strings.HasPrefix(m, "minimax"):
		return PlatformNewAPI
	default:
		return ""
	}
}
