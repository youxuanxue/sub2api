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
// scripts/preflight.sh § 9 (newapi compat-pool drift) check guards against
// forgetting either side.
func OpenAICompatPlatforms() []string {
	return []string{PlatformOpenAI, PlatformNewAPI}
}

// IsOpenAICompatPlatform reports whether the given platform identifier
// participates in the OpenAI-compatible request shape (i.e. clients speaking
// the OpenAI HTTP protocol). This is the canonical *string-arg* sibling of
// IsOpenAICompatPoolMember (which takes an Account) and routes-layer
// `isOpenAICompatPlatform` (which is private to the routes package).
//
// Use this whenever a handler / service has a raw platform string in scope and
// needs to decide between OpenAI-shape and Anthropic-shape default behavior
// (e.g. `/v1/models` fallback list, default upstream protocol guess).
//
// Strict equality only — empty / unknown returns false. Adding a sixth compat
// platform requires updating OpenAICompatPlatforms() above; this helper
// derives from that list automatically.
func IsOpenAICompatPlatform(platform string) bool {
	if platform == "" {
		return false
	}
	for _, p := range OpenAICompatPlatforms() {
		if platform == p {
			return true
		}
	}
	return false
}
