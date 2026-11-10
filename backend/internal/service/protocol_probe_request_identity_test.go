package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestApplyProtocolProbeRequestIdentitySkipsCodexHeadersForPlatformAPIKey(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	got := applyProtocolProbeRequestIdentity(req, account, protocolrouter.ProtocolResponses)

	require.Empty(t, got.Header.Get("originator"))
	require.Empty(t, got.Header.Get("version"))
	require.Empty(t, got.Header.Get("OpenAI-Beta"))
	require.NotContains(t, got.Header.Get("User-Agent"), "codex")
}

func TestApplyProtocolProbeRequestIdentityKeepsCodexHeadersForOAuth(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	got := applyProtocolProbeRequestIdentity(req, account, protocolrouter.ProtocolResponses)

	require.Equal(t, openai.CodexDefaultOriginator, got.Header.Get("originator"))
	require.Equal(t, codexCLIVersion, got.Header.Get("version"))
	require.Equal(t, "responses=experimental", got.Header.Get("OpenAI-Beta"))
	require.Contains(t, got.Header.Get("User-Agent"), openai.CodexDefaultOriginator)
}

func TestApplyProtocolProbeRequestIdentitySkipsCodexHeadersForGrokResponses(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://api.x.ai/v1/responses", nil)
	account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}

	got := applyProtocolProbeRequestIdentity(req, account, protocolrouter.ProtocolResponses)

	require.Empty(t, got.Header.Get("originator"))
	require.Empty(t, got.Header.Get("OpenAI-Beta"))
}
