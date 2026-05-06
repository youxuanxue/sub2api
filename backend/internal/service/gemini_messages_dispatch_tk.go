package service

import "strings"

// TKResolveGeminiDispatchModel 镜像 upstream (g *Group)ResolveMessagesDispatchModel
// 的结构，但服务于 platform=gemini 分组：
//
//   - 复用 g.MessagesDispatchModelConfig JSON（精确映射 + opus/sonnet/haiku
//     家族映射），让运维在同一个分组级表单里同时管 openai 与 gemini 的
//     Claude→上游模型映射。控制面跨平台一致 (§OPC 统一控制面)。
//   - 不使用 upstream defaultOpenAIMessagesDispatchOpusMappedModel 等 GPT
//     默认常量 —— gemini 没有 upstream-canonical 默认；运维没配则返回 ""，
//     调用方继续走原 model（保留可观测的 404 上游反馈优于静默替换）。
//   - 已是 gemini-* 形态的请求模型直接 passthrough，避免运维误把
//     gemini-3.1-pro-preview 错映射成自己。
//
// 调用范围：仅 /v1/messages → gemini 桥接路径
// (gemini_messages_compat_service.go Forward())。/v1beta/models/{model}:{action}
// (ForwardNative) 不在范围 —— Gemini-CLI 直接走 Google-native API 时通常
// 已主动发 gemini-* 模型名；扩范围会无谓增加 upstream-edit 面，需要时另开 PR。
//
// AllowMessagesDispatch 语义：openai 流上该 bool 控制
// /v1/messages → /v1/chat/completions 协议级翻译开关；gemini 桥接没有
// 协议翻译需求，故 gemini 路径不读该 bool，配置存在自驱动。
//
// 复用 upstream 包内未导出函数：
// normalizeOpenAIMessagesDispatchModelConfig、claudeMessagesDispatchFamily
// 都在 openai_messages_dispatch.go，platform-agnostic。
func (g *Group) TKResolveGeminiDispatchModel(requestedModel string) string {
	if g == nil {
		return ""
	}
	requested := strings.TrimSpace(requestedModel)
	if requested == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(requested), "gemini-") {
		return ""
	}
	cfg := normalizeOpenAIMessagesDispatchModelConfig(g.MessagesDispatchModelConfig)
	if mapped := strings.TrimSpace(cfg.ExactModelMappings[requested]); mapped != "" {
		return mapped
	}
	switch claudeMessagesDispatchFamily(requested) {
	case "opus":
		return strings.TrimSpace(cfg.OpusMappedModel)
	case "sonnet":
		return strings.TrimSpace(cfg.SonnetMappedModel)
	case "haiku":
		return strings.TrimSpace(cfg.HaikuMappedModel)
	}
	return ""
}
