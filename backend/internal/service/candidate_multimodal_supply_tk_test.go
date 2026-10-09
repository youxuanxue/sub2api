//go:build unit

package service

import (
	"context"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

func multimodalSupplyNVIDIAAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{
			"base_url":      newapiintegration.NVIDIABuildBaseURL,
			"api_key":       "test-nvidia",
			"model_mapping": map[string]any{"kimi-k3": "moonshotai/kimi-k3"},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func multimodalSupplyVolcAccount(id int64, groups ...int64) Account {
	a := *volcEnginePlanTestAccount()
	a.ID, a.Status, a.Schedulable, a.Concurrency, a.Priority, a.GroupIDs = id, StatusActive, true, 10, 10, groups
	a.Credentials["model_mapping"] = map[string]any{"kimi-k3": "kimi-k3"}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func multimodalVideoChatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"video_url","video_url":{"url":"https://example.com/a.mp4"}}]}]}`)
}

func multimodalTextChatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hello"}]}`)
}

func multimodalImageChatBody(model string) []byte {
	return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
}

func multimodalSupplyChinaEdgeAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{
			"base_url":      "https://api-us4.tokenkey.dev",
			"api_key":       "test-china-edge",
			"model_mapping": map[string]any{"glm-5.3": "glm-5.3", "kimi-k3": "kimi-k3"},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func multimodalSupplyTokenseaAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{
			"base_url":      "https://agent.tokensea.ai",
			"api_key":       "test-tokensea",
			"model_mapping": map[string]any{"gpt-5": "gpt-5", "gpt-5.5": "gpt-5.5"},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func TestSupplyKnownNegativeForInputVideoKimiK3NVIDIA(t *testing.T) {
	t.Parallel()
	nvidia := multimodalSupplyNVIDIAAccount(138)
	volc := multimodalSupplyVolcAccount(42)
	other := Account{
		ID: 99, Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeOpenAI,
		Credentials: map[string]any{"base_url": "https://api.openai.com", "api_key": "x"},
	}
	require.True(t, supplyKnownNegativeForInputVideo(&nvidia, "kimi-k3"))
	require.True(t, supplyKnownNegativeForInputVideo(&nvidia, "kimi-k3[thinking]"))
	require.False(t, supplyKnownNegativeForInputVideo(&volc, "kimi-k3"))
	require.False(t, supplyKnownNegativeForInputVideo(&other, "kimi-k3"))
	require.False(t, supplyKnownNegativeForInputVideo(&nvidia, "glm-5.3"),
		"glm-5.3 on NVIDIA has no clear video hard-negative yet (opaque Invalid request)")
	require.True(t, supplyKnownNegativeForInputVideo(&nvidia, "glm-5.3-flash"))
	require.False(t, supplyKnownNegativeForInputVideo(&nvidia, ""))
}

func TestSupplyKnownNegativeGLMTextOnlyVolcAndChinaEdge(t *testing.T) {
	t.Parallel()
	volc := multimodalSupplyVolcAccount(88)
	china := multimodalSupplyChinaEdgeAccount(152)
	nvidia := multimodalSupplyNVIDIAAccount(138)
	for _, model := range []string{"glm-4.5-air", "glm-5.2", "glm-5.3"} {
		require.True(t, supplyKnownNegativeForInputVideo(&volc, model), model)
		require.True(t, supplyKnownNegativeForInputImage(&volc, model), model)
		require.True(t, supplyKnownNegativeForInputVideo(&china, model), model)
		require.True(t, supplyKnownNegativeForInputImage(&china, model), model)
		require.False(t, supplyKnownNegativeForInputImage(&nvidia, model), model)
	}
	require.False(t, supplyKnownNegativeForInputImage(&volc, "glm-5.3-flash"))
	require.False(t, supplyKnownNegativeForInputVideo(&volc, "glm-5.3-flash"))
}

func TestSupplyKnownNegativeTokenseaGPT5FamilyVideo(t *testing.T) {
	t.Parallel()
	sea := multimodalSupplyTokenseaAccount(139)
	nvidia := multimodalSupplyNVIDIAAccount(138)
	for _, model := range []string{"gpt-5", "gpt-5-mini", "gpt-5.1", "gpt-5.2"} {
		require.True(t, supplyKnownNegativeForInputVideo(&sea, model), model)
		require.False(t, supplyKnownNegativeForInputImage(&sea, model), model)
		require.False(t, supplyKnownNegativeForInputVideo(&nvidia, model), model)
	}
	require.False(t, supplyKnownNegativeForInputVideo(&sea, "gpt-5.5"),
		"gpt-5.5 on tokensea accepted video_url in 2026-10-08 probe")
}

func multimodalSupplyAliTokenPlanAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		ChannelType: newapiconstant.ChannelTypeAli,
		Credentials: map[string]any{
			"base_url": newapiintegration.AliTokenPlanBaseURL,
			"api_key":  "test-ali",
			"model_mapping": map[string]any{
				"deepseek-flash": "deepseek-flash", "qwen3.7-max": "qwen3.7-max",
				"glm-5.3": "glm-5.3",
			},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func TestSupplyKnownNegativeAliTokenPlan(t *testing.T) {
	t.Parallel()
	ali := multimodalSupplyAliTokenPlanAccount(129)
	volc := multimodalSupplyVolcAccount(88)
	require.True(t, supplyKnownNegativeForInputVideo(&ali, "deepseek-flash"))
	require.True(t, supplyKnownNegativeForInputVideo(&ali, "deepseek-v4.1-flash"))
	require.False(t, supplyKnownNegativeForInputImage(&ali, "deepseek-flash"))
	require.False(t, supplyKnownNegativeForInputVideo(&volc, "deepseek-flash"),
		"Volc accepts deepseek-flash video — Ali negative must stay supply-scoped")
	require.True(t, supplyKnownNegativeForInputVideo(&ali, "qwen3.7-max"))
	require.True(t, supplyKnownNegativeForInputImage(&ali, "qwen3.7-max"))
	require.False(t, supplyKnownNegativeForInputVideo(&ali, "glm-5.3"),
		"glm-5.3 video is servable on Ali — must not inherit Volc text-only negative")
	require.False(t, supplyKnownNegativeForInputImage(&ali, "glm-5.3"))
}

func multimodalSupplyQianfanTokenPlanAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		ChannelType: newapiconstant.ChannelTypeBaiduV2,
		Credentials: map[string]any{
			"base_url": newapiintegration.QianfanTokenPlanBaseURL,
			"api_key":  "test-qianfan",
			"model_mapping": map[string]any{
				"deepseek-v4-pro": "deepseek-v4-pro", "glm-4.5-air": "glm-4.5-air", "glm-4.7": "glm-4.7",
			},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	return a
}

func TestSupplyKnownNegativeQianfanTokenPlan(t *testing.T) {
	t.Parallel()
	qf := multimodalSupplyQianfanTokenPlanAccount(130)
	volc := multimodalSupplyVolcAccount(88)
	for _, model := range []string{"deepseek-v4-flash-0731", "deepseek-v4-pro", "deepseek-v4-pro-0813", "glm-4.5-air", "glm-4.7"} {
		require.True(t, supplyKnownNegativeForInputVideo(&qf, model), model)
		require.True(t, supplyKnownNegativeForInputImage(&qf, model), model)
	}
	require.False(t, supplyKnownNegativeForInputVideo(&qf, "glm-5.3"))
	require.False(t, supplyKnownNegativeForInputVideo(&volc, "deepseek-v4-pro"),
		"Volc deepseek-v4-pro video is servable — Qianfan negative must stay supply-scoped")
}

func multimodalSupplyOpenAIEdgeAccount(id int64, groups ...int64) Account {
	a := Account{
		ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive,
		Schedulable: true, Concurrency: 10, Priority: 1, GroupIDs: groups,
		Credentials: map[string]any{
			"base_url": "https://api-us6.tokenkey.dev",
			"api_key":  "test-openai-edge",
			"model_mapping": map[string]any{
				"gpt-5.5": "gpt-5.5", "gpt-5.3-codex-spark": "gpt-5.3-codex-spark",
			},
		},
	}
	attachTestProtocolCapability(&a, protocolrouter.ProtocolResponses)
	return a
}

func TestSupplyKnownNegativeOpenAIEdgeMirror(t *testing.T) {
	t.Parallel()
	oai := multimodalSupplyOpenAIEdgeAccount(63)
	nvidia := multimodalSupplyNVIDIAAccount(138)
	require.True(t, isOpenAIEdgeMirrorAccount(&oai))
	require.True(t, supplyKnownNegativeForInputVideo(&oai, "gpt-5.5"),
		"OpenAI edge rejects input_video supply-wide")
	require.True(t, supplyKnownNegativeForInputVideo(&oai, "gpt-6.1"))
	require.True(t, supplyKnownNegativeForInputVideo(&oai, "codex-auto-review"))
	require.False(t, supplyKnownNegativeForInputImage(&oai, "gpt-5.5"),
		"gpt-5.5 image is servable on OpenAI edge")
	require.True(t, supplyKnownNegativeForInputImage(&oai, "gpt-5.3-codex-spark"))
	require.False(t, supplyKnownNegativeForInputVideo(&nvidia, "gpt-5.5"),
		"OpenAI edge video negative must not apply to NVIDIA")
}

func TestAccountAdmitsRequestInputModalitiesVideoGate(t *testing.T) {
	t.Parallel()
	nvidia := multimodalSupplyNVIDIAAccount(138)
	volc := multimodalSupplyVolcAccount(42)
	router := NewProtocolRouter()

	videoReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"kimi-k3", false, multimodalVideoChatBody("kimi-k3"),
	)
	require.NoError(t, err)
	require.NotZero(t, videoReq.Profile().ContentKinds&protocolrouter.ContentVideo)
	videoCtx := WithProtocolRouting(context.Background(), router, videoReq)
	require.Equal(t, multimodalInputModalityVideo, accountInputModalityRejection(videoCtx, &nvidia, "kimi-k3"))
	require.Empty(t, accountInputModalityRejection(videoCtx, &volc, "kimi-k3"))

	textReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"kimi-k3", false, multimodalTextChatBody("kimi-k3"),
	)
	require.NoError(t, err)
	require.Zero(t, textReq.Profile().ContentKinds&protocolrouter.ContentVideo)
	textCtx := WithProtocolRouting(context.Background(), router, textReq)
	require.Empty(t, accountInputModalityRejection(textCtx, &nvidia, "kimi-k3"),
		"text-only must not apply the video known-negative")
	require.Empty(t, accountInputModalityRejection(context.Background(), &nvidia, "kimi-k3"),
		"ungoverned context stays eligible")
}

func TestAccountAdmitsRequestInputModalitiesImageGate(t *testing.T) {
	t.Parallel()
	volc := multimodalSupplyVolcAccount(88)
	china := multimodalSupplyChinaEdgeAccount(152)
	router := NewProtocolRouter()

	imageReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"glm-5.3", false, multimodalImageChatBody("glm-5.3"),
	)
	require.NoError(t, err)
	require.NotZero(t, imageReq.Profile().ContentKinds&protocolrouter.ContentImage)
	imageCtx := WithProtocolRouting(context.Background(), router, imageReq)
	require.Equal(t, multimodalInputModalityImage, accountInputModalityRejection(imageCtx, &volc, "glm-5.3"))
	require.Equal(t, multimodalInputModalityImage, accountInputModalityRejection(imageCtx, &china, "glm-5.3"))

	flashReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"glm-5.3-flash", false, multimodalImageChatBody("glm-5.3-flash"),
	)
	require.NoError(t, err)
	flashCtx := WithProtocolRouting(context.Background(), router, flashReq)
	require.Empty(t, accountInputModalityRejection(flashCtx, &volc, "glm-5.3-flash"),
		"glm-5.3-flash image is servable on Volc — must not inherit glm-5.3 text-only negative")
}

