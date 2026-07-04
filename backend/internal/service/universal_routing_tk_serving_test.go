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

func supportProvider(byGroup map[int64]bool) groupModelSupportProvider {
	return func(_ context.Context, gid *int64, _ string, _ string, _ UniversalShape) (bool, bool) {
		if gid == nil {
			return false, false
		}
		serves, ok := byGroup[*gid]
		return serves, ok
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

func TestResolve_UsesDirectSchedulerModelSupportBeforeServedSet(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),
		grp(18, PlatformNewAPI, 10, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	// /v1/models-style served set is lossy for mixed groups: it only sees the
	// mapped account and misses another OpenAI passthrough account that direct
	// scheduling would accept. The direct-parity provider must win.
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		2:  {"gpt-5.5"},
		18: {"qwen-max"},
	}))
	r.SetModelSupportProvider(supportProvider(map[int64]bool{
		2:  true,
		18: false,
	}))
	g, err := r.Resolve(ctx, universalKey(16), ShapeOpenAIChat, "gpt-5.4-mini", "")
	if err != nil || g == nil || g.ID != 2 {
		t.Fatalf("direct-parity provider 应使 gpt-5.4-mini 落 openai passthrough 组 gid=2, got=%v err=%v", g, err)
	}
}

func TestResolve_ModelSupportProviderUnknownFallsBackToServedSet(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),
		grp(18, PlatformNewAPI, 10, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		2:  {"gpt-5.4-mini"},
		18: {"qwen-max"},
	}))
	r.SetModelSupportProvider(func(context.Context, *int64, string, string, UniversalShape) (bool, bool) {
		return false, false
	})
	g, err := r.Resolve(ctx, universalKey(16), ShapeOpenAIChat, "gpt5.4-mini", "")
	if err != nil || g == nil || g.ID != 2 {
		t.Fatalf("support provider unknown 时应 fallback 到 served-set alias 匹配 gid=2, got=%v err=%v", g, err)
	}
}

func TestUniversalGroupSupportsModel_OpenAICompatMatchesDirectScheduler(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)
	svc := &GatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:       1,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"openai_passthrough": true},
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gpt-5.5": "gpt-5.5"},
				},
			},
		}},
	}

	serves, known := svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformOpenAI, "gpt-5.4-mini")
	if !known || serves {
		t.Fatalf("openai compat support must follow direct scheduler mapping check, got serves=%v known=%v", serves, known)
	}
	serves, known = svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformOpenAI, "gpt5.5")
	if !known || !serves {
		t.Fatalf("openai compat alias should match mapped model, got serves=%v known=%v", serves, known)
	}
}

func TestUniversalGroupSupportsModel_OpenAIPassthroughRejectsGemini(t *testing.T) {
	ctx := context.Background()
	groupID := int64(2)
	svc := &GatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{{
			ID:       1,
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Extra:    map[string]any{"openai_passthrough": true},
		}}},
	}
	serves, known := svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformOpenAI, "gemini-2.5-flash")
	if !known || serves {
		t.Fatalf("openai passthrough must not claim gemini models, got serves=%v known=%v", serves, known)
	}
	serves, known = svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformOpenAI, "gpt-5.4-mini")
	if !known || !serves {
		t.Fatalf("openai passthrough should still claim gpt models, got serves=%v known=%v", serves, known)
	}
}

