package service

import (
	"context"
	"strings"

	newapiconstant "github.com/QuantumNous/new-api/constant"
)

// NewAPIOpenAIImageEditsAccount reports a newapi API-key account whose channel
// speaks the OpenAI Images edits protocol. Other newapi channels stay on their
// own adaptors and are not forwarded through the native edits client.
func NewAPIOpenAIImageEditsAccount(account *Account) bool {
	if account == nil || account.Platform != PlatformNewAPI || account.Type != AccountTypeAPIKey {
		return false
	}
	if account.ChannelType != newapiconstant.ChannelTypeOpenAI {
		return false
	}
	return strings.TrimSpace(account.GetCredential("base_url")) != ""
}

// CandidateNewAPIOpenAIImageEditsAccount returns the already selected execution
// account when it can take POST /v1/images/edits. Admission may select this
// account inside an OpenAI group; the route gate must follow that account.
func CandidateNewAPIOpenAIImageEditsAccount(ctx context.Context) *Account {
	request := CandidateRequestFromContext(ctx)
	if request == nil || request.current == nil || !NewAPIOpenAIImageEditsAccount(request.current.account) {
		return nil
	}
	return request.current.account
}

func newAPIOpenAIImageEditsBaseURL(account *Account) string {
	if !NewAPIOpenAIImageEditsAccount(account) {
		return ""
	}
	return strings.TrimSpace(account.GetCredential("base_url"))
}
