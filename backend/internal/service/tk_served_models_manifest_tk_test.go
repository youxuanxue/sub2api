package service

import "testing"

func TestIsTkCuratedNewAPIModelListed(t *testing.T) {
	if !isTkCuratedNewAPIModelListed("deepseek-chat") {
		t.Fatal("deepseek-chat must be manifest-listed")
	}
	if isTkCuratedNewAPIModelListed("deepseek-v3-2-251201") {
		t.Fatal("deepseek-v3-2-251201 must not be manifest-listed")
	}
	if isTkCuratedNewAPIModelListed("glm-4-32b-0414-128k") {
		t.Fatal("glm-4-32b-0414-128k must not be manifest-listed after upstream 400 withdrawal")
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
