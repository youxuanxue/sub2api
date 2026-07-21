package service

import (
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const openAICompatMessagesOpsContextKey = "openai_compat_messages_ops"

// OpenAICompatMessagesOpsSnapshot captures /v1/messages compat forwarding
// decisions for ops indexing (ops_system_logs on forward_failed).
type OpenAICompatMessagesOpsSnapshot struct {
	PreviousResponseIDAttached bool
	MessagesCompactionApplied  bool
	EstimatedInputTokens       int
}

func SetOpenAICompatMessagesOpsContext(c *gin.Context, snapshot OpenAICompatMessagesOpsSnapshot) {
	if c == nil {
		return
	}
	c.Set(openAICompatMessagesOpsContextKey, snapshot)
}

func OpenAICompatMessagesOpsLogFields(c *gin.Context) []zap.Field {
	if c == nil {
		return openAICompatMessagesOpsLogFields(OpenAICompatMessagesOpsSnapshot{})
	}
	value, ok := c.Get(openAICompatMessagesOpsContextKey)
	if !ok {
		return openAICompatMessagesOpsLogFields(OpenAICompatMessagesOpsSnapshot{})
	}
	snapshot, ok := value.(OpenAICompatMessagesOpsSnapshot)
	if !ok {
		return openAICompatMessagesOpsLogFields(OpenAICompatMessagesOpsSnapshot{})
	}
	return openAICompatMessagesOpsLogFields(snapshot)
}

func openAICompatMessagesOpsLogFields(snapshot OpenAICompatMessagesOpsSnapshot) []zap.Field {
	return []zap.Field{
		zap.Bool("compat_previous_response_id_attached", snapshot.PreviousResponseIDAttached),
		zap.Bool("compat_messages_compaction_applied", snapshot.MessagesCompactionApplied),
		zap.Int("compat_estimated_input_tokens", snapshot.EstimatedInputTokens),
	}
}
