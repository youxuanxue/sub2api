package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
)

func TestApplyOpenAICompatReplayCompaction_TailOnlyProfileTrimsOldMessages(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := 0; i < openAICompatAnthropicReplayMaxTailMessages+3; i++ {
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    "user",
			Content: json.RawMessage(fmt.Sprintf(`"message-%02d"`, i)),
		})
	}

	trimmed := applyOpenAICompatReplayCompaction(req, openAICompatReplayCompactionProfile{tailMessages: openAICompatAnthropicReplayMaxTailMessages})

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages)
	require.JSONEq(t, `"message-03"`, string(req.Messages[0].Content))
	require.JSONEq(t, `"message-14"`, string(req.Messages[len(req.Messages)-1].Content))
}

func TestApplyOpenAICompatReplayCompaction_TailOnlyProfileKeepsToolBoundaryIntact(t *testing.T) {
	t.Parallel()

	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, openAICompatAnthropicReplayMaxTailMessages+3)}
	for i := 0; i < openAICompatAnthropicReplayMaxTailMessages+3; i++ {
		role := "user"
		content := json.RawMessage(fmt.Sprintf(`"message-%02d"`, i))
		if i == 1 {
			role = "assistant"
			content = json.RawMessage(`[{"type":"tool_use","id":"toolu_keep","name":"Read","input":{"file_path":"main.go"}}]`)
		}
		if i == 3 {
			content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_keep","content":"ok"}]`)
		}
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	trimmed := applyOpenAICompatReplayCompaction(req, openAICompatReplayCompactionProfile{tailMessages: openAICompatAnthropicReplayMaxTailMessages})

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatAnthropicReplayMaxTailMessages+2)
	require.Equal(t, "assistant", req.Messages[0].Role)
	require.Contains(t, string(req.Messages[0].Content), `"toolu_keep"`)
	require.Contains(t, string(req.Messages[2].Content), `"tool_result"`)
}

func TestApplyOpenAICompatReplayCompaction_AnchorAndTailProfileKeepsPrefixAndTail(t *testing.T) {
	t.Parallel()

	messageCount := openAICompatOAuthReplayAnchorMessages + openAICompatAnthropicReplayMaxTailMessages + 4
	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, messageCount)}
	for i := 0; i < messageCount; i++ {
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    "user",
			Content: json.RawMessage(fmt.Sprintf(`"message-%02d"`, i)),
		})
	}

	trimmed := applyOpenAICompatReplayCompaction(req, openAICompatReplayCompactionProfile{prefixMessages: openAICompatOAuthReplayAnchorMessages, tailMessages: openAICompatAnthropicReplayMaxTailMessages})

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatOAuthReplayAnchorMessages+openAICompatAnthropicReplayMaxTailMessages)
	require.JSONEq(t, `"message-00"`, string(req.Messages[0].Content))
	require.JSONEq(t, `"message-01"`, string(req.Messages[1].Content))
	require.JSONEq(t, `"message-06"`, string(req.Messages[2].Content))
	require.JSONEq(t, `"message-17"`, string(req.Messages[len(req.Messages)-1].Content))
}

func TestApplyOpenAICompatReplayCompaction_AnchorAndTailProfileKeepsPrefixToolBoundaryIntact(t *testing.T) {
	t.Parallel()

	messageCount := openAICompatOAuthReplayAnchorMessages + openAICompatAnthropicReplayMaxTailMessages + 4
	req := &apicompat.AnthropicRequest{Messages: make([]apicompat.AnthropicMessage, 0, messageCount)}
	for i := 0; i < messageCount; i++ {
		role := "user"
		content := json.RawMessage(fmt.Sprintf(`"message-%02d"`, i))
		if i == 1 {
			role = "assistant"
			content = json.RawMessage(`[{"type":"tool_use","id":"toolu_prefix","name":"Read","input":{"file_path":"main.go"}}]`)
		}
		if i == 2 {
			content = json.RawMessage(`[{"type":"tool_result","tool_use_id":"toolu_prefix","content":"ok"}]`)
		}
		req.Messages = append(req.Messages, apicompat.AnthropicMessage{
			Role:    role,
			Content: content,
		})
	}

	trimmed := applyOpenAICompatReplayCompaction(req, openAICompatReplayCompactionProfile{prefixMessages: openAICompatOAuthReplayAnchorMessages, tailMessages: openAICompatAnthropicReplayMaxTailMessages})

	require.True(t, trimmed)
	require.Len(t, req.Messages, openAICompatOAuthReplayAnchorMessages+openAICompatAnthropicReplayMaxTailMessages+1)
	require.JSONEq(t, `"message-00"`, string(req.Messages[0].Content))
	require.Contains(t, string(req.Messages[1].Content), `"toolu_prefix"`)
	require.Contains(t, string(req.Messages[2].Content), `"tool_result"`)
	require.JSONEq(t, `"message-06"`, string(req.Messages[3].Content))
	require.JSONEq(t, `"message-17"`, string(req.Messages[len(req.Messages)-1].Content))
}