func TestUniversalGroupSupportsModel_NewapiRequiresCompatPoolMember(t *testing.T) {
	ctx := context.Background()
	groupID := int64(18)
	modelMapping := map[string]any{"qwen-max": "qwen-max"}

	t.Run("channel type zero rejected", func(t *testing.T) {
		svc := &GatewayService{
			accountRepo: stubOpenAIAccountRepo{accounts: []Account{
				{
					ID:          1,
					Platform:    PlatformNewAPI,
					ChannelType: 0,
					Credentials: map[string]any{"model_mapping": modelMapping},
				},
			}},
		}
		serves, known := svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformNewAPI, "qwen-max")
		if !known || serves {
			t.Fatalf("newapi channel_type=0 must not serve via universal, got serves=%v known=%v", serves, known)
		}
	})

	t.Run("positive channel type accepted", func(t *testing.T) {
		svc := &GatewayService{
			accountRepo: stubOpenAIAccountRepo{accounts: []Account{
				{
					ID:          2,
					Platform:    PlatformNewAPI,
					ChannelType: 45,
					Credentials: map[string]any{"model_mapping": modelMapping},
				},
			}},
		}
		serves, known := svc.UniversalGroupSupportsModel(ctx, &groupID, PlatformNewAPI, "qwen-max")
		if !known || !serves {
			t.Fatalf("newapi channel_type>0 should serve mapped model, got serves=%v known=%v", serves, known)
		}
	})
}

func TestResolve_VeoVideoRequiresVideoCapableVertexGroup(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 0, false),
		grp(16, PlatformNewAPI, 60, false),
	}
	veoMapping := map[string]any{"veo-3.1-generate-001": "veo-3.1-generate-001"}
	svc := &GatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:          63,
				GroupIDs:    []int64{2},
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				ChannelType: 0,
			},
			{
				ID:          47,
				GroupIDs:    []int64{16},
				Platform:    PlatformNewAPI,
				Type:        AccountTypeServiceAccount,
				Status:      StatusActive,
				Schedulable: true,
				ChannelType: 41,
				Credentials: map[string]any{"model_mapping": veoMapping},
			},
		}}},
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetModelSupportProvider(svc.UniversalGroupSupportsRequest)

	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIVideo, "veo-3.1-generate-001", "")
	if err != nil || g == nil || g.ID != 16 {
		t.Fatalf("veo video universal must skip openai channel_type=0 and pick vertex gid=16, got=%v err=%v", g, err)
	}
}

func TestModelInServedSet_UsesDirectMappingAliases(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		model    string
		served   []string
	}{
		{
			name:     "openai gpt5 spelling",
			platform: PlatformOpenAI,
			model:    "gpt5.4-mini",
			served:   []string{"gpt-5.4-mini"},
		},
		{
			name:     "openai provider prefix and compact spelling",
			platform: PlatformOpenAI,
			model:    "openai/gpt 5.4mini",
			served:   []string{"gpt-5.4-mini"},
		},
		{
			name:     "wildcard mapping",
			platform: PlatformOpenAI,
			model:    "gpt-5.4-mini-20260702",
			served:   []string{"gpt-5.4-mini*"},
		},
		{
			name:     "gemini customtools alias",
			platform: PlatformGemini,
			model:    "gemini-3.1-pro-preview-customtools",
			served:   []string{"gemini-3.1-pro-preview"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !modelInServedSet(tc.model, tc.served, tc.platform) {
				t.Fatalf("modelInServedSet(%q, %v, %s) = false, want true", tc.model, tc.served, tc.platform)
			}
		})
	}
}

// openai 中继组(native 空映射)不得撞 newapi vendor 模型;但其自身平台模型仍命中。
func TestResolve_NativeEmptyMappingDoesNotMatchOtherVendor(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),   // 中继组,nil served
		grp(11, PlatformNewAPI, 10, false), // deepseek
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

// 1.8.74 prod smoke: /v1/messages + gemini-2.5-flash (tools) 经 universal key 必须落
// antigravity/gemini,不能因 openai passthrough 的「全模型 true」在 tiebreak 里胜过
// antigravity 并打到 Codex（"not supported when using Codex with a ChatGPT account"）。
func TestResolve_GeminiMessagesAnthropicShapeAvoidsOpenAIPassthrough(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		dispatchGrp(2, PlatformOpenAI, 5, true), // Codex passthrough + messages dispatch
		grp(9, PlatformAntigravity, 10, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		9: {"gemini-2.5-flash", "claude-sonnet-4-6"},
	}))
	svc := &GatewayService{
		accountRepo: stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:       100,
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"openai_passthrough": true},
			},
			{
				ID:       101,
				Platform: PlatformAntigravity,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"gemini-2.5-flash": "gemini-2.5-flash"},
				},
			},
		}},
	}
	r.SetModelSupportProvider(svc.UniversalGroupSupportsRequest)

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "gemini-2.5-flash", "")
	if err != nil || g == nil || g.ID != 9 {
		t.Fatalf("gemini @/v1/messages 应落 antigravity gid=9, got=%v err=%v", g, err)
	}
}

