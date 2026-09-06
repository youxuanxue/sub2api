package service

import "net/http"

// tkOpenAIOAuth429ShouldSkipAccountStorm reports whether noteOpenAIOAuth429ForScheduling
// must skip account-wide 429 storm / parking side effects because the Codex OAuth
// 429 is model-scoped (healthy account window + spark/model usage_limit_reached).
func tkOpenAIOAuth429ShouldSkipAccountStorm(
	account *Account,
	headers http.Header,
	responseBody []byte,
	requestedModel ...string,
) bool {
	reqModel := ""
	if len(requestedModel) > 0 {
		reqModel = requestedModel[0]
	}
	return tkShouldOpenAICodex429BeModelScoped(account, headers, responseBody, reqModel)
}
