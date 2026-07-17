package service

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// Embed the manifest independently from the production loader so the test
// derives its expectations from the declarative owner, not from a projection
// produced by the code under test.
//
//go:embed tk_served_models.json
var tkServedModelsOwnerRawForTest []byte

type tkServedModelsOwnerEntryForTest struct {
	ModelID     string `json:"model_id"`
	ChannelType int    `json:"channel_type"`
	Display     bool   `json:"display"`
}

type tkServedModelsOwnerProjectionForTest struct {
	listedIDs           map[string]struct{}
	displayIDs          map[string]struct{}
	IDsByChannel        map[int][]string
	displayIDsByChannel map[int][]string
	channelTypes        []int
}

func TestTkServedModelsManifestProjectionsMatchRawOwner(t *testing.T) {
	want := loadTkServedModelsOwnerProjectionForTest(t)
	loadTkServedModelsManifest()

	requireServedManifestProjectionEqualForTest(t, "listed IDs", want.listedIDs, tkServedModelsManifestIDs)
	requireServedManifestProjectionEqualForTest(t, "display IDs", want.displayIDs, tkServedModelsManifestDisplayIDs)
	requireServedManifestProjectionEqualForTest(t, "IDs by channel", want.IDsByChannel, tkServedModelsManifestIDsByChannelType)
	requireServedManifestProjectionEqualForTest(t, "display IDs by channel", want.displayIDsByChannel, tkServedModelsManifestDisplayIDsByChannelType)
	requireServedManifestProjectionEqualForTest(t, "channel types", want.channelTypes, NewAPIManifestPresetChannelTypes())

	for modelID := range want.listedIDs {
		if !isTkCuratedNewAPIModelListed(modelID) {
			t.Errorf("raw-owner model %q must be listed", modelID)
		}
		_, wantDisplayed := want.displayIDs[modelID]
		if got := isTkCuratedNewAPIModelDisplayed(modelID); got != wantDisplayed {
			t.Errorf("display projection for %q = %t, want %t from raw owner", modelID, got, wantDisplayed)
		}
	}

	for _, channelType := range want.channelTypes {
		requireServedManifestProjectionEqualForTest(t,
			"channel preset", want.IDsByChannel[channelType], tkServedModelsManifestPresetIDsByChannelType(channelType))
		requireServedManifestProjectionEqualForTest(t,
			"channel display preset", want.displayIDsByChannel[channelType], tkServedModelsManifestDisplayPresetIDsByChannelType(channelType))
	}

	for _, modelID := range []string{
		"tk-not-in-served-models-manifest-zzz", // unknown
		"deepseek-v3-2-251201",                 // priced residue, never served
		"glm-4-7-251222",                       // retired VolcEngine duplicate
		"glm-4-32b-0414-128k",                  // withdrawn GLM SKU
	} {
		if isTkCuratedNewAPIModelListed(modelID) {
			t.Errorf("unknown/retired model %q must not be manifest-listed", modelID)
		}
		if isTkCuratedNewAPIModelDisplayed(modelID) {
			t.Errorf("unknown/retired model %q must not be public-display eligible", modelID)
		}
	}

	const unknownChannelType = 999999
	if _, exists := want.IDsByChannel[unknownChannelType]; exists {
		t.Fatalf("test's unknown channel_type %d unexpectedly exists in the raw owner", unknownChannelType)
	}
	if got := tkServedModelsManifestPresetIDsByChannelType(unknownChannelType); got != nil {
		t.Errorf("unknown channel_type preset = %v, want nil", got)
	}
	if got := tkServedModelsManifestDisplayPresetIDsByChannelType(unknownChannelType); got != nil {
		t.Errorf("unknown channel_type display preset = %v, want nil", got)
	}
}

func TestIsNewAPILongTailCatalogVendor(t *testing.T) {
	for _, v := range []string{"volcengine", "deepseek", "dashscope", "zhipu", "newapi"} {
		if !isNewAPILongTailCatalogVendor(v) {
			t.Fatalf("vendor %q must be newapi long-tail", v)
		}
	}
	if isNewAPILongTailCatalogVendor("anthropic") {
		t.Fatal("anthropic must not be newapi long-tail")
	}
}

func loadTkServedModelsOwnerProjectionForTest(t *testing.T) tkServedModelsOwnerProjectionForTest {
	t.Helper()
	var doc struct {
		Entries map[string]tkServedModelsOwnerEntryForTest `json:"entries"`
	}
	if err := json.Unmarshal(tkServedModelsOwnerRawForTest, &doc); err != nil {
		t.Fatalf("parse raw served-models owner: %v", err)
	}
	if len(doc.Entries) == 0 {
		t.Fatal("raw served-models owner must contain entries")
	}

	out := tkServedModelsOwnerProjectionForTest{
		listedIDs:           make(map[string]struct{}, len(doc.Entries)),
		displayIDs:          make(map[string]struct{}, len(doc.Entries)),
		IDsByChannel:        make(map[int][]string),
		displayIDsByChannel: make(map[int][]string),
	}
	for key, entry := range doc.Entries {
		if entry.ModelID == "" {
			t.Fatalf("raw owner entry %q has an empty model_id", key)
		}
		if entry.ChannelType <= 0 {
			t.Fatalf("raw owner entry %q has invalid channel_type %d", key, entry.ChannelType)
		}
		if _, duplicate := out.listedIDs[entry.ModelID]; duplicate {
			t.Fatalf("raw owner declares model_id %q more than once", entry.ModelID)
		}
		out.listedIDs[entry.ModelID] = struct{}{}
		out.IDsByChannel[entry.ChannelType] = append(out.IDsByChannel[entry.ChannelType], entry.ModelID)
		if entry.Display {
			out.displayIDs[entry.ModelID] = struct{}{}
			out.displayIDsByChannel[entry.ChannelType] = append(out.displayIDsByChannel[entry.ChannelType], entry.ModelID)
		}
	}
	for channelType, ids := range out.IDsByChannel {
		sort.Strings(ids)
		out.IDsByChannel[channelType] = ids
		out.channelTypes = append(out.channelTypes, channelType)
	}
	for channelType, ids := range out.displayIDsByChannel {
		sort.Strings(ids)
		out.displayIDsByChannel[channelType] = ids
	}
	sort.Ints(out.channelTypes)
	return out
}

func requireServedManifestProjectionEqualForTest(t *testing.T, name string, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s mismatch\nwant: %#v\n got: %#v", name, want, got)
	}
}
