//go:build unit

package service

import (
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// XRToken account 96 is a ch54 (DoubaoVideo) ARK-compatible reseller. Its five
// served seedance SKUs are split across two manifest channel_type indexes: the
// two XRToken-only SKUs are indexed under ch54, while the three shared with the
// VolcEngine Ark pool stay indexed under ch45 (the manifest forbids duplicate
// model_id, and account 7 owns that index). The admin preset surface must
// therefore merge the ch54 scoped lookup with xrtokenSharedManifestModelIDs —
// exactly the mechanism qianfanSharedManifestModelIDs provides for account 90.
//
// Without the merge the admin model dropdown silently shows only 2 of 5 models,
// which is how a provisioning step quietly drops the shared SKUs.
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

	// Derived from the manifest, NOT a hand-copied list: the shared rows plus the
	// ch54-scoped rows are the definition of "what account 96 serves".
	want := mergeSortedManifestModelIDs(
		tkServedModelsManifestPresetIDsByChannelType(newapiconstant.ChannelTypeDoubaoVideo),
		xrtokenSharedManifestModelIDs,
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

	// Every shared SKU must be present: these are the ones a ch54-only lookup
	// misses, so assert them explicitly rather than trusting the count.
	for _, id := range xrtokenSharedManifestModelIDs {
		found := false
		for _, g := range got {
			if g == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("shared model %q missing from XRToken preset %v", id, got)
		}
	}

	// display projection follows the same merge (all five rows are display=true)
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
	for _, shared := range xrtokenSharedManifestModelIDs {
		for _, g := range got {
			if g == shared {
				t.Errorf("plain Ark ch54 preset leaked XRToken shared model %q: %v", shared, got)
			}
		}
	}
}
