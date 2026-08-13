package service

import (
	_ "embed"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Embed the manifest independently from the production loader so the test
// derives its expectations from the declarative owner, not from a projection
// produced by the code under test.
//
//go:embed tk_served_models.json
var tkServedModelsOwnerRawForTest []byte

type tkServedModelsOwnerEntryForTest struct {
	Platform      string                            `json:"platform"`
	ModelID       string                            `json:"model_id"`
	ChannelType   int                               `json:"channel_type"`
	AccountScope  *tkServedModelsOwnerScopeForTest  `json:"account_scope"`
	AccountScopes []tkServedModelsOwnerScopeForTest `json:"account_scopes"`
	Display       bool                              `json:"display"`
	ServedOn      []string                          `json:"served_on"`
}

type tkServedModelsOwnerScopeForTest struct {
	Platform    string `json:"platform"`
	ChannelType int    `json:"channel_type"`
	BaseURL     string `json:"base_url"`
}

type tkServedModelsOwnerProjectionForTest struct {
	listedIDs           map[string]struct{}
	displayIDs          map[string]struct{}
	IDsByChannel        map[int][]string
	displayIDsByChannel map[int][]string
	IDsByScope          map[string][]string
	displayIDsByScope   map[string][]string
	channelTypes        []int
}

func TestTkServedModelsManifestProjectionsMatchRawOwner(t *testing.T) {
	want := loadTkServedModelsOwnerProjectionForTest(t)
	loadTkServedModelsManifest()

	requireServedManifestProjectionEqualForTest(t, "listed IDs", want.listedIDs, tkServedModelsManifestIDs)
	requireServedManifestProjectionEqualForTest(t, "display IDs", want.displayIDs, tkServedModelsManifestDisplayIDs)
	requireServedManifestProjectionEqualForTest(t, "IDs by channel", want.IDsByChannel, tkServedModelsManifestIDsByChannelType)
	requireServedManifestProjectionEqualForTest(t, "display IDs by channel", want.displayIDsByChannel, tkServedModelsManifestDisplayIDsByChannelType)
	requireServedManifestProjectionEqualForTest(t, "IDs by scope", want.IDsByScope, tkServedModelsManifestIDsByScope)
	requireServedManifestProjectionEqualForTest(t, "display IDs by scope", want.displayIDsByScope, tkServedModelsManifestDisplayIDsByScope)
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
	for scope, ids := range want.IDsByScope {
		parts := strings.SplitN(scope, ":", 3)
		if len(parts) != 3 {
			t.Fatalf("invalid test scope key %q", scope)
		}
		channelType, err := strconv.Atoi(parts[1])
		if err != nil {
			t.Fatalf("invalid test scope channel_type %q: %v", scope, err)
		}
		requireServedManifestProjectionEqualForTest(t,
			"scope preset", ids, tkServedModelsManifestPresetIDsForSelector(parts[0], channelType, parts[2]))
		requireServedManifestProjectionEqualForTest(t,
			"scope display preset", want.displayIDsByScope[scope], tkServedModelsManifestDisplayPresetIDsForSelector(parts[0], channelType, parts[2]))
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
	for _, v := range []string{"volcengine", "deepseek", "dashscope", "zhipu", "newapi", "wenxin", "qianfan", "baidu"} {
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
		IDsByScope:          make(map[string][]string),
		displayIDsByScope:   make(map[string][]string),
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
		if entry.Display {
			out.displayIDs[entry.ModelID] = struct{}{}
		}
		for _, scope := range manifestScopeKeysForTest(entry) {
			out.IDsByScope[scope] = append(out.IDsByScope[scope], entry.ModelID)
			if entry.Display {
				out.displayIDsByScope[scope] = append(out.displayIDsByScope[scope], entry.ModelID)
			}
		}
		if manifestEntryIsAccountScopedOnlyForTest(entry) {
			continue
		}
		out.IDsByChannel[entry.ChannelType] = append(out.IDsByChannel[entry.ChannelType], entry.ModelID)
		if entry.Display {
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
	for scope, ids := range out.IDsByScope {
		sort.Strings(ids)
		out.IDsByScope[scope] = ids
	}
	for scope, ids := range out.displayIDsByScope {
		sort.Strings(ids)
		out.displayIDsByScope[scope] = ids
	}
	sort.Ints(out.channelTypes)
	return out
}

func manifestEntryIsAccountScopedOnlyForTest(entry tkServedModelsOwnerEntryForTest) bool {
	return entry.AccountScope != nil &&
		entry.ChannelType == entry.AccountScope.ChannelType &&
		manifestScopeKeyForTest(*entry.AccountScope) != ""
}

func manifestScopeKeysForTest(entry tkServedModelsOwnerEntryForTest) []string {
	scopes := make([]tkServedModelsOwnerScopeForTest, 0, len(entry.AccountScopes)+1)
	if entry.AccountScope != nil {
		scopes = append(scopes, *entry.AccountScope)
	}
	scopes = append(scopes, entry.AccountScopes...)
	seen := make(map[string]struct{}, len(scopes))
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		key := manifestScopeKeyForTest(scope)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func manifestScopeKeyForTest(scope tkServedModelsOwnerScopeForTest) string {
	platform := strings.ToLower(strings.TrimSpace(scope.Platform))
	baseURL := strings.TrimRight(strings.ToLower(strings.TrimSpace(scope.BaseURL)), "/")
	if platform != "newapi" {
		return ""
	}
	valid := (scope.ChannelType == 45 && baseURL == "https://ark.cn-beijing.volces.com/api/plan/v3") ||
		(scope.ChannelType == 46 && baseURL == "https://qianfan.baidubce.com") ||
		(scope.ChannelType == 54 && baseURL == "https://api.xrtoken.net")
	if !valid {
		return ""
	}
	return platform + ":" + strconv.Itoa(scope.ChannelType) + ":" + baseURL
}

func requireServedManifestProjectionEqualForTest(t *testing.T, name string, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s mismatch\nwant: %#v\n got: %#v", name, want, got)
	}
}
