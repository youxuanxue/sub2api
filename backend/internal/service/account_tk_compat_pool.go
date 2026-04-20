package service

// IsOpenAICompatPoolMember reports whether this account is eligible for the
// scheduling pool of an OpenAI-compatible gateway group whose Group.Platform
// equals groupPlatform.
//
// This is the canonical helper introduced by
// docs/approved/newapi-as-fifth-platform.md (§3.2). It expresses the
// "scheduling pool membership" predicate that scheduler / sticky / fresh-recheck
// filters need, distinct from the legacy IsOpenAI() which only answers
// "is this account on the openai platform?". The two predicates were
// conflated before this design — the cost was that any group with
// Platform="newapi" silently failed because IsOpenAI() filtered every
// candidate out.
//
// Semantics (must match design §2.1 / §3.2):
//
//   - account.Platform must equal groupPlatform — strict equality, no mixing
//   - for the newapi pool, account.ChannelType MUST be > 0; ChannelType == 0
//     means the account is incompletely configured (no New API adaptor target)
//     and would crash bridge dispatch. Excluding it here is the cheapest
//     defense.
//   - empty groupPlatform returns false; treat unknown platforms as "no pool",
//     not as "openai pool" — that protects against accidental mixing if a new
//     platform shows up before this helper is updated.
func (a *Account) IsOpenAICompatPoolMember(groupPlatform string) bool {
	if a == nil || groupPlatform == "" {
		return false
	}
	if a.Platform != groupPlatform {
		return false
	}
	if groupPlatform == PlatformNewAPI {
		return a.ChannelType > 0
	}
	return true
}

// OpenAICompatPlatforms returns the platform identifiers eligible for the
// OpenAI-compatible gateway entrypoints (chat completions, messages, responses).
//
// Mirrors the predicate used at the route layer
// (`isOpenAICompatPlatform` in routes/gateway_tk_openai_compat_handlers.go).
// When adding a sixth compat platform, BOTH places must be updated; the
// scripts/preflight.sh § 2 drift check guards against forgetting either side.
func OpenAICompatPlatforms() []string {
	return []string{PlatformOpenAI, PlatformNewAPI}
}
