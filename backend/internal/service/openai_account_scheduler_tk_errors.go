package service

// openAICompatErrorPlatformLabel returns the platform identifier to embed in
// "no available accounts" error messages produced by the OpenAI-compat
// scheduler.
//
// Background (docs/bugs/2026-04-23-newapi-fifth-platform-audit.md, P1-2):
// the OpenAI-compat scheduling pool now carries both `openai` and `newapi`
// accounts (see docs/approved/newapi-as-fifth-platform.md). Hard-coded
// "no available OpenAI accounts" error text caused operator confusion when
// the failing group was actually `newapi`. We surface req.GroupPlatform
// verbatim, falling back to PlatformOpenAI for the legacy "no group / empty
// platform" case so existing tests / log greps that expect "openai" continue
// to work for openai-shaped requests.
//
// Kept in a TK-only companion file so future upstream merges of
// openai_account_scheduler.go do not collide on this branding choice.
func openAICompatErrorPlatformLabel(groupPlatform string) string {
	if groupPlatform == "" {
		return PlatformOpenAI
	}
	return groupPlatform
}
