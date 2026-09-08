//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestProtocolGeminiMessagesToolsPlansAndConvertsVertex(t *testing.T) {
	resolver, _, accounts, _ := candidateGoogleFixture(t)
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":1024,"messages":[{"role":"user","content":"Reply with one short sentence."}],"tools":[{"name":"tk_smoke_schema_probe","description":"Do not call.","input_schema":{"type":"object","required":["mode"],"properties":{"mode":{"type":"string","const":"auto"},"limit":{"type":"integer","minimum":1,"exclusiveMinimum":0,"exclusiveMaximum":100}}}}]}`)
	ctx := resolver.WithRequest(context.Background(), ShapeAnthropicMessages, "/v1/messages", "gemini-3.8-flash", body)
	plan, governed, err := protocolPlanForAccount(ctx, &accounts[0], "gemini-3.8-flash")
	require.True(t, governed)
	require.NoError(t, err)
	require.Equal(t, protocolrouter.AdapterMessagesToGemini, plan.AdapterID())
	require.Equal(t, "gemini-3.8-flash", plan.ResolvedModel())
	converted, err := convertClaudeMessagesToGeminiGenerateContent(body)
	require.NoError(t, err)
	require.Equal(t, "tk_smoke_schema_probe", gjson.GetBytes(converted, "tools.0.functionDeclarations.0.name").String())
	require.Equal(t, "mode", gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.required.0").String())
	require.False(t, gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.properties.mode.const").Exists())
	require.Equal(t, int64(1), gjson.GetBytes(converted, "tools.0.functionDeclarations.0.parameters.properties.limit.minimum").Int())
	group := grp(16, PlatformNewAPI, 0, false)
	group.AllowMessagesDispatch = true
	require.True(t, candidatePathAllowsEndpoint(ctx, &accounts[0], &group, ShapeAnthropicMessages, "gemini-3.8-flash", &plan))
	group.AllowMessagesDispatch = false
	require.False(t, candidatePathAllowsEndpoint(ctx, &accounts[0], &group, ShapeAnthropicMessages, "gemini-3.8-flash", &plan))
}
