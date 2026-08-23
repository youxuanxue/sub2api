package service

import "github.com/gin-gonic/gin"

func tkRestoreGrokOpenAIResponsesSSEPayload(
	c *gin.Context,
	dataBytes []byte,
	eventType string,
	line string,
) (string, []byte, string, string, error) {
	return tkRestoreGatewayResponseSSELine(c, dataBytes, eventType, line)
}