// gemini-native image on /v1/chat/completions 必须落 antigravity 组,不能撞 openai/Codex。
func TestResolve_GeminiNativeImageChatPicksAntigravity(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),
		grp(9, PlatformAntigravity, 10, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		9: {"gemini-3.1-flash-image", "claude-sonnet-4-6"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "gemini-3.1-flash-image", "")
	if err != nil || g == nil || g.ID != 9 {
		t.Fatalf("gemini-native image chat 应落 antigravity gid=9, got=%v err=%v", g, err)
	}
}

// imagen 走 newapi google-vertex 组(显式声明),不落 openai 组。
func TestResolve_ImagenPicksVertexNewapiGroup(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),   // openai 中继,nil served(不支持 imagen)
		grp(16, PlatformNewAPI, 10, false), // google-vertex
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

func TestResolve_ImagenServiceAccountPicksVertexWithDirectSchedulerSupport(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(2, PlatformOpenAI, 5, false),
		grp(16, PlatformNewAPI, 10, false),
	}
	imagenMapping := map[string]any{"imagen-4.0-generate-001": "imagen-4.0-generate-001"}
	svc := &GatewayService{
		accountRepo: groupAwareStubOpenAIAccountRepo{stubOpenAIAccountRepo{accounts: []Account{
			{
				ID:          63,
				GroupIDs:    []int64{2},
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				ChannelType: 0,
			},
			{
				ID:          57,
				GroupIDs:    []int64{16},
				Platform:    PlatformNewAPI,
				Type:        AccountTypeServiceAccount,
				Status:      StatusActive,
				Schedulable: true,
				ChannelType: 41,
				Credentials: map[string]any{"model_mapping": imagenMapping},
			},
		}}},
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetModelSupportProvider(svc.UniversalGroupSupportsRequest)

	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIImages, "imagen-4.0-generate-001", "")
	if err != nil || g == nil || g.ID != 16 {
		t.Fatalf("imagen service-account universal must pick vertex gid=16, got=%v err=%v", g, err)
	}
}

