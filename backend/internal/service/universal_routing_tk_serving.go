package service

import "context"

// 全能 Key 解析的「组已服务模型集」真值过滤。
//
// 背景:旧解析器只按 平台 + 模型名前缀 hint 盲选后端组,对「某组账号到底服不服务这个
// 具体模型」零可见 —— newapi 平台底下多个 vendor 组(deepseek/Qwen/volcengine/
// google-vertex/grok/…,多组是按 vendor 专属倍率计费的有意设计)里盲选必投错组 →
// 下游 `no available accounts`。本文件给解析器加一道「组是否真服务该模型」的判别,
// 复用 GatewayService.GetAvailableModels(与 /v1/models 同一真值)。
//
// 判别按组的服务集来源非对称分流(见 docs/approved/universal-key-routing.md):
//   - GetAvailableModels 返回非 nil(组有显式 model_mapping,含全部 newapi vendor 组)
//     → 精确成员判定 M ∈ served。把 deepseek/qwen/imagen/veo/seedance 落到声明了该
//     模型的组,排除别的 vendor 组。
//   - 返回 nil(native 空映射:anthropic/openai/gemini/antigravity 等单一 vendor 平台
//     的「空=透传」)→ 用模型平台 hint == 组平台 判别(单 vendor 平台无歧义,且能匹配
//     尚未进白名单的新模型)。
//   - 返回 nil 且组平台为 newapi(多 vendor)→ 不匹配(空映射在多 vendor 平台是配置缺失,
//     不能靠前缀 hint 撞;由写时校验 + ops 审计 + 路由层共同拦截)。

// availableModelsProvider 返回某组(按 platform)当前可调度账号 model_mapping 键的并集;
// 无任何账号声明映射时返回 nil(native/permissive)。由 GatewayService.GetAvailableModels
// 满足,经 APIKeyService 后期绑定(避免构造期 GatewayService 尚不存在的环;见 wire.go
// ProvideTKUniversalModelsProvider)。provider 为 nil(未接线/降级)时,解析器退回平台级
// 现状行为 —— 安全兜底,绝不黑洞 universal 流量。
type availableModelsProvider func(ctx context.Context, groupID *int64, platform string) []string

// groupServesModel 判定组 g 是否真服务模型 model(上述三分流)。provider 非 nil 由调用方保证。
func groupServesModel(ctx context.Context, provider availableModelsProvider, g Group, model string) bool {
	gid := g.ID
	served := provider(ctx, &gid, g.Platform)
	if served != nil {
		// 组有显式 model_mapping → 精确成员判定。
		if modelInServedSet(model, served, g.Platform) {
			return true
		}
		// Grok native OAuth: chat-only mapping entries must not hide the curated
		// grok-imagine media + probed chat catalog that fillAccountFallback advertises.
		if g.Platform == PlatformGrok && grokGroupServesNativeCatalogModel(model) {
			return true
		}
		return false
	}
	// served == nil:native/permissive(无账号声明映射,或 provider 取数失败)。
	if g.Platform == PlatformNewAPI {
		// 多 vendor 平台的空映射 = 配置缺失,不靠 hint 撞(防 openai 名/其它 vendor 误投)。
		return false
	}
	// 单 vendor 原生平台:模型平台 hint == 组平台。
	return universalModelPlatformHint(model) == g.Platform
}

// modelInServedSet 判定 model 是否在组的显式服务集里(精确 + 归一,与 IsModelSupported 同口径)。
func modelInServedSet(model string, served []string, platform string) bool {
	for _, m := range served {
		if m == model {
			return true
		}
	}
	if norm := normalizeRequestedModelForLookup(platform, model); norm != model {
		for _, m := range served {
			if m == norm {
				return true
			}
		}
	}
	return false
}
