//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// deepseek-v4-flash-0731 must keep billing from the deepseek-v4-flash owner (no
// second owner, no $0) while NO LONGER raising the served_at_fallback convergence
// alert — sharing that owner is a declared decision, not a gap. 2026-08-25: the
// alert was firing on it and the digest printed the already-billed tokens as
// "未计费", which is what sent an operator hunting a nonexistent revenue leak.
func TestDeclaredAliasSuppressesFallbackAlertButKeepsPrice(t *testing.T) {
	owner, declared := tkDeclaredRegistryAlias("deepseek-v4-flash-0731")
	require.True(t, declared, "0731 must be a declared alias")
	require.Equal(t, "deepseek-v4-flash", owner)

	// Case-insensitive and whitespace-tolerant, since it is keyed off client input.
	owner, declared = tkDeclaredRegistryAlias("  DeepSeek-V4-Flash-0731 ")
	require.True(t, declared)
	require.Equal(t, "deepseek-v4-flash", owner)

	// The owner itself is not an alias of anything.
	_, declared = tkDeclaredRegistryAlias("deepseek-v4-flash")
	require.False(t, declared)

	// An id that only resolves via the substring matcher stays unsuppressed: that
	// accidental-owner case is precisely what the alert must keep surfacing.
	_, declared = tkDeclaredRegistryAlias("deepseek-v4-flash-9999")
	require.False(t, declared)

	// The declared owner must exist in the overlay, or the alias would silently
	// resolve to nothing and the price would fall through to the matcher again.
	require.NotNil(t, tkRegistryAliasOwnerPricing(owner),
		"declared alias owner must be a real overlay owner")
}

// The predicate this PR actually changes is IsServedViaFamilyFloor — asserting
// the lookup table alone would leave the behaviour change untested. Every
// declared alias must read as NOT served-via-floor (it is a settled decision),
// while a substring-only id in the same family must still read as true, and the
// resolved price must be identical either way.
func TestDeclaredAliasIsNotServedViaFamilyFloor(t *testing.T) {
	// Empty registry: nothing has a direct owner here, so the verdict is decided
	// by the declared-alias table and the substring matcher alone.
	billing := newConsistencyBilling(t, []byte(`{}`))

	for alias, owner := range tkDeclaredRegistryAliases {
		require.False(t, billing.IsServedViaFamilyFloor(alias),
			"declared alias %q must not raise served_at_fallback", alias)

		// Price is unchanged by declaring it: both paths land on the same owner.
		viaAlias := billing.getRegistryAliasPricing(alias)
		viaOwner := tkRegistryAliasOwnerPricing(owner)
		require.NotNil(t, viaAlias, "alias %q must still resolve a price", alias)
		require.NotNil(t, viaOwner, "owner %q must resolve a price", owner)
		require.Equal(t, viaOwner.InputPricePerToken, viaAlias.InputPricePerToken,
			"alias %q input price must equal owner %q", alias, owner)
		require.Equal(t, viaOwner.OutputPricePerToken, viaAlias.OutputPricePerToken,
			"alias %q output price must equal owner %q", alias, owner)
	}

	// Same family, NOT declared: still surfaced, so the alert keeps catching
	// accidental substring owners.
	require.True(t, billing.IsServedViaFamilyFloor("deepseek-v4-flash-9999"),
		"an undeclared substring match must still raise served_at_fallback")
}

// Every declared alias must point at a resolvable overlay owner, and must not
// itself be an overlay owner (that would make two owners for one price — the
// exact drift tk_pricing_overlay's single-owner rule forbids).
func TestDeclaredAliasesAreWellFormed(t *testing.T) {
	overlay := loadTKPricingOverlay()
	for alias, owner := range tkDeclaredRegistryAliases {
		require.NotEmpty(t, owner, "alias %q has empty owner", alias)
		require.Nil(t, overlay[alias],
			"alias %q must NOT be its own overlay owner (two owners drift)", alias)
		require.NotNil(t, tkRegistryAliasOwnerPricing(owner),
			"alias %q points at %q which is not a resolvable overlay owner", alias, owner)
	}
}
