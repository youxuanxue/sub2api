//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Regression for the grok (seventh platform) direct-scheduling bug.
//
// Every OpenAI-compat selection runs each candidate through
// accountSupportsOpenAICapabilities (openai_account_scheduler.go). For a grok
// account that funnels into Account.SupportsOpenAIImageCapability, which
// hardcoded an "openai || newapi" allowlist and never included grok. So a
// schedulable grok account in a grok group was silently excluded on EVERY chat
// completions request, and the gateway fast-failed with "no available accounts"
// (excluded_account_count=0). grok was never exercised in production (first /
// only grok account, last_used=never), so this latent gap only surfaced on the
// first real request.
//
// The capability gate must treat grok like the other OpenAI-compat platforms
// (openai / newapi): grok rides the same OpenAI wire protocol and image surface.
func TestGrokAccount_PassesOpenAICompatCapabilityGate(t *testing.T) {
	grok := &Account{
		ID:          1,
		Platform:    PlatformGrok,
		Type:        AccountTypeOAuth,
		ChannelType: 0,
		Status:      StatusActive,
		Schedulable: true,
	}

	// Chat-completions selection passes requiredImageCapability="" (no image
	// requirement). The grok account MUST NOT be excluded by the capability gate.
	require.True(t,
		accountSupportsOpenAICapabilities(grok, OpenAIEndpointCapabilityChatCompletions, ""),
		"grok account must pass the OpenAI-compat capability gate for chat completions",
	)

	// The underlying image-capability predicate must recognize grok as an
	// OpenAI-compat platform (true for the no-requirement case).
	require.True(t, grok.SupportsOpenAIImageCapability(""),
		"grok must be recognized as an OpenAI-compat platform")

	// grok image generation (oauth account) is also supported.
	require.True(t, grok.SupportsOpenAIImageCapability(OpenAIImagesCapabilityBasic),
		"grok oauth account supports basic image capability")
	require.True(t, grok.SupportsOpenAIImageCapability(OpenAIImagesCapabilityNative),
		"grok oauth account supports native image capability")
}
