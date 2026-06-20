//go:build unit

package service

import (
	"context"
	"testing"
)

// servedProvider 构造一个按 group id 返回显式服务集的 provider stub。
// 缺席的 id 返回 nil(= native/无声明映射)。
func servedProvider(byGroup map[int64][]string) availableModelsProvider {
	return func(_ context.Context, gid *int64, _ string) []string {
		if gid == nil {
			return nil
		}
		return byGroup[*gid] // 缺席 → nil(native)
	}
}

// 核心:newapi 平台多 vendor 组,按「组已服务模型集」精确落到对的组,而非盲选 tiebreaker。
func TestResolve_NewapiPicksGroupThatServesModel(t *testing.T) {
	ctx := context.Background()
	// 两个 newapi vendor 组:deepseek(gid=11)、Qwen(gid=18)。盲选会按 tiebreaker 取 11。
	span := []Group{
		grp(11, PlatformNewAPI, 10, false),
		grp(18, PlatformNewAPI, 20, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		11: {"deepseek-chat", "deepseek-reasoner"},
		18: {"qwen-max", "qwen-plus"},
	}))
	key := universalKey(1)

	g, err := r.Resolve(ctx, key, ShapeOpenAIChat, "qwen-max", "")
	if err != nil || g == nil || g.ID != 18 {
		t.Fatalf("qwen-max 应落 Qwen 组 gid=18, got=%v err=%v", g, err)
	}
	g, err = r.Resolve(ctx, key, ShapeOpenAIChat, "deepseek-chat", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("deepseek-chat 应落 deepseek 组 gid=11, got=%v err=%v", g, err)
	}
}

// openai 中继组(native 空映射)不得撞 newapi vendor 模型;但其自身平台模型仍命中。
func TestResolve_NativeEmptyMappingDoesNotMatchOtherVendor(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),    // 中继组,nil served
		grp(11, PlatformNewAPI, 10, false),  // deepseek
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		11: {"deepseek-chat"},
		// gid=2 缺席 → nil(native openai)
	}))
	key := universalKey(1)

	// deepseek-chat 必须落 newapi(11),不能被 openai 空映射组(2)撞走。
	g, err := r.Resolve(ctx, key, ShapeOpenAIChat, "deepseek-chat", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("deepseek-chat 应落 gid=11, got=%v err=%v", g, err)
	}
	// gpt-5 命中 openai 空映射组(native hint==platform)。
	g, err = r.Resolve(ctx, key, ShapeOpenAIChat, "gpt-5", "")
	if err != nil || g == nil || g.ID != 2 {
		t.Fatalf("gpt-5 应落 openai gid=2, got=%v err=%v", g, err)
	}
}

// imagen 走 newapi google-vertex 组(显式声明),不落 openai 组。
func TestResolve_ImagenPicksVertexNewapiGroup(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),    // openai 中继,nil served(不支持 imagen)
		grp(16, PlatformNewAPI, 10, false),  // google-vertex
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		16: {"imagen-4.0-fast-generate-001", "veo-3.1-generate-001"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIImages, "imagen-4.0-fast-generate-001", "")
	if err != nil || g == nil || g.ID != 16 {
		t.Fatalf("imagen 应落 vertex newapi 组 gid=16, got=%v err=%v", g, err)
	}
}

// 回归:原生 anthropic 组(nil served)claude-* 仍正常解析(按 tiebreaker)。
func TestResolve_NativeAnthropicRegression(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(1, PlatformAnthropic, 5, false),
		grp(15, PlatformAnthropic, 10, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{})) // 全 native(nil)
	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "claude-sonnet-4-6", "")
	if err != nil || g == nil || g.ID != 1 {
		t.Fatalf("claude 应落 anthropic gid=1(tiebreaker), got=%v err=%v", g, err)
	}
}

// 安全兜底:provider 未接线(nil)→ 退回平台级现状(不因新逻辑改变行为)。
func TestResolve_NilProviderFallsBackToPlatformLevel(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(11, PlatformNewAPI, 20, false),
		grp(18, PlatformNewAPI, 10, false), // 更小 sort_order → tiebreaker 胜出
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	// 不设 provider。
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "deepseek-chat", "")
	if err != nil || g == nil || g.ID != 18 {
		t.Fatalf("无 provider 应退回平台级 tiebreaker(gid=18), got=%v err=%v", g, err)
	}
}

// 安全兜底:无组声明该模型(served 全空)→ 不硬 403,退回 eligible 由下游裁决。
func TestResolve_NoServingGroupFallsBackNotHard403(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(11, PlatformNewAPI, 10, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		11: {"deepseek-chat"}, // 不含 gpt-image
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "some-unmapped-model", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("无组声明该模型应退回 eligible(gid=11),由下游诚实拒绝;got=%v err=%v", g, err)
	}
}

func TestValidateNewapiAccountModelMapping(t *testing.T) {
	nonEmpty := map[string]any{"model_mapping": map[string]any{"deepseek-chat": "deepseek-chat"}}
	empty := map[string]any{"model_mapping": map[string]any{}}
	absent := map[string]any{"api_key": "sk-x"}

	if err := validateNewapiAccountModelMapping(PlatformNewAPI, nonEmpty); err != nil {
		t.Fatalf("newapi 非空映射应通过, got %v", err)
	}
	if err := validateNewapiAccountModelMapping(PlatformNewAPI, empty); err == nil {
		t.Fatal("newapi 空映射应拒绝")
	}
	if err := validateNewapiAccountModelMapping(PlatformNewAPI, absent); err == nil {
		t.Fatal("newapi 缺映射应拒绝")
	}
	// 原生平台空映射放行(透传)。
	if err := validateNewapiAccountModelMapping(PlatformAnthropic, absent); err != nil {
		t.Fatalf("anthropic 空映射应放行, got %v", err)
	}
	if err := validateNewapiAccountModelMapping(PlatformOpenAI, empty); err != nil {
		t.Fatalf("openai 空映射应放行, got %v", err)
	}
}
