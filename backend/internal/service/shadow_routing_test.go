package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDefaultSparkShadowModelMapping(t *testing.T) {
	mapping := defaultSparkShadowModelMapping()

	require.Len(t, mapping, len(sparkModelVariants()), "spark 默认映射跟随 codexModelMap 中归一到 spark 的变体")
	for _, model := range sparkModelVariants() {
		require.Equal(t, model, mapping[model], "恒等映射：每个 spark 变体映射到自身")
	}
}

func TestSparkModelVariantsDerivedFromAliases(t *testing.T) {
	got := sparkModelVariants()
	for _, want := range []string{
		"gpt-5.3-codex-spark",
		"gpt-5.3-codex-spark-low",
		"gpt-5.3-codex-spark-medium",
		"gpt-5.3-codex-spark-high",
		"gpt-5.3-codex-spark-xhigh",
	} {
		require.Contains(t, got, want, "spark 变体集合必须从 codexModelMap 派生")
	}
	require.NotContains(t, got, "gpt-5.3-codex", "legacy codex id is deprecated, not a spark shadow variant")
	require.NotContains(t, got, "gpt-5-codex", "legacy codex id is deprecated, not a spark shadow variant")
}
