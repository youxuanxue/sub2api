package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeMessagesProbeSupported(t *testing.T) {
	t.Parallel()
	require.False(t, nativeMessagesProbeSupported(http.StatusNotFound, nil))
	require.False(t, nativeMessagesProbeSupported(http.StatusMethodNotAllowed, nil))
	require.False(t, nativeMessagesProbeSupported(http.StatusBadRequest, []byte(`{"type":"error"}`)))
	require.True(t, nativeMessagesProbeSupported(http.StatusOK, []byte(`{"type":"message","content":[{"type":"text","text":"OK"}]}`)))
	require.True(t, nativeMessagesProbeSupported(http.StatusOK, []byte(`{"content":[{"type":"text","text":"OK"}]}`)))
}

func TestResponsesProbeBodyIndicatesNotImplemented(t *testing.T) {
	t.Parallel()
	require.True(t, responsesProbeBodyIndicatesNotImplemented([]byte(`{"error":{"message":"not implemented","code":"convert_request_failed"}}`)))
	require.False(t, responsesProbeBodyIndicatesNotImplemented([]byte(`{"error":{"message":"rate limit"}}`)))
}