// imagen 不得落到 allow_image_generation=false 的 newapi 组; 应优先/仅选已开生图的组。
func TestResolve_ImagenSkipsGroupWithoutImageGenerationPermission(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grpNoImage(16, PlatformNewAPI, 10, false),
		grp(17, PlatformNewAPI, 20, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		16: {"imagen-4.0-fast-generate-001"},
		17: {"imagen-4.0-fast-generate-001"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIImages, "imagen-4.0-fast-generate-001", "")
	if err != nil || g == nil || g.ID != 17 {
		t.Fatalf("imagen 应跳过未开生图的 gid=16 并落 gid=17, got=%v err=%v", g, err)
	}
}

// 若所有可服务 imagen 的组都未开生图,解析失败(403 套餐语义),而非事后 permission_error。
func TestResolve_ImagenNoImageEnabledGroupReturnsNoEntitled(t *testing.T) {
	ctx := context.Background()
	span := []Group{grpNoImage(16, PlatformNewAPI, 10, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		16: {"imagen-4.0-fast-generate-001"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIImages, "imagen-4.0-fast-generate-001", "")
	if err != ErrUniversalNoEntitledGroup || g != nil {
		t.Fatalf("无 allow_image_generation 组应 ErrUniversalNoEntitledGroup, got=%v err=%v", g, err)
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

// 1.8.72 prod 实测:direct Anthropic key 已支持 Claude 模型走 /v1/chat/completions
// 与 /v1/responses。全能 key 的 OpenAI-shaped chat/responses 也应在 claude-* 模型
// 下把 Anthropic 组纳入候选,而不是因候选只含 openai-compat 返回 403。
func TestResolve_ClaudeOpenAIShapePicksAnthropic(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(1, PlatformAnthropic, 5, false),
		grp(2, PlatformOpenAI, 1, false),
		grp(11, PlatformNewAPI, 2, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{})) // native groups only
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "claude-sonnet-4-6", "")
	if err != nil || g == nil || g.ID != 1 {
		t.Fatalf("claude @openai-shape 应落 anthropic gid=1, got=%v err=%v", g, err)
	}
}

func TestResolve_GeminiAnthropicShapePicksGemini(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(40, PlatformGemini, 5, false),
		grp(2, PlatformOpenAI, 1, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		40: {"gemini-2.5-flash"},
		2:  {"gpt-5"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "gemini-2.5-flash", "")
	if err != nil || g == nil || g.ID != 40 {
		t.Fatalf("gemini @/v1/messages 应落 gemini gid=40, got=%v err=%v", g, err)
	}
	g, err = r.Resolve(ctx, universalKey(1), ShapeAnthropicCountTokens, "gemini-2.5-flash", "")
	if err != nil || g == nil || g.ID != 40 {
		t.Fatalf("gemini @count_tokens 应落 gemini gid=40, got=%v err=%v", g, err)
	}
}

func TestResolve_GeminiOpenAIShapePicksGemini(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(40, PlatformGemini, 5, false),
		grp(2, PlatformOpenAI, 1, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		40: {"gemini-2.5-flash"},
		2:  {"gpt-5"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "gemini-2.5-flash", "")
	if err != nil || g == nil || g.ID != 40 {
		t.Fatalf("gemini @openai-shape 应落 gemini gid=40, got=%v err=%v", g, err)
	}
}

func TestResolve_KiroAnthropicShapePicksKiroWhenOnlyEntitled(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(70, PlatformKiro, 5, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{})) // native Kiro fallback

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "claude-sonnet-4-6", "")
	if err != nil || g == nil || g.ID != 70 {
		t.Fatalf("claude @/v1/messages with only kiro entitlement 应落 kiro gid=70, got=%v err=%v", g, err)
	}
	g, err = r.Resolve(ctx, universalKey(1), ShapeAnthropicCountTokens, "claude-sonnet-4-6", "")
	if err != nil || g == nil || g.ID != 70 {
		t.Fatalf("claude @count_tokens with only kiro entitlement 应落 kiro gid=70, got=%v err=%v", g, err)
	}
}

func TestResolve_KiroDoesNotClaimFableOrOpus41ForUniversal(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(70, PlatformKiro, 5, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetModelSupportProvider(func(_ context.Context, gid *int64, platform, model string, _ UniversalShape) (bool, bool) {
		if gid == nil || platform != PlatformKiro {
			return false, true
		}
		return kiroMirrorStubSupportsModel(model), true
	})
	for _, model := range []string{"claude-fable-5", "claude-opus-4-1"} {
		g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, model, "")
		if err != ErrUniversalNoEntitledGroup || g != nil {
			t.Fatalf("%s with only kiro entitlement must ErrUniversalNoEntitledGroup, got=%v err=%v", model, g, err)
		}
	}
}

func TestResolve_MessagesDispatchFiltersPerGroupPolicy(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		dispatchGrp(2, PlatformOpenAI, 1, false), // serves gpt but direct /messages would 403
		dispatchGrp(11, PlatformNewAPI, 5, true), // opens compat candidates but does not serve gpt
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		2:  {"gpt-5"},
		11: {"deepseek-chat"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "gpt-5", "")
	if err != ErrUniversalNoEntitledGroup || g != nil {
		t.Fatalf("universal must not pick a direct-disallowed /messages group, got=%v err=%v", g, err)
	}
}

func TestResolve_CountTokensFiltersPerGroupPolicy(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		dispatchGrp(2, PlatformOpenAI, 1, false), // serves gpt but direct count_tokens would 403
		dispatchGrp(11, PlatformNewAPI, 5, true), // opens compat candidates but does not serve gpt
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		2:  {"gpt-5"},
		11: {"deepseek-chat"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicCountTokens, "gpt-5", "")
	if err != ErrUniversalNoEntitledGroup || g != nil {
		t.Fatalf("universal must not pick a direct-disallowed count_tokens group, got=%v err=%v", g, err)
	}
}

func TestResolve_GrokMessagesDoesNotRequireMessagesDispatch(t *testing.T) {
	ctx := context.Background()
	span := []Group{dispatchGrp(25, PlatformGrok, 0, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		25: {"grok-4.3"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicMessages, "grok-4.3", "")
	if err != nil || g == nil || g.ID != 25 {
		t.Fatalf("grok /v1/messages 应与 direct key 一样不要求 messages_dispatch, got=%v err=%v", g, err)
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

// 有平台 hint 但无组声明该模型 → 403(不盲选错组 → routing 429 P0 风暴)。
func TestResolve_NoServingGroupWithHintReturnsNoEntitled(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(11, PlatformNewAPI, 10, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		11: {"deepseek-chat"}, // 不含 kimi-2.6
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "kimi-2.6", "")
	if err != ErrUniversalNoEntitledGroup || g != nil {
		t.Fatalf("kimi-2.6 无服务组应 ErrUniversalNoEntitledGroup, got=%v err=%v", g, err)
	}
}

// hint 为空(未知 channel 模型)仍退回 eligible,由下游诚实拒绝。
func TestResolve_NoServingGroupWithoutHintFallsBack(t *testing.T) {
	ctx := context.Background()
	span := []Group{grp(11, PlatformNewAPI, 10, false)}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		11: {"deepseek-chat"},
	}))
	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIChat, "some-unmapped-model", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("无 hint 的未知模型应退回 eligible(gid=11); got=%v err=%v", g, err)
	}
}

// user16Span 复刻 prod user_id=16(计算所)的权限跨度形状:一个 anthropic 组 +
// 多个 newapi vendor 组(deepseek / Qwen / volcengine,全部 sort_order=0 且开 dispatch)
// + 一个 openai dispatch 组。volcengine 的 id(5)最小,是「按 id tiebreak 盲选」的陷阱:
// 不靠「组已服务模型集」真值过滤就会把 deepseek 落到 volcengine 组 → 下游 no available accounts。
// 这正是 prod 上 user16 把 deepseek-v4-flash 发到 /v1/messages 时 1045 次 429 的成因
// （direct key 绑死在 claude/Qwen 组,池里没有 deepseek 账号）。
func user16Span() []Group {
	return []Group{
		dispatchGrp(5, PlatformNewAPI, 0, true),  // volcengine —— 最小 id,tiebreak 陷阱
		grp(1, PlatformAnthropic, 1, false),      // claude(native,无 dispatch)
		dispatchGrp(2, PlatformOpenAI, 0, true),  // GPT专线(native openai)
		dispatchGrp(11, PlatformNewAPI, 0, true), // deepseek
		dispatchGrp(18, PlatformNewAPI, 0, true), // Qwen
	}
}

// user16ServingProvider 复刻 prod 上各组的真实服务集(account 39 ds-官 在 11、Qwen 在 18、
// volcengine 在 5;openai/anthropic 组 native 空映射)。
func user16ServingProvider() availableModelsProvider {
	return servedProvider(map[int64][]string{
		11: {"deepseek-chat", "deepseek-v4-pro", "deepseek-reasoner", "deepseek-v4-flash"},
		18: {"qwen-max", "qwen3-235b-a22b", "qwen3-coder-plus"},
		5:  {"doubao-seed-1-6-250615", "doubao-seedream-4-0-250828"},
		// gid=1(anthropic)、gid=2(openai)缺席 → nil(native)
	})
}

func TestResolve_User16GPTAliasPicksOpenAIGroup(t *testing.T) {
	ctx := context.Background()
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: user16Span()})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		2:  {"gpt-5.4-mini"},
		11: {"deepseek-chat", "deepseek-v4-flash"},
		18: {"qwen-max", "qwen3-coder-plus"},
		5:  {"doubao-seed-1-6-250615"},
	}))

	g, err := r.Resolve(ctx, universalKey(16), ShapeOpenAIChat, "gpt5.4-mini", "")
	if err != nil || g == nil || g.ID != 2 {
		t.Fatalf("user16 gpt5.4-mini 应经 universal 落 openai/GPT 专线 gid=2, got=%v err=%v", g, err)
	}
}

