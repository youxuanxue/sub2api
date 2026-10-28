package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Plan supply channels (VolcEngine Agent Plan / Ali Token Plan) must not replace
// OEM vendor attribution on the public pricing page. Vendor comes from the
// pricing overlay litellm_provider, not from the schedule channel.
func TestPublicCatalog_PlanSuppliedModelsKeepOEMVendor(t *testing.T) {
	t.Parallel()
	resp := FilterPublicCatalogToServable((&PricingCatalogService{}).BuildPublicCatalog(context.Background()))
	require.NotNil(t, resp)
	byID := make(map[string]string, len(resp.Data))
	for _, m := range resp.Data {
		byID[m.ModelID] = m.Vendor
	}

	cases := map[string]string{
		"deepseek-v4-pro":     "deepseek",
		"deepseek-v4.1-flash": "deepseek",
		"glm-5.3":             "zhipu",
		"kimi-k3":             "moonshot",
		"kimi-k3.1-pro":       "moonshot",
		"qwen3.7-plus":        "dashscope",
		"doubao-seed-2.1-pro": "volcengine", // ByteDance OEM / Ark native
		"minimax-m3":          "minimax",
	}
	for id, want := range cases {
		got, ok := byID[id]
		require.True(t, ok, "%s must appear on public catalog (display+recommended)", id)
		assert.Equal(t, want, got, "%s must show OEM vendor, not supply-channel vendor", id)
	}
	for _, hidden := range []string{"glm-5.2", "deepseek-v4-flash-0731"} {
		_, ok := byID[hidden]
		require.False(t, ok, "%s stays priced and mapped, and is absent from the public catalog", hidden)
	}
}