func TestCandidateSupportsRequestRejectsNVIDIAKimiK3Video(t *testing.T) {
	nvidia := multimodalSupplyNVIDIAAccount(138, 10)
	volc := multimodalSupplyVolcAccount(42, 10)
	groups := []Group{grp(10, PlatformNewAPI, 1, false)}
	r, _, _ := globalCandidateFixture(groups, []Account{nvidia, volc})
	body := multimodalVideoChatBody("kimi-k3")
	ctx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "kimi-k3", body)
	req, ok := ProtocolRoutingRequest(ctx)
	require.True(t, ok)
	require.NotZero(t, req.Profile().ContentKinds&protocolrouter.ContentVideo)

	okNVIDIA, err := r.candidateGateway.candidateSupportsRequest(ctx, &nvidia, PlatformNewAPI, false, "kimi-k3", ShapeOpenAIChat)
	require.ErrorIs(t, err, ErrUnsupportedInputModality)
	require.False(t, okNVIDIA, "N1: NVIDIA Build + kimi-k3 + video must not admit")

	okVolc, err := r.candidateGateway.candidateSupportsRequest(ctx, &volc, PlatformNewAPI, false, "kimi-k3", ShapeOpenAIChat)
	require.NoError(t, err)
	require.True(t, okVolc, "P1: Volc Agent Plan + kimi-k3 + video remains eligible")

	textCtx := r.WithRequest(context.Background(), ShapeOpenAIChat, "/v1/chat/completions", "kimi-k3", multimodalTextChatBody("kimi-k3"))
	okText, err := r.candidateGateway.candidateSupportsRequest(textCtx, &nvidia, PlatformNewAPI, false, "kimi-k3", ShapeOpenAIChat)
	require.NoError(t, err)
	require.True(t, okText, "R1: text-only NVIDIA kimi-k3 still admits")
}