// 核心收敛(user16 修复守卫):deepseek-v4-flash 发到 /v1/messages(Anthropic 形状),全能 Key
// 必须落到真正服务它的 deepseek 组(11),而非:anthropic 组(1,空池 429)、volcengine 组
// (5,id 最小的盲选陷阱)、Qwen 组(18)。依赖 ① span 内有 dispatch 组让 /v1/messages 把
// openai-compat 并入候选 ② 服务真值过滤在多 newapi vendor 间精确收敛。
func TestResolve_MessagesDispatch_NewapiDeepseekDisambiguation_User16(t *testing.T) {
	ctx := context.Background()
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: user16Span()})
	r.SetAvailableModelsProvider(user16ServingProvider())
	key := universalKey(16)

	// deepseek-v4-flash on /v1/messages → 必须落 deepseek 组(11)。
	g, err := r.Resolve(ctx, key, ShapeAnthropicMessages, "deepseek-v4-flash", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("deepseek-v4-flash @/v1/messages 应落 deepseek 组 gid=11, got=%v err=%v", g, err)
	}

	// 同跨度同端点,claude-opus-4-8 仍正确落 anthropic 组(1)—— 不被 dispatch 并入的
	// newapi/openai 候选污染(claude-* 的 hint==anthropic,且 native 组按 hint==platform 命中)。
	g, err = r.Resolve(ctx, key, ShapeAnthropicMessages, "claude-opus-4-8", "")
	if err != nil || g == nil || g.ID != 1 {
		t.Fatalf("claude-opus-4-8 @/v1/messages 应落 anthropic 组 gid=1, got=%v err=%v", g, err)
	}

	// deepseek-v4-flash 经原生 openai-compat 入口(/v1/chat/completions)同样落 deepseek 组(11)。
	g, err = r.Resolve(ctx, key, ShapeOpenAIChat, "deepseek-v4-flash", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("deepseek-v4-flash @/v1/chat/completions 应落 deepseek 组 gid=11, got=%v err=%v", g, err)
	}
}

