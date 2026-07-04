package service

import "testing"

func TestIsTkCuratedNewAPIModelListed(t *testing.T) {
	if !isTkCuratedNewAPIModelListed("deepseek-chat") {
		t.Fatal("deepseek-chat must be manifest-listed")
	}
	if !isTkCuratedNewAPIModelListed("glm-5.2") {
		t.Fatal("Qwen-served GLM models must remain manifest-listed")
	}
	if isTkCuratedNewAPIModelListed("glm-5-turbo") {
		t.Fatal("direct-only GLM models must not remain manifest-listed after GLM account/group removal")
	}
	if isTkCuratedNewAPIModelListed("deepseek-v3-2-251201") {
		t.Fatal("deepseek-v3-2-251201 must not be manifest-listed")
	}
	if isTkCuratedNewAPIModelListed("glm-4-32b-0414-128k") {
		t.Fatal("glm-4-32b-0414-128k must not be manifest-listed after upstream 400 withdrawal")
	}
}

func TestIsTkCuratedNewAPIModelDisplayed(t *testing.T) {
	if !isTkCuratedNewAPIModelDisplayed("deepseek-chat") {
		t.Fatal("display=true manifest rows must be public-display eligible")
	}
	if isTkCuratedNewAPIModelDisplayed("glm-5-turbo") {
		t.Fatal("unlisted direct-only GLM rows must stay hidden from public catalog")
	}
	if isTkCuratedNewAPIModelDisplayed("deepseek-v3-2-251201") {
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
	deepseek := tkServedModelsManifestPresetIDsByChannelType(43)
	if len(deepseek) == 0 {
		t.Fatal("deepseek channel_type 43 must have manifest presets")
	}
	if !containsString(deepseek, "deepseek-chat") {
		t.Fatal("deepseek-chat must be in ch43 preset")
	}
	if tkServedModelsManifestPresetIDsByChannelType(25) != nil {
		t.Fatal("unprobed channel_type 25 must return nil preset")
	}
	if tkServedModelsManifestPresetIDsByChannelType(26) != nil {
		t.Fatal("removed ZhipuV4 direct GLM channel_type 26 must return nil preset")
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
