package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
)

// tkApplyResponsesDispatchModelMapping applies the group-level messages-dispatch
// model mapping (opus_mapped_model 等) to a /v1/responses forward body, mirroring
// what the /v1/messages and /v1/chat/completions handlers already do via
// resolveOpenAIMessagesDispatchMappedModel.
//
// 背景：/v1/responses 入口此前不套用 group.MessagesDispatchModelConfig。带 claude
// 模型名（如 claude-opus-4-7）的请求会把裸 claude 名透传到 Codex/ChatGPT 后端，
// 返回上游 400 "The 'claude-opus-4-7' model is not supported when using Codex with
// a ChatGPT account."。/v1/messages（openai_gateway_handler.go:712）与
// /v1/chat/completions（openai_chat_completions.go:216）都在转发前把 claude 家族
// 名映射成配置的 gpt 模型，唯独 responses 入口漏接——这与 edge 中转无关，是入口层
// 缺一段映射（OAuth 直连账号同样命中）。
//
// 此 helper 在 handler 层、转发之前补上同一映射，覆盖 native responses 与
// raw-chat-fallback 两条转发路径（service.Forward 的 model 解析走 account 级
// GetMappedModel + codex 归一化，永不查 group dispatch 配置，故必须在 handler 改写
// body）。计费语义不变：handler 仍以原始 reqModel 记 requested_model，
// result.UpstreamModel 记实际上游模型，与 /v1/messages 路径一致。
//
// 映射基于「当前（已套用渠道映射后的）body model」判定：仅当其为 claude 家族名时
// 才改写，从而让渠道映射优先、dispatch 仅作 claude→gpt 兜底，优先级与 messages
// 路径相同。非 claude 模型（gpt-5.5 等）返回空映射，函数原样返回 body。
func tkApplyResponsesDispatchModelMapping(
	apiKey *service.APIKey,
	forwardBody []byte,
	replace openAIModelBodyReplaceFunc,
) []byte {
	if apiKey == nil || replace == nil {
		return forwardBody
	}
	currentModel := gjson.GetBytes(forwardBody, "model").String()
	mapped := resolveOpenAIMessagesDispatchMappedModel(apiKey, currentModel)
	if mapped == "" || mapped == currentModel {
		return forwardBody
	}
	return replace(forwardBody, mapped)
}
