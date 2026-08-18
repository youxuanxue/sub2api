package middleware

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsAsyncImageTaskRead(t *testing.T) {
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/tasks/imgtask_123"))
	require.True(t, isAsyncImageTaskRead(http.MethodGet, "/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodPost, "/v1/images/tasks/imgtask_123"))
	require.False(t, isAsyncImageTaskRead(http.MethodGet, "/v1/images/generations"))
}

func TestIsTokenKeyVideoTaskRead(t *testing.T) {
	for _, path := range []string{
		"/v1/video/generations/vt_123",
		"/video/generations/vt_123",
		"/v1/videos/vt_123",
		"/videos/generations/vt_123",
		"/v1/videos/edits/vt_123",
	} {
		require.True(t, isTokenKeyVideoTaskRead(http.MethodGet, path), path)
		require.True(t, skipsBillingEnforcement(http.MethodGet, path), path)
	}

	require.False(t, isTokenKeyVideoTaskRead(http.MethodPost, "/v1/videos/vt_123"))
	require.False(t, isTokenKeyVideoTaskRead(http.MethodGet, "/v1/videos/request-native"))
	require.False(t, isTokenKeyVideoTaskRead(http.MethodGet, "/v1/videos/vt_123/content"))
	require.False(t, skipsBillingEnforcement(http.MethodGet, "/v1/videos/request-native"))
}
