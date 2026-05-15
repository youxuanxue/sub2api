package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/claude"
	"github.com/stretchr/testify/require"
)

func TestApplyClaudeCodeMimicHeadersPreservesCapturedFingerprint(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	require.NoError(t, err)
	setHeaderRaw(req.Header, "User-Agent", "claude-cli/2.2.10 (external, cli)")
	setHeaderRaw(req.Header, "X-Stainless-Package-Version", "0.71.0")

	applyClaudeCodeMimicHeaders(req, true)

	require.Equal(t, "claude-cli/2.2.10 (external, cli)", getHeaderRaw(req.Header, "User-Agent"))
	require.Equal(t, "0.71.0", getHeaderRaw(req.Header, "X-Stainless-Package-Version"))
	require.Equal(t, claude.DefaultHeaders["X-App"], getHeaderRaw(req.Header, "x-app"))
	require.Equal(t, "application/json", getHeaderRaw(req.Header, "Accept"))
	require.NotEmpty(t, getHeaderRaw(req.Header, "x-client-request-id"))
}

func TestApplyClaudeCodeMimicHeadersDoesNotEmitHelperMethod(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	require.NoError(t, err)

	applyClaudeCodeMimicHeaders(req, true)

	require.Empty(t, getHeaderRaw(req.Header, "x-stainless-helper-method"))
}