func TestCandidateSelectionPrefersVolcForKimiK3Video(t *testing.T) {
	nvidia := multimodalSupplyNVIDIAAccount(138, 10)
	volc := multimodalSupplyVolcAccount(42, 10)
	nvidia.Priority, volc.Priority = 100, 1 // NVIDIA would win on priority without the gate
	groups := []Group{grp(10, PlatformNewAPI, 1, false)}
	r, _, key := globalCandidateFixture(groups, []Account{nvidia, volc})
	_, state, err := r.PrepareCandidateRequest(
		context.Background(), key, ShapeOpenAIChat, "/v1/chat/completions",
		"kimi-k3", multimodalVideoChatBody("kimi-k3"), "", "",
	)
	require.NoError(t, err)
	require.Equal(t, volc.ID, state.current.account.ID, "P1: selection must land on Volc, not higher-priority NVIDIA")

	rOnlyNVIDIA, _, keyOnly := globalCandidateFixture(groups, []Account{nvidia})
	_, stateOnly, err := rOnlyNVIDIA.PrepareCandidateRequest(
		context.Background(), keyOnly, ShapeOpenAIChat, "/v1/chat/completions",
		"kimi-k3", multimodalVideoChatBody("kimi-k3"), "", "",
	)
	require.ErrorIs(t, err, ErrUnsupportedInputModality, "N1: NVIDIA-only video pool must be modality 400, not capacity/unsupported-model")
	require.Contains(t, err.Error(), "video")
	require.Nil(t, stateOnly)
	require.Equal(t, "No available endpoints support input video for model: kimi-k3",
		TkUnsupportedInputModalityMessage("kimi-k3", "video"))
}

