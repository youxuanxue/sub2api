package service

// isOpenAICompatPlatformGroup reports whether the group's platform participates
// in the OpenAI-compatible gateway surface (chat completions / messages /
// responses). It is the group-side counterpart of
// IsOpenAICompatPoolMember on the account side, and mirrors the route-layer
// predicate `isOpenAICompatPlatform`.
//
// Used by sanitizeGroupMessagesDispatchFields to decide whether a group is
// allowed to retain its messages_dispatch_model_config — design §2.3:
//
//	| group.platform | AllowMessagesDispatch | MessagesDispatchModelConfig |
//	|----------------|-----------------------|-----------------------------|
//	| openai         | configurable          | configurable                |
//	| newapi         | configurable (NEW)    | configurable (NEW)          |
//	| anthropic      | force false           | force clear                 |
//	| gemini         | force false           | force clear                 |
//	| antigravity    | force false           | force clear                 |
//
// Adding a sixth compat platform requires updating BOTH this predicate and
// OpenAICompatPlatforms(); scripts/preflight.sh "newapi compat-pool drift"
// guards the consistency.
func isOpenAICompatPlatformGroup(g *Group) bool {
	if g == nil {
		return false
	}
	return IsOpenAICompatPlatform(g.Platform)
}

// tkGroupKeepsDispatchConfig 决定一个分组在 sanitize 阶段是否保留
// MessagesDispatchModelConfig。upstream sanitizer 此前只让
// isOpenAICompatPlatformGroup 为 true 的分组（openai / newapi）保留配置；
// TK 把 gemini 也纳入该集合，使分组级 Claude→上游模型映射在
// openai / newapi / gemini 三个平台行为一致。
//
// 故意不扩 isOpenAICompatPlatformGroup 自身的语义：那个谓词在前后端 11+
// 处用于"是否走 OpenAI HTTP 形态"判断（route 层、account pool、
// scheduling），扩到 gemini 会全局漂移；本谓词只承担"是否保留 dispatch
// config"这一窄语义。
//
// AllowMessagesDispatch (bool) 在 openai 流上控制
// /v1/messages → /v1/chat/completions 协议级翻译开关；gemini 桥接没有协
// 议翻译需求，故 gemini 路径不读该 bool，配置存在自驱动。
//
// 调用点：upstream openai_messages_dispatch.go sanitizeGroupMessagesDispatchFields。
func tkGroupKeepsDispatchConfig(g *Group) bool {
	if g == nil {
		return false
	}
	if isOpenAICompatPlatformGroup(g) {
		return true
	}
	return g.Platform == PlatformGemini
}
