package domain

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 重构前各处手写的平台列表，作为平台清单派生结果的等价基准；重构后新登记的平台
// （Command Code、Cline）按同类平台（OpenCode）的位置补入。
var (
	legacyDisplayOrder = []string{
		PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformAntigravity,
		// TK: newapi / kiro 是 TokenKey 登记的第五、第六平台，位置在 antigravity 之后。
		PlatformNewAPI, PlatformKiro,
		PlatformGrok,
		PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformTypeSafe,
		PlatformCommandCode,
		PlatformCline,
	}
	legacyCompositePrecedence = []string{
		PlatformAnthropic, PlatformGemini, PlatformOpenAI, PlatformAntigravity, PlatformGrok,
		PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo, PlatformTypeSafe,
		PlatformCommandCode,
		PlatformCline,
	}
	legacyOpenAIGateway = []string{
		// TK: newapi 走 OpenAI 网关族调度；kiro 刻意不是 OpenAI 兼容成员。
		PlatformOpenAI, PlatformNewAPI, PlatformGrok, PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax, PlatformOpenCodeGo,
		PlatformCommandCode,
		PlatformCline,
	}
	legacyCNProviders    = []string{PlatformKimi, PlatformZhipu, PlatformDeepseek, PlatformMiniMax}
	legacyLiteLLMByPlatf = map[string]string{
		PlatformAnthropic: "anthropic", PlatformOpenAI: "openai", PlatformGemini: "gemini",
		PlatformAntigravity: "anthropic", PlatformGrok: "xai", PlatformKimi: "moonshot",
		PlatformZhipu: "zhipu", PlatformDeepseek: "deepseek", PlatformMiniMax: "minimax",
		PlatformOpenCodeGo: "opencode-go", PlatformTypeSafe: "typesafe", PlatformCommandCode: "", PlatformCline: "",
		// TK: newapi 定价走自有 curated registry，kiro 无 LiteLLM 目录同步。
		PlatformNewAPI: "", PlatformKiro: "",
	}
)

func TestPlatformListMatchesLegacyLists(t *testing.T) {
	require.Equal(t, legacyDisplayOrder, ConcretePlatformIDs())
	require.Equal(t, legacyCompositePrecedence, CompositePrecedencePlatformIDs())
	require.Equal(t, legacyOpenAIGateway, PlatformIDsWhere(func(spec PlatformSpec) bool {
		return spec.Gateway == PlatformGatewayOpenAI
	}))
	require.Equal(t, legacyCNProviders, PlatformIDsWhere(func(spec PlatformSpec) bool { return spec.CNProvider }))
	for platform, provider := range legacyLiteLLMByPlatf {
		spec, ok := LookupPlatform(platform)
		require.True(t, ok, platform)
		require.Equal(t, provider, spec.LiteLLMProvider, platform)
	}
}

func TestPlatformListPredicates(t *testing.T) {
	for _, platform := range legacyDisplayOrder {
		require.True(t, IsConcretePlatform(platform), platform)
		require.True(t, IsGroupPlatform(platform), platform)
		spec, ok := LookupPlatform(platform)
		require.True(t, ok, platform)
		require.NotEmpty(t, spec.DisplayName, platform)
		require.NotEmpty(t, spec.Gateway, platform)
	}
	require.False(t, IsConcretePlatform(PlatformComposite))
	require.True(t, IsGroupPlatform(PlatformComposite))
	for _, invalid := range []string{"", "moonshot", "Kimi", "openai ", "glm", "bogus"} {
		require.False(t, IsConcretePlatform(invalid), invalid)
		require.False(t, IsGroupPlatform(invalid), invalid)
		require.False(t, UsesOpenAIGateway(invalid), invalid)
	}
}

func TestPlatformsReturnsCopy(t *testing.T) {
	platforms := Platforms()
	platforms[0].ID = "mutated"
	require.Equal(t, PlatformAnthropic, Platforms()[0].ID)
}
