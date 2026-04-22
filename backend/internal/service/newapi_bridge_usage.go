package service

import (
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/dto"
	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/relay/bridge"
)

// NewAPIRelayError wraps a New API adaptor error for handler-level JSON rendering.
type NewAPIRelayError struct {
	Err *newapitypes.NewAPIError
}

func (e *NewAPIRelayError) Error() string {
	if e == nil || e.Err == nil {
		return "newapi adaptor error"
	}
	return e.Err.Error()
}

func claudeUsageFromNewAPIDTO(u *dto.Usage) ClaudeUsage {
	if u == nil {
		return ClaudeUsage{}
	}
	in := u.PromptTokens
	out := u.CompletionTokens
	if u.InputTokens > 0 {
		in = u.InputTokens
	}
	if u.OutputTokens > 0 {
		out = u.OutputTokens
	}
	return ClaudeUsage{
		InputTokens:              in,
		OutputTokens:             out,
		CacheReadInputTokens:     u.PromptTokensDetails.CachedTokens,
		CacheCreationInputTokens: u.PromptTokensDetails.CachedCreationTokens,
		CacheCreation5mTokens:    u.ClaudeCacheCreation5mTokens,
		CacheCreation1hTokens:    u.ClaudeCacheCreation1hTokens,
	}
}

func openAIUsageFromNewAPIDTO(u *dto.Usage) OpenAIUsage {
	if u == nil {
		return OpenAIUsage{}
	}
	cu := claudeUsageFromNewAPIDTO(u)
	return OpenAIUsage{
		InputTokens:              cu.InputTokens,
		OutputTokens:             cu.OutputTokens,
		CacheCreationInputTokens: cu.CacheCreationInputTokens,
		CacheReadInputTokens:     cu.CacheReadInputTokens,
		ImageOutputTokens:        cu.ImageOutputTokens,
	}
}

func newAPIBridgeChannelInput(account *Account, userID int64, groupLabel string) bridge.ChannelContextInput {
	var mappingJSON string
	if m := account.GetModelMapping(); len(m) > 0 {
		if b, err := json.Marshal(m); err == nil {
			mappingJSON = string(b)
		}
	}
	baseURL := strings.TrimSpace(account.GetBaseURL())
	// Fifth platform `newapi`: OpenAI base URL fallback does not apply; credentials.base_url is required at create time.
	if baseURL == "" && account.Platform != PlatformNewAPI {
		baseURL = strings.TrimSpace(account.GetOpenAIBaseURL())
	}
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(account.GetOpenAIApiKey())
	}
	// Forwarding-affecting credentials that the new-api relay handlers read from
	// the Gin context (see PopulateContextKeys). Storing them under credentials
	// keeps them with the account that owns them; UI exposes them via the
	// AccountNewApiPlatformFields component (US-019). Empty values are skipped
	// downstream by PopulateContextKeys, so plain ascii whitespace-only inputs
	// effectively disable the feature, mirroring upstream new-api semantics.
	organization := strings.TrimSpace(account.GetCredential("openai_organization"))
	statusCodeMappingJSON := strings.TrimSpace(account.GetCredential("status_code_mapping"))
	return bridge.ChannelContextInput{
		ChannelType:           account.ChannelType,
		ChannelID:             int(account.ID),
		BaseURL:               baseURL,
		APIKey:                apiKey,
		ModelMappingJSON:      mappingJSON,
		Organization:          organization,
		StatusCodeMappingJSON: statusCodeMappingJSON,
		UserID:                int(userID),
		UserGroup:             groupLabel,
		UsingGroup:            groupLabel,
	}
}