// 守卫服务真值过滤「不可或缺」:同 user16 跨度但 provider 未接线时,/v1/messages + deepseek
// 的 hint==newapi 会在三个 sort_order=0 的 newapi 组里按 id 盲选到 volcengine(5)—— 一个
// 不服务 deepseek 的组。这复现「没有真值过滤就投错组」的回归,锁死过滤器的存在价值。
func TestResolve_MessagesDispatch_NewapiVendor_FilterIsLoadBearing(t *testing.T) {
	ctx := context.Background()
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: user16Span()})
	// 故意不设 provider(模拟未接线/降级)。
	g, err := r.Resolve(ctx, universalKey(16), ShapeAnthropicMessages, "deepseek-v4-flash", "")
	if err != nil || g == nil {
		t.Fatalf("无 provider 应仍解析出一个组(平台级兜底), got=%v err=%v", g, err)
	}
	if g.ID != 5 {
		t.Fatalf("无 provider 时 hint=newapi 按 id 盲选应落 volcengine 组 gid=5(陷阱), got gid=%d —— "+
			"若此处已非 5,说明 tiebreak 行为变更,需同步更新真值过滤守卫", g.ID)
	}
	// 接上 provider 后必须收敛到正确的 deepseek 组(11)—— 与上一个测试呼应。
	r.SetAvailableModelsProvider(user16ServingProvider())
	r.InvalidateAll()
	g, err = r.Resolve(ctx, universalKey(16), ShapeAnthropicMessages, "deepseek-v4-flash", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("接上 provider 后 deepseek 应收敛到 gid=11, got=%v err=%v", g, err)
	}
}

