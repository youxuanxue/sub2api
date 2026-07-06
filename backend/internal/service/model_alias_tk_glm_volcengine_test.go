//go:build unit

package service

import "testing"

func TestNormalizeGLMVolcengineDatedModelID(t *testing.T) {
	for in, want := range map[string]string{
		"glm-4-7-251222": "glm-4.7",
		"GLM-4-7-251222": "glm-4.7",
		" glm-5-2-260115 ": "glm-5.2",
		"glm-4.7":           "",
		"glm-4.7-flash":     "",
		"glm-4-32b-0414-128k": "",
		"doubao-seed-1-6-250615": "",
	} {
		if got := normalizeGLMVolcengineDatedModelID(in); got != want {
			t.Errorf("normalizeGLMVolcengineDatedModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccount_GLMDatedVolcengineAliasRoutesViaDashScopeMapping(t *testing.T) {
	account := &Account{
		Platform: PlatformNewAPI,
		Credentials: map[string]any{
			"model_mapping": map[string]any{
				"glm-4.7": "glm-4.7",
			},
		},
	}
	if !account.IsModelSupported("glm-4-7-251222") {
		t.Fatal("dated VolcEngine GLM id must match DashScope glm-4.7 mapping")
	}
	if got := account.GetMappedModel("glm-4-7-251222"); got != "glm-4.7" {
		t.Fatalf("GetMappedModel(glm-4-7-251222) = %q, want glm-4.7", got)
	}
}
