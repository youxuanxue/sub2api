//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSetClaudeCodeClientContext_DesktopGatewayUA(t *testing.T) {
	t.Parallel()

	c, _ := newHelperTestContext(http.MethodPost, "/v1/messages")
	c.Request.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Claude/1.8555.2 Chrome/146.0.7680.216 Electron/41.6.1 Safari/537.36")

	SetClaudeCodeClientContext(c, []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":4,"messages":[{"role":"user","content":"ok"}]}`), nil)

	require.False(t, service.IsClaudeCodeClient(c.Request.Context()))
	require.True(t, service.IsClaudeDesktopGatewayClient(c.Request.Context()), "Desktop 3p Electron UA must set IsClaudeDesktopGatewayClient on fast path")
}
