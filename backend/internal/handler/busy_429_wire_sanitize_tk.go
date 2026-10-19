package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkSanitizeBusy429WireMessage is the handler-layer choke for public HTTP 429
// capacity / upstream-limit copy. When a legacy pool/account phrase would hit
// the wire, rewrite to service.TkClientFacingBusy429Message and ensure ops
// still keeps the original detail (MarkOpsStreamError is first-wins).
func tkSanitizeBusy429WireMessage(c *gin.Context, status int, errType, message string) string {
	public, sanitized := service.TkSanitizeBusy429WireMessage(status, message)
	if !sanitized {
		return public
	}
	if errType == "" {
		errType = "api_error"
	}
	service.TkAnnotateBusy429Ops(c, errType, message)
	return public
}

// tkSanitizeBusy429StatusMessage is for writers that only have status+message
// (e.g. Google error envelopes).
func tkSanitizeBusy429StatusMessage(c *gin.Context, status int, message string) string {
	return tkSanitizeBusy429WireMessage(c, status, "api_error", message)
}
