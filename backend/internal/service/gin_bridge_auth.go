package service

import "github.com/gin-gonic/gin"

// BridgeGinAuthContextKey stores optional auth labels for New API bridge relay (avoids service importing middleware).
const BridgeGinAuthContextKey = "sub2api_bridge_gin_auth"

// GinBridgeAuth carries user/group labels for New API RelayInfo population.
type GinBridgeAuth struct {
	UserID    int64
	GroupName string
}

func bridgeAuthFromGin(c *gin.Context) GinBridgeAuth {
	if c == nil {
		return GinBridgeAuth{}
	}
	v, ok := c.Get(BridgeGinAuthContextKey)
	if !ok {
		return GinBridgeAuth{}
	}
	a, ok := v.(GinBridgeAuth)
	if !ok {
		return GinBridgeAuth{}
	}
	return a
}
