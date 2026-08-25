package service

import "strings"

// tkDeclaredRegistryAliases maps a client-visible model id to the registry owner
// whose price it legitimately shares. It exists to make the difference between a
// DECLARED alias and an ACCIDENTAL substring match observable.
//
// WHY (2026-08-25): `deepseek-v4-flash-0731` has no overlay row of its own — by
// design, and there is a test forbidding one (billing_service_tk_qianfan_test.go
// asserts overlay["deepseek-v4-flash-0731"] is nil) because a second row would be
// a second owner free to drift. It nevertheless bills correctly, because
// getFallbackPricing carries a `strings.Contains(model, "deepseek-v4-flash")`
// branch that happens to land on the right owner. That is luck, not a contract:
// the same matcher is what makes `us.anthropic.claude-sonnet-5` resolve to
// `claude-3-5-sonnet` ($3/$15 instead of its own $2/$10).
//
// Declaring the alias here changes NO price — both paths resolve the identical
// owner, which TestDeclaredAliasKeepsOwnerPrice asserts through the real billing
// resolver. What it changes is classification: a declared alias is a settled
// decision and must not raise the served_at_fallback convergence alert, while a
// substring-matched id still must.
//
// Membership rule: add an id here only when its price SHOULD equal the owner's
// (a dated/prefixed SKU of the same model), the owner has a real registry row,
// and giving the id its own row would create a second owner. An id that deserves
// a genuinely different price does NOT belong here — it needs its own owner.
var tkDeclaredRegistryAliases = map[string]string{
	// Qianfan publishes this dated SKU under the same price as V4-Flash (its own
	// price page names it "V4-Flash-0731"), so the flash owner is the right one.
	"deepseek-v4-flash-0731": "deepseek-v4-flash",
}

// tkDeclaredRegistryAlias reports the declared owner for `model`, if any.
func tkDeclaredRegistryAlias(model string) (string, bool) {
	owner, ok := tkDeclaredRegistryAliases[strings.ToLower(strings.TrimSpace(model))]
	return owner, ok
}
