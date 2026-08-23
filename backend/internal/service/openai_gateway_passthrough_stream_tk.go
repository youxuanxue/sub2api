package service

import (
	"bytes"
	"strings"

	"github.com/gin-gonic/gin"
)

// tkRestorePassthroughSSEPayload applies namespace restore and Codex tool-name
// alias reversal for one passthrough SSE data payload.
func tkRestorePassthroughSSEPayload(
	c *gin.Context,
	dataBytes []byte,
	rawEventType string,
	line string,
) (string, []byte, string, error) {
	trimmedData := strings.TrimSpace(string(dataBytes))
	if trimmedData == "[DONE]" {
		return line, dataBytes, trimmedData, nil
	}
	restoredData, restoreErr := restoreOpenAIResponsesNamespacePayload(c, dataBytes)
	if restoreErr != nil {
		return line, dataBytes, trimmedData, restoreErr
	}
	restoredData = restoreCodexToolNamesFromSSEContext(c, restoredData, rawEventType)
	if !bytes.Equal(restoredData, dataBytes) {
		dataBytes = restoredData
		trimmedData = strings.TrimSpace(string(restoredData))
		line = "data: " + string(restoredData)
	}
	return line, dataBytes, trimmedData, nil
}
