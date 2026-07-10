package service

import "testing"

func TestIsTkCuratedNewAPIModelListed(t *testing.T) {
	listed := firstMapKeyForTest(t, loadTkServedModelsManifestIDs())
	if !isTkCuratedNewAPIModelListed(listed) {
		t.Fatalf("manifest-listed model %q must be recognized", listed)
	}
	if isTkCuratedNewAPIModelListed("tk-not-in-served-models-manifest-zzz") {
		t.Fatal("unknown model must not be manifest-listed")
	}
}

func TestIsTkCuratedNewAPIModelDisplayed(t *testing.T) {
	loadTkServedModelsManifest()
	displayed := firstMapKeyForTest(t, tkServedModelsManifestDisplayIDs)
	if !isTkCuratedNewAPIModelDisplayed(displayed) {
		t.Fatalf("display=true manifest row %q must be public-display eligible", displayed)
	}
	if isTkCuratedNewAPIModelDisplayed("tk-not-in-served-models-manifest-zzz") {
		t.Fatal("unlisted models must not be public-display eligible")
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

func TestTkServedModelsManifestPresetIDsByChannelType(t *testing.T) {
	channelTypes := NewAPIManifestPresetChannelTypes()
	if len(channelTypes) == 0 {
		t.Fatal("manifest must expose at least one channel_type preset")
	}
	channelType := channelTypes[0]
	preset := tkServedModelsManifestPresetIDsByChannelType(channelType)
	if len(preset) == 0 {
		t.Fatalf("manifest channel_type %d must have presets", channelType)
	}
	if !containsString(preset, preset[0]) {
		t.Fatalf("manifest preset %q must be returned for channel_type %d", preset[0], channelType)
	}
	if tkServedModelsManifestPresetIDsByChannelType(999999) != nil {
		t.Fatal("unknown channel_type must return nil preset")
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
