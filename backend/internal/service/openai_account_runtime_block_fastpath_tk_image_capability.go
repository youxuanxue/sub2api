package service

import "context"

// tkHandleOpenAIImageCapabilityLossFastpath cools openai:image_generation on
// capability-loss 400 without blocking the whole account. Returns true when the
// caller must treat the account as failover-eligible for THIS request (so a
// mixed image pool can try the next account) while leaving chat schedulable.
func (s *OpenAIGatewayService) tkHandleOpenAIImageCapabilityLossFastpath(
	stateCtx context.Context,
	account *Account,
	statusCode int,
	responseBody []byte,
) bool {
	if !isOpenAIImageCapabilityLoss400(statusCode, responseBody) {
		return false
	}
	if s != nil && s.rateLimitService != nil {
		_ = s.rateLimitService.HandleOpenAIImageCapabilityLoss400(stateCtx, account, statusCode, responseBody)
	}
	return true
}
