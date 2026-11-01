package batchadapter

import "encoding/json"

// decodeOpenAIUsage accepts both Chat Completions and Responses usage names.
// The Responses API calls the counters input/output_tokens while Chat uses
// prompt/completion_tokens; both are normalized to the Batch usage contract.
func decodeOpenAIUsage(body []byte) Usage {
	var envelope struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			InputDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return Usage{}
	}
	u := envelope.Usage
	if u.InputTokens == 0 {
		u.InputTokens = u.PromptTokens
	}
	if u.OutputTokens == 0 {
		u.OutputTokens = u.CompletionTokens
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	cached := u.InputDetails.CachedTokens
	if cached == 0 {
		cached = u.PromptDetails.CachedTokens
	}
	return Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CacheReadInputTokens: cached}
}

// Antigravity uses the Gemini usageMetadata names on its cloudcode wire. A
// fallback to OpenAI names keeps the adapter compatible with its OpenAI relay.
func decodeAntigravityUsage(body []byte) Usage {
	var envelope struct {
		UsageMetadata struct {
			PromptTokenCount     int `json:"promptTokenCount"`
			CandidatesTokenCount int `json:"candidatesTokenCount"`
			TotalTokenCount      int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return Usage{}
	}
	u := envelope.UsageMetadata
	if u.TotalTokenCount == 0 {
		u.TotalTokenCount = u.PromptTokenCount + u.CandidatesTokenCount
	}
	if u.TotalTokenCount == 0 {
		return decodeOpenAIUsage(body)
	}
	return Usage{InputTokens: u.PromptTokenCount, OutputTokens: u.CandidatesTokenCount, TotalTokens: u.TotalTokenCount}
}

// Kiro's relay is currently disabled for Managed Batch, but keeping its
// decoder here makes enabling the capability a data-only change after an
// account-level probe succeeds.
func decodeKiroUsage(body []byte) Usage { return decodeOpenAIUsage(body) }