func TestOpenAIRequestEligibilityReasonRejectsNVIDIAKimiK3Video(t *testing.T) {
	t.Parallel()
	nvidia := multimodalSupplyNVIDIAAccount(138)
	volc := multimodalSupplyVolcAccount(42)
	router := NewProtocolRouter()
	videoReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"kimi-k3", false, multimodalVideoChatBody("kimi-k3"),
	)
	require.NoError(t, err)
	ctx := WithProtocolRouting(context.Background(), router, videoReq)

	reason := openAIRequestEligibilityReason(ctx, &nvidia, "kimi-k3", false, "")
	require.Equal(t, openAICompatIneligibleInputModality+"(video)", reason)

	require.Empty(t, openAIRequestEligibilityReason(ctx, &volc, "kimi-k3", false, ""))
	textReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"kimi-k3", false, multimodalTextChatBody("kimi-k3"),
	)
	require.NoError(t, err)
	textCtx := WithProtocolRouting(context.Background(), router, textReq)
	require.Empty(t, openAIRequestEligibilityReason(textCtx, &nvidia, "kimi-k3", false, ""),
		"text-only must not trip the Direct scheduler modality gate")
}

func TestOpenAIFilterOnlyReason(t *testing.T) {
	t.Parallel()
	stats := openAISelectionFilterStats{pool: 2, reasons: map[string]int{openAICompatIneligibleInputModality: 2}}
	require.True(t, openAIFilterOnlyReason(stats, openAICompatIneligibleInputModality))
	stats.reasons["model_not_supported"] = 1
	require.False(t, openAIFilterOnlyReason(stats, openAICompatIneligibleInputModality))
	require.False(t, openAIFilterOnlyReason(openAISelectionFilterStats{}, openAICompatIneligibleInputModality))
}
