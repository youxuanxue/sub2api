package service

import (
	"bytes"
	"fmt"

	"github.com/gin-gonic/gin"
)

// tkRestoreGrokOpenAIResponsesSSEPayload restores Grok client-tool payloads, OpenAI
// namespace fields, and Codex tool-name aliases for one SSE data line.
func tkRestoreGrokOpenAIResponsesSSEPayload(
	c *gin.Context,
	dataBytes []byte,
	eventType string,
	line string,
) (string, []byte, string, string, error) {
	data := string(dataBytes)
	restoredData, restoreErr := restoreGrokResponsesClientToolPayload(c, dataBytes)
	if restoreErr != nil {
		return line, dataBytes, data, eventType, fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
	}
	restoredData, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredData)
	if restoreErr != nil {
		return line, dataBytes, data, eventType, fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
	}
	restoredData = restoreCodexToolNamesFromSSEContext(c, restoredData, eventType)
	if !bytes.Equal(restoredData, dataBytes) {
		dataBytes = restoredData
		data = string(restoredData)
		line = "data: " + data
		eventType = effectiveOpenAISSEEventType(dataBytes, eventType)
	}
	return line, dataBytes, data, eventType, nil
}

// tkRestoreGatewayResponseBody applies Grok/OpenAI namespace/Codex tool restore on
// a complete non-streaming or SSE-to-JSON response body.
func tkRestoreGatewayResponseBody(c *gin.Context, body []byte) ([]byte, error) {
	restoredBody, restoreErr := restoreGrokResponsesClientToolPayload(c, body)
	if restoreErr != nil {
		return body, fmt.Errorf("restore Grok Responses client tool response: %w", restoreErr)
	}
	restoredBody, restoreErr = restoreOpenAIResponsesClientToolPayload(c, restoredBody)
	if restoreErr != nil {
		return body, fmt.Errorf("restore OpenAI Responses client tool response: %w", restoreErr)
	}
	restoredBody, restoreErr = restoreOpenAIResponsesNamespacePayload(c, restoredBody)
	if restoreErr != nil {
		return body, fmt.Errorf("restore OpenAI namespace response: %w", restoreErr)
	}
	return restoreCodexToolNamesFromContext(c, restoredBody), nil
}
