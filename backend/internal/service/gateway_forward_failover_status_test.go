package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGatewayShouldFailoverUpstreamError_424IsFailoverEligible(t *testing.T) {
	svc := &GatewayService{}

	assert.True(t, svc.shouldFailoverUpstreamError(http.StatusFailedDependency),
		"424 from CloudWise-class relays is a provider dependency failure and must leave the current account")
}

func TestGatewayShouldFailoverUpstreamError_ExistingCodesStillWork(t *testing.T) {
	svc := &GatewayService{}

	failoverCodes := []int{401, 403, 424, 429, 529, 500, 502, 503, 504}
	for _, code := range failoverCodes {
		assert.True(t, svc.shouldFailoverUpstreamError(code), "status %d should trigger failover", code)
	}

	nonFailoverCodes := []int{200, 201, 400, 404, 408, 422}
	for _, code := range nonFailoverCodes {
		assert.False(t, svc.shouldFailoverUpstreamError(code), "status %d should NOT trigger failover", code)
	}
}
