package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// NewOpenAIGatewayServiceForUnitTests builds a minimal OpenAI gateway service for
// handler unit tests that only need billing/hold helpers (not full Wire DI).
func NewOpenAIGatewayServiceForUnitTests(billing *BillingService, cfg *config.Config) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		billingService: billing,
		cfg:            cfg,
	}
}
