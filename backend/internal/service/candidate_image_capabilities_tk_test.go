//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageCapabilityProfilesRespectKeyAndWebPath(t *testing.T) {
	web := webCandidateAccount(true)
	peer := webCandidateAccount(false)
	peer.ID = 201
	peer.GroupIDs = []int64{741}
	delete(peer.Credentials, "gemini_web")
	groups := []Group{grp(740, PlatformGemini, 1, false), grp(741, PlatformGemini, 1, false)}
	for i := range groups {
		groups[i].AllowImageGeneration = true
	}
	svc, key := candidateDiscoveryFixture(groups, []Account{web, peer})
	find := func() []ImageGenerationCapability {
		capabilities, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
		require.NoError(t, err)
		for _, m := range capabilities {
			if m.ID == "nano-2" {
				require.Contains(t, m.Modalities, UniversalModalityImage)
				return m.ImageGeneration
			}
		}
		t.Fatal("nano-2 missing from authorized catalog")
		return nil
	}
	// Universal can expose broader general Gemini, but must retain separate profiles.
	profiles := find()
	var broad bool
	for _, p := range profiles {
		if len(p.AspectRatios) == len(geminiImageDiscoveryRatios) {
			broad = true
		}
	}
	require.True(t, broad, "%+v", profiles)
	// Direct scope must not borrow the other group's broader profile.
	key.RoutingMode, key.GroupID, key.Group = RoutingModeDirect, &groups[0].ID, &groups[0]
	profiles = find()
	require.Len(t, profiles, 2)
	endpoints := []string{}
	for _, profile := range profiles {
		endpoints = append(endpoints, profile.Endpoint)
		require.ElementsMatch(t, []string{"1:1", "3:4", "4:3", "9:16", "16:9"}, profile.AspectRatios)
		require.Equal(t, []int{1}, profile.Counts)
		require.False(t, profile.InputImage)
		require.Equal(t, profile.Endpoint == "/v1/images/generations", profile.SoftAspectRatio)
	}
	require.ElementsMatch(t, []string{"/v1/chat/completions", "/v1/images/generations"}, endpoints)
	data, err := json.Marshal(profiles)
	require.NoError(t, err)
	require.NotContains(t, string(data), "test-only")
	require.NotContains(t, string(data), "example.invalid")
}

func TestImageCapabilityGPTUsesExistingRatioContract(t *testing.T) {
	account := globalCandidateAccount(100, 1, 10)
	account.Credentials["model_mapping"] = map[string]any{"gpt-image-1": "gpt-image-1"}
	group := grp(10, PlatformOpenAI, 1, false)
	group.AllowImageGeneration = true
	svc, key := candidateDiscoveryFixture([]Group{group}, []Account{account})
	capabilities, err := svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.NoError(t, err)
	require.Len(t, capabilities, 1)
	require.Len(t, capabilities[0].ImageGeneration, 1)
	profile := capabilities[0].ImageGeneration[0]
	require.Equal(t, "/v1/images/generations", profile.Endpoint)
	require.Equal(t, openAIImagesAllowedAspectRatioList, profile.AspectRatios)
	require.True(t, profile.SoftAspectRatio)
	require.False(t, profile.InputImage)
	// A denied image group cannot synthesize controls from the model name.
	group.AllowImageGeneration = false
	svc, key = candidateDiscoveryFixture([]Group{group}, []Account{account})
	capabilities, err = svc.List(context.Background(), key, UniversalProtocolOpenAI)
	require.NoError(t, err)
	for _, model := range capabilities {
		require.Empty(t, model.ImageGeneration)
	}
}
