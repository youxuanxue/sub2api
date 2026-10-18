package service

import "net/http"

const NoAvailableAccountsRetryAfterSeconds = "5"

func isAntigravityAPIKeyAccount(account *Account) bool {
	return account != nil && account.Platform == PlatformAntigravity && account.Type == AccountTypeAPIKey
}

func newUpstreamFailoverError(statusCode int, headers http.Header, responseBody []byte) *UpstreamFailoverError {
	return &UpstreamFailoverError{StatusCode: statusCode, ResponseBody: responseBody, ResponseHeaders: headers.Clone()}
}
