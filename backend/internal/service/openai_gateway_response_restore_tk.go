package service

import (
	"bytes"
	"fmt"

	"github.com/gin-gonic/gin"
)

// tkRestoreResponsesPayload is the canonical restore pipeline for complete JSON
// payloads (non-streaming body, SSE-to-JSON terminal, passthrough JSON).
func tkRestoreResponsesPayload(c *gin.Context, payload []byte) ([]byte, error) {
	restored, err := restoreGrokResponsesClientToolPayload(c, payload)
	if err != nil {
		return payload, fmt.Errorf("restore Grok Responses client tool response: %w", err)
	}
	restored, err = restoreOpenAIResponsesClientToolPayload(c, restored)
	if err != nil {
		return payload, fmt.Errorf("restore OpenAI Responses client tool response: %w", err)
	}
	restored, err = restoreOpenAIResponsesNamespacePayload(c, restored)
	if err != nil {
		return payload, fmt.Errorf("restore OpenAI namespace response: %w", err)
	}
	return restored, nil
}

// tkRestoreResponsesSSEPayload applies the canonical payload restore pipeline to
// one SSE data line, then reverses Codex tool-name aliases for that event.
func tkRestoreResponsesSSEPayload(c *gin.Context, payload []byte, eventType string) ([]byte, string, error) {
	restored, err := tkRestoreResponsesPayload(c, payload)
	if err != nil {
		return payload, eventType, err
	}
	restored = restoreCodexToolNamesFromSSEContext(c, restored, eventType)
	return restored, effectiveOpenAISSEEventType(restored, eventType), nil
}

// tkRestoreGatewayResponseBody applies the canonical payload restore pipeline and
// Codex tool-name aliases on a complete response body.
func tkRestoreGatewayResponseBody(c *gin.Context, body []byte) ([]byte, error) {
	restoredBody, err := tkRestoreResponsesPayload(c, body)
	if err != nil {
		return body, err
	}
	return restoreCodexToolNamesFromContext(c, restoredBody), nil
}

// tkRestoreGatewayResponseSSELine restores one SSE data line through the canonical
// payload pipeline plus per-event Codex alias reversal.
func tkRestoreGatewayResponseSSELine(
	c *gin.Context,
	dataBytes []byte,
	eventType string,
	line string,
) (string, []byte, string, string, error) {
	restoredData, restoredEventType, restoreErr := tkRestoreResponsesSSEPayload(c, dataBytes, eventType)
	if restoreErr != nil {
		return line, dataBytes, string(dataBytes), eventType, restoreErr
	}
	if !bytes.Equal(restoredData, dataBytes) {
		dataBytes = restoredData
		line = "data: " + string(restoredData)
		eventType = restoredEventType
	}
	return line, dataBytes, string(restoredData), eventType, nil
}