// 1.8.72 prod 实测:direct OpenAI/NewAPI/Grok key 的 /v1/messages/count_tokens 已可用
// (OpenAI-compatible bridge)。全能 key 的 count_tokens 也应按模型收敛到对应
// OpenAI-compatible vendor 组,而不是候选层 403。
func TestResolve_CountTokensDispatchesOpenAICompat_User16(t *testing.T) {
	ctx := context.Background()
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: user16Span()})
	r.SetAvailableModelsProvider(user16ServingProvider())
	key := universalKey(16)

	g, err := r.Resolve(ctx, key, ShapeAnthropicCountTokens, "deepseek-v4-flash", "")
	if err != nil || g == nil || g.ID != 11 {
		t.Fatalf("count_tokens+deepseek 应落 deepseek 组 gid=11, got=%v err=%v", g, err)
	}

	g, err = r.Resolve(ctx, key, ShapeAnthropicCountTokens, "qwen-max", "")
	if err != nil || g == nil || g.ID != 18 {
		t.Fatalf("count_tokens+qwen 应落 Qwen 组 gid=18, got=%v err=%v", g, err)
	}

	g, err = r.Resolve(ctx, key, ShapeAnthropicCountTokens, "gpt-5", "")
	if err != nil || g == nil || g.ID != 2 {
		t.Fatalf("count_tokens+gpt 应落 openai 组 gid=2, got=%v err=%v", g, err)
	}
}

func TestResolve_CountTokensDispatchesGrok(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(25, PlatformGrok, 0, false),
		grp(2, PlatformOpenAI, 1, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		25: {"grok-4.3"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeAnthropicCountTokens, "grok-4.3", "")
	if err != nil || g == nil || g.ID != 25 {
		t.Fatalf("count_tokens+grok 应落 grok 组 gid=25, got=%v err=%v", g, err)
	}
}

func TestResolve_GrokImagePicksGrok(t *testing.T) {
	ctx := context.Background()
	span := []Group{
		grp(25, PlatformGrok, 0, false),
		grp(2, PlatformOpenAI, 1, false),
	}
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: span})
	r.SetAvailableModelsProvider(servedProvider(map[int64][]string{
		25: {"grok-imagine"},
		2:  {"gpt-image-1"},
	}))

	g, err := r.Resolve(ctx, universalKey(1), ShapeOpenAIImages, "grok-imagine", "")
	if err != nil || g == nil || g.ID != 25 {
		t.Fatalf("grok image 应落 grok 组 gid=25, got=%v err=%v", g, err)
	}
}

// prod P0 2026-07-01:user16 universal key 245 压测未上架/未映射模型 → routing 429 风暴。
func TestResolve_UniversalBenchmarkUnservedModels_User16(t *testing.T) {
	ctx := context.Background()
	r := NewUniversalRoutingResolver(&stubSpanLister{groups: user16Span()})
	r.SetAvailableModelsProvider(user16ServingProvider())
	key := universalKey(16)
	cases := []struct {
		shape UniversalShape
		model string
	}{
		{ShapeOpenAIChat, "kimi-2.6"},
		{ShapeOpenAIChat, "deepseek-v3-2-251201"},
		{ShapeOpenAIChat, "glm-4-32b-0414-128k"},
		{ShapeOpenAIChat, "gemini-pro-agent"},
	}
	for _, tc := range cases {
		g, err := r.Resolve(ctx, key, tc.shape, tc.model, "")
		if err != ErrUniversalNoEntitledGroup || g != nil {
			t.Fatalf("model %q shape=%d should 403, got group=%v err=%v", tc.model, tc.shape, g, err)
		}
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
