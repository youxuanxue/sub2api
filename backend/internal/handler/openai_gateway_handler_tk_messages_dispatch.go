package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func resolveOpenAIMessagesMetadataSession(sessionHash, promptCacheKey, reqModel string, body []byte) (string, string) {
	// Anthropic metadata.user_id 只作为账号粘性信号。上游 GPT/Codex 缓存键
	// 交给 ForwardAsAnthropic 从 cache_control 或完整消息 digest 派生，避免
	// 固定 metadata key 压住后续 turn 的缓存滚动。
	if sessionHash != "" {
		return sessionHash, promptCacheKey
	}
	if userID := strings.TrimSpace(gjson.GetBytes(body, "metadata.user_id").String()); userID != "" {
		seed := reqModel + "-" + userID
		sessionHash = service.DeriveSessionHashFromSeed(seed)
	}
	return sessionHash, promptCacheKey
}

func resolveOpenAIMessagesDispatchMappedModelForContext(c *gin.Context, apiKey *service.APIKey, requestedModel string) string {
	if apiKey == nil || apiKey.Group == nil {
		return ""
	}
	// composite 解析到 grok/CN 目标时调度级映射不适用（Group 级映射的 gpt-5.x
	// 默认值是 openai 专属,发给这些上游必错）,模型改写交给账号级 model_mapping。
	if apiKey.Group.Platform == service.PlatformComposite && c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok &&
			(platform == service.PlatformGrok || service.IsCNProvider(platform)) {
			return ""
		}
	}
	return strings.TrimSpace(apiKey.Group.ResolveMessagesDispatchModel(requestedModel))
}

func resolveOpenAIMessagesDispatchMappedModel(apiKey *service.APIKey, requestedModel string) string {
	return resolveOpenAIMessagesDispatchMappedModelForContext(nil, apiKey, requestedModel)
}

func allowOpenAICompatibleMessagesDispatch(c *gin.Context, apiKey *service.APIKey) bool {
	if apiKey == nil || apiKey.Group == nil {
		return true
	}
	if apiKey.Group.Platform == service.PlatformGrok {
		return true
	}
	// 国产供应商分组与 grok 同语义:/v1/messages 就是其主要服务形态(anthropic
	// 协议账号原生直通 Claude Code),无需 allow_messages_dispatch 开关授权——
	// 该开关对非 openai/composite 平台恒被 sanitizeGroupMessagesDispatchFields 置 false,
	// 若不豁免,CN 分组将永远 403。
	if service.IsCNProvider(apiKey.Group.Platform) {
		return true
	}
	// composite 分组解析到 grok/CN 目标时与对应独立分组同语义豁免；
	// 解析到 openai 目标则受 composite 分组自身的可配置开关控制。
	if apiKey.Group.Platform == service.PlatformComposite && c != nil && c.Request != nil {
		if platform, ok := service.ResolvedTargetPlatformFromContext(c.Request.Context()); ok &&
			(platform == service.PlatformGrok || service.IsCNProvider(platform)) {
			return true
		}
	}
	return apiKey.Group.AllowMessagesDispatch
}
