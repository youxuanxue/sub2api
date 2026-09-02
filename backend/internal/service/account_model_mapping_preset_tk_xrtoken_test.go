//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

// XRToken account 96 is a ch54 (DoubaoVideo) ARK-compatible reseller. Its five
// Seedance SKUs are projected from the manifest's XRToken property scope: two
// primary ch54 rows plus three shared ch45 rows that declare an additional scope.
// The account preset must consume that one declarative owner rather than keep a
// second Go list of shared ids.
func xrTokenProbeAccount(baseURL string) *Account {
	return &Account{
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeDoubaoVideo,
		Credentials: map[string]any{"base_url": baseURL},
	}
}

func TestXRTokenAccountPresetMergesSharedAndScopedModels(t *testing.T) {
	account := xrTokenProbeAccount(newapiintegration.XRTokenBaseURL)
	if !isNewAPIXRTokenAccount(account) {
		t.Fatal("ch54 + XRToken base_url must be recognized as an XRToken account")
	}

	// Derived independently from the manifest scope, not from the account helper
	// under test and not from a second hand-copied list.
	want := tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeDoubaoVideo,
		newapiintegration.XRTokenBaseURL,
	)
	if len(want) == 0 {
		t.Fatal("manifest projection produced no XRToken models — manifest wiring regressed")
	}

	got := NewAPIModelMappingPresetIDsForAccount(account)
	if len(got) != len(want) {
		t.Fatalf("preset ids = %v (%d), want %v (%d)", got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("preset ids = %v, want %v", got, want)
		}
	}

	// display projection follows the same scope (all five rows are display=true)
	if disp := NewAPIModelDisplayIDsForAccount(account); len(disp) != len(want) {
		t.Fatalf("display ids = %v (%d), want %d entries", disp, len(disp), len(want))
	}
}

// The base XRToken's own SDK docs hand out carries a trailing /v1. An admin who
// pastes it verbatim must get identical behavior, or the account silently falls
// back to the plain-Ark preset (2 of 5 models) while still relaying correctly.
func TestXRTokenAccountPresetAcceptsV1SuffixedBase(t *testing.T) {
	withSuffix := xrTokenProbeAccount(newapiintegration.XRTokenBaseURL + "/v1")
	if !isNewAPIXRTokenAccount(withSuffix) {
		t.Fatal("XRToken base_url with a trailing /v1 must still be recognized")
	}
	bare := NewAPIModelMappingPresetIDsForAccount(xrTokenProbeAccount(newapiintegration.XRTokenBaseURL))
	got := NewAPIModelMappingPresetIDsForAccount(withSuffix)
	if len(got) != len(bare) {
		t.Fatalf("/v1-suffixed preset = %v, want same as bare root %v", got, bare)
	}
}

// A plain ch54 VolcEngine Ark account must NOT inherit XRToken's shared rows —
// it reaches official Ark, where those model ids live under ch45's own account.
// This is the isolation half of the sentinel: sharing a channel_type must not
// leak serving intent between two different upstreams.
func TestPlainArkCh54AccountDoesNotInheritXRTokenSharedModels(t *testing.T) {
	account := xrTokenProbeAccount("https://ark.cn-beijing.volces.com")
	if isNewAPIXRTokenAccount(account) {
		t.Fatal("official Ark base_url on ch54 must NOT be treated as XRToken")
	}
	got := NewAPIModelMappingPresetIDsForAccount(account)
	require.Empty(t, got,
		"XRToken-only ch54 rows must stay scoped to its base_url; plain Ark must not inherit them")
}

func TestXRTokenManifestScopeOwnsAllFiveModels(t *testing.T) {
	scoped := tkServedModelsManifestPresetIDsForSelector(
		PlatformNewAPI,
		newapiconstant.ChannelTypeDoubaoVideo,
		newapiintegration.XRTokenBaseURL,
	)
	require.ElementsMatch(t, []string{
		"doubao-seedance-1-5-pro-251215",
		"doubao-seedance-2-0-260128",
		"doubao-seedance-2-0-fast-260128",
		"doubao-seedance-2-5-260628",
		"doubao-seedance-2.0-mini",
	}, scoped)
	require.Empty(t, tkServedModelsManifestPresetIDsByChannelType(
		newapiconstant.ChannelTypeDoubaoVideo,
	), "base_url-scoped XRToken models must not leak into the generic ch54 floor")
}

// TestAccountModelMappingFloorForOpsIncludesXRTokenOverride is the SSOT half of
// the fix. It is not enough that the admin preset surface knows about account
// 96: the COMPILED FLOOR must export an XRToken account_override too, because
// that floor is what `modelops activate` / `apply-accounts` diff live accounts
// against.
//
// Before this override existed the bundle carried no XRToken scope at all, so
// activation had nothing to add and refused with "bundle delta has no added or
// retargeted required model mappings" — i.e. the documented
// the declared modelops activation path could not actually run.
//
// Two assertions, both load-bearing:
//
//  1. All FIVE served SKUs are present. The generic ch54 floor is empty because
//     these rows are XRToken base_url-scoped; falling through would leave the
//     live account without a compiled serving floor.
//  2. Every target is IDENTITY. XRToken's `volcengine/` namespace belongs on
//     the wire (applied by the task adaptor), never in a mapping target: the
//     floor cannot represent a prefixed target, so storing one makes
//     apply-accounts see permanent `bad_targets` drift and rewrite it back.
func TestAccountModelMappingFloorForOpsIncludesXRTokenOverride(t *testing.T) {
	t.Parallel()
	floor, err := AccountModelMappingFloorForOps(context.Background(), "")
	require.NoError(t, err)
	require.NotEmpty(t, floor.AccountOverrides)

	var override *AccountModelMappingOverride
	for i := range floor.AccountOverrides {
		candidate := floor.AccountOverrides[i]
		if candidate.Platform == PlatformNewAPI &&
			candidate.ChannelType == newapiconstant.ChannelTypeDoubaoVideo &&
			candidate.BaseURL == newapiintegration.XRTokenBaseURL {
			override = &floor.AccountOverrides[i]
			break
		}
	}
	require.NotNil(t, override,
		"XRToken ch54 account override must be exported in the bundle floor, "+
			"otherwise modelops activate has no delta to apply")

	// (1) every served SKU, not just the ch54-indexed pair.
	wantModels := NewAPIModelMappingPresetIDsForAccount(
		xrTokenProbeAccount(newapiintegration.XRTokenBaseURL),
	)
	require.Len(t, wantModels, 5, "account 96 serves five seedance SKUs")
	for _, id := range wantModels {
		require.Contains(t, override.ModelMapping, id,
			"floor must export %q or apply-accounts prunes it off account 96", id)
	}
	require.Len(t, override.ModelMapping, len(wantModels))

	// (2) identity targets only — the vendor prefix lives in the adaptor.
	for key, target := range override.ModelMapping {
		require.Equal(t, key, target,
			"model_mapping target must be identity; the volcengine/ prefix is applied "+
				"on the wire by the XRToken task adaptor, and a prefixed target here "+
				"would be reverted by the next apply-accounts")
		require.NotContains(t, target, newapiintegration.XRTokenVideoVendorPrefix,
			"vendor prefix must never appear in a mapping target")
	}
}
