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
	require.False(t, supplyKnownNegativeForInputVideo(&nvidia, "glm-5.3"))
	require.False(t, supplyKnownNegativeForInputVideo(&nvidia, ""))
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
	require.False(t, accountAdmitsRequestInputModalities(videoCtx, &nvidia, "kimi-k3"))
	require.True(t, accountAdmitsRequestInputModalities(videoCtx, &volc, "kimi-k3"))

	textReq, err := protocolrouter.ParseCanonicalRequest(
		protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone,
		"kimi-k3", false, multimodalTextChatBody("kimi-k3"),
	)
	require.NoError(t, err)
	require.Zero(t, textReq.Profile().ContentKinds&protocolrouter.ContentVideo)
	textCtx := WithProtocolRouting(context.Background(), router, textReq)
	require.True(t, accountAdmitsRequestInputModalities(textCtx, &nvidia, "kimi-k3"),
		"text-only must not apply the video known-negative")
	require.True(t, accountAdmitsRequestInputModalities(context.Background(), &nvidia, "kimi-k3"),
		"ungoverned context stays eligible")
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
	require.NoError(t, err)
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
	require.Error(t, err, "N1: NVIDIA-only pool must yield no candidate for video")
	require.Nil(t, stateOnly)
}
