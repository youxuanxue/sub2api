//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

// 档位 wire 一律不上客户可见面，且这条不许依赖「它恰好没有定价行」。
//
// 2026-10-24：此前拦住它们的只是一个巧合 —— 公开目录投影要求有定价行，而多数
// 档位 wire 没有。但 overlay 里 gemini-3.1-pro-high / -low 是有定价行的，谁要
// 给某个档位补一行价，它就会直接出现在客户列表里，而没有任何测试会红。
//
// 档位选择的唯一入口是参数（output_config.effort / thinking.type ->
// generationConfig.thinkingConfig.thinkingLevel，见 gemini_compat_thinking.go），
// 不是模型名。官方 Gemini API 也是这么做的：thinking_level 是参数。
//
// 还有一条更硬的理由不暴露 wire ID：它的后缀与上游 app 展示的档位名并不一致。
// overlay 记着 gemini-3.5-flash-low 在 Antigravity app 里显示为
// "Gemini 3.5 Flash (Medium)"，gemini-pro-agent 显示为 "Gemini 3.1 Pro (High)"。
// 把这种 ID 摆到客户面前，客户按字面选 -low 实际拿到的是 Medium 并按 Medium 计费。
func TestAntigravityThinkingWireModelsNeverPublic(t *testing.T) {
	t.Parallel()
	wires := domain.AntigravityThinkingWireFloorEntries()
	require.NotEmpty(t, wires, "档位 wire 集合为空则本测试失去意义")

	for id := range wires {
		require.True(t, domain.IsAntigravityThinkingWireModel(id), "wire %s 必须被判定为档位 wire", id)
		for _, platform := range []string{PlatformAntigravity, PlatformGemini} {
			require.False(t, isPublicCatalogModelSupported(platform, id),
				"档位 wire %s 不得出现在 %s 的公开目录", id, platform)
			require.NotContains(t, supportedCatalogModelIDsForPlatform(platform), id,
				"档位 wire %s 不得出现在 %s 的客户可见列表", id, platform)
		}
	}

	// 裸名仍然要在，否则客户没法请求这个模型族。
	require.True(t, isPublicCatalogModelSupported(PlatformAntigravity, "gemini-3.8-flash"))
}
