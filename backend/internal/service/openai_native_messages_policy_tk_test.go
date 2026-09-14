//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNativeMessagesPolicyAfterHeaderKeepalive(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")
	_, err := c.Writer.WriteString(anthropicSSEPingFrame)
	require.NoError(t, err)
	c.Writer.Flush()

	payload := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"cyber_policy","message":"Request blocked by upstream cyber-security policy"}}`)
	// The upstream returned an HTTP error before any model output, after the
	// header-wait keepalive had already committed the client's SSE response.
	require.True(t, forwardNativeMessagesPolicy(c, payload, http.StatusBadRequest, nil, "messages", false, "", ""))
	require.True(t, IsResponseCommitted(c))
	require.NotNil(t, GetOpsCyberPolicy(c))
	require.Equal(t, 1, strings.Count(recorder.Body.String(), "event: error\n"))
	require.Contains(t, recorder.Body.String(), `"code":"cyber_policy"`)
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		require.False(t, strings.HasPrefix(line, "{"), "JSON outside an SSE data frame is invisible to the client")
	}
}
