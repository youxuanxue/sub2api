package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func encodeZapField(field zap.Field) map[string]any {
	enc := zapcore.NewMapObjectEncoder()
	field.AddTo(enc)
	return enc.Fields
}

func TestOpenAICompatMessagesOpsContextLogFields(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("defaults when unset", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)

		fields := OpenAICompatMessagesOpsLogFields(c)
		require.Len(t, fields, 3)
		require.Equal(t, false, encodeZapField(fields[0])["compat_previous_response_id_attached"])
		require.Equal(t, false, encodeZapField(fields[1])["compat_messages_compaction_applied"])
		require.Equal(t, int64(0), encodeZapField(fields[2])["compat_estimated_input_tokens"])
	})

	t.Run("reflects snapshot", func(t *testing.T) {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		SetOpenAICompatMessagesOpsContext(c, OpenAICompatMessagesOpsSnapshot{
			PreviousResponseIDAttached: true,
			MessagesCompactionApplied:  true,
			EstimatedInputTokens:       181000,
		})

		fields := OpenAICompatMessagesOpsLogFields(c)
		require.Equal(t, true, encodeZapField(fields[0])["compat_previous_response_id_attached"])
		require.Equal(t, true, encodeZapField(fields[1])["compat_messages_compaction_applied"])
		require.Equal(t, int64(181000), encodeZapField(fields[2])["compat_estimated_input_tokens"])
	})
}
