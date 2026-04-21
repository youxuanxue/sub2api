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
// OpenAICompatPlatforms(); scripts/preflight.sh § 9 (newapi compat-pool drift)
// guards the consistency.
func isOpenAICompatPlatformGroup(g *Group) bool {
	if g == nil {
		return false
	}
	return IsOpenAICompatPlatform(g.Platform)
}
