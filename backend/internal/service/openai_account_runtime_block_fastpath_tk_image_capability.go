package service

import "context"

// tkHandleOpenAIImageCapabilityLossFastpath cools openai:image_generation on
// capability-loss 400 without blocking the whole account. Returns true when the
// caller must return false from handleOpenAIAccountUpstreamError (handled).
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
