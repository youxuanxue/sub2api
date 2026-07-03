package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkValidateAnthropicToolContext_FlagsToolResultOnly(t *testing.T) {
	body := []byte(`{
		"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}],
		"max_tokens":64000,
		"thinking":{"type":"adaptive"}
	}`)
	violation := tkValidateAnthropicToolContext(body, false)
	require.NotNil(t, violation)
	require.Equal(t, "orphan_tool_result_context", violation.Reason)
	require.Equal(t, 0, violation.MessageIndex)
	require.Equal(t, "toolu_01", violation.ToolUseID)
}

func TestTkValidateAnthropicToolContext_AllowsSdkToolContinuation(t *testing.T) {
	body := []byte(`{
		"system":[
			{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.198.b93; cc_entrypoint=claude-vscode;"},
			{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK."}
		],
		"messages":[
			{"role":"user","content":[{"type":"text","text":"<system-reminder>\nToday's date is 2026-07-03.\n</system-reminder>"},{"type":"text","text":"run echo"}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{"command":"echo TOOL_OK"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"TOOL_OK"}]}
		],
		"max_tokens":64000,
		"thinking":{"type":"adaptive"}
	}`)
	require.Nil(t, tkValidateAnthropicToolContext(body, true))
}

func TestTkValidateAnthropicToolContext_FlagsMissingParallelToolResult(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[
				{"type":"tool_use","id":"toolu_a","name":"Read","input":{}},
				{"type":"tool_use","id":"toolu_b","name":"Read","input":{}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"ok"}]}
		]
	}`)
	violation := tkValidateAnthropicToolContext(body, false)
	require.NotNil(t, violation)
	require.Equal(t, "missing_tool_result_for_tool_use", violation.Reason)
	require.Equal(t, "toolu_b", violation.ToolUseID)
}

func TestTkValidateAnthropicToolContext_FlagsToolResultWithoutSystemWhenRequired(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"Bash","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}
		]
	}`)
	require.Nil(t, tkValidateAnthropicToolContext(body, false))
	violation := tkValidateAnthropicToolContext(body, true)
	require.NotNil(t, violation)
	require.Equal(t, "tool_result_without_system_surface", violation.Reason)
}

func TestTkRejectInvalidAnthropicToolContext_WritesLocalClientErrorAndOpsEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"ok"}]}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req_guard")
	account := &Account{ID: 13, Name: "oh-3-g", Platform: PlatformAnthropic}

	err := (&GatewayService{}).tkRejectInvalidAnthropicToolContext(ctx, c, account, body, true, false)
	var guardErr *ClientToolContextCorruptError
	require.ErrorAs(t, err, &guardErr)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Invalid Anthropic tool continuation")
	require.True(t, IsResponseCommitted(c))

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, OpsUpstreamKindClientToolContextCorrupt, events[0].Kind)
	require.Equal(t, int64(13), events[0].AccountID)
	require.Equal(t, 0, events[0].UpstreamStatusCode)
	require.Contains(t, events[0].UpstreamRequestBody, `"tool_result"`)
}
