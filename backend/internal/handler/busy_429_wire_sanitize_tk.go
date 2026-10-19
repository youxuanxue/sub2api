package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// tkSanitizeBusy429WireMessage is the handler-layer choke for public error copy.
// It first rewrites legacy HTTP 429 pool/account phrases to
// service.TkClientFacingBusy429Message, then strips upstream plan/product
// branding (Agent Plan / Token Plan / vendor docs) via the unsupported SSOT.
// Ops keeps original detail (MarkOpsStreamError is first-wins).
func tkSanitizeBusy429WireMessage(c *gin.Context, status int, errType, message string) string {
	public, sanitized := service.TkSanitizeBusy429WireMessage(status, message)
	if sanitized {
		if errType == "" {
			errType = "api_error"
		}
		service.TkAnnotateBusy429Ops(c, errType, message)
		message = public
	} else {
		message = public
	}
	return tkSanitizeUpstreamProductWireMessage(c, status, errType, message)
}

// tkSanitizeBusy429StatusMessage is for writers that only have status+message
// (e.g. Google error envelopes).
func tkSanitizeBusy429StatusMessage(c *gin.Context, status int, message string) string {
	return tkSanitizeBusy429WireMessage(c, status, "api_error", message)
}
