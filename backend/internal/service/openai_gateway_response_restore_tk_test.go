//go:build unit

package service

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkRestoreResponsesPayload_SSEAndBodyShareClientToolRestore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setOpenAIResponsesClientToolMapping(c, apicompat.ResponsesClientToolMapping{
		CustomTools: map[string]bool{"exec": true},
	})

	payload := []byte(`{"output":[{"type":"function_call","name":"exec","arguments":"{}"}]}`)

	bodyRestored, err := tkRestoreResponsesPayload(c, payload)
	require.NoError(t, err)

	sseRestored, _, err := tkRestoreResponsesSSEPayload(c, payload, "response.output_item.done")
	require.NoError(t, err)
	require.Equal(t, string(bodyRestored), string(sseRestored))
}

func TestTkRestoreGatewayResponseSSELine_IncludesOpenAIClientToolRestore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	setOpenAIResponsesClientToolMapping(c, apicompat.ResponsesClientToolMapping{
		CustomTools: map[string]bool{"exec": true},
	})

	data := []byte(`{"type":"response.output_item.done","item":{"type":"function_call","name":"exec","arguments":"{}"}}`)
	line, restored, _, _, err := tkRestoreGatewayResponseSSELine(c, data, "response.output_item.done", "data: "+string(data))
	require.NoError(t, err)
	require.Contains(t, string(restored), `"custom_tool_call"`)
	require.Contains(t, line, "data:")
}
