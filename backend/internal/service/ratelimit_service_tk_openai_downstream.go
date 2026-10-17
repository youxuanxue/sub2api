package service

func tkOpenAICompatRetryableOnSameAccount(account *Account, statusCode int, upstreamMsg string, responseBody []byte, includeTransient bool) bool {
	if account == nil || !account.IsPoolMode() {
		return false
	}
	if tkIsAccountCapacityFailure(account, statusCode, upstreamMsg, responseBody) {
		return false
	}
	if account.IsPoolModeRetryableStatus(statusCode) {
		return true
	}
	return includeTransient && isOpenAITransientProcessingError(statusCode, upstreamMsg, responseBody)
}
