package service

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// tkRestorePassthroughSSEPayload applies the canonical Responses restore pipeline
// to one passthrough SSE data payload.
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
	line, dataBytes, trimmedData, _, restoreErr := tkRestoreGatewayResponseSSELine(c, dataBytes, rawEventType, line)
	return line, dataBytes, trimmedData, restoreErr
}
