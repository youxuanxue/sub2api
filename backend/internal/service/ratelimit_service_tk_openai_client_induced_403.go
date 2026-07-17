package service

import "strings"

// openAIClientInducedCapability403Keywords match OpenAI 403 bodies that are a
// CLIENT-induced model/endpoint capability rejection, not an account-level
// problem. The canonical trigger is a `POST /v1/embeddings` request carrying a
// chat-only model (e.g. `gpt-5.5`): OpenAI replies 403 "You are not allowed to
// generate embeddings from this model". The same request will 403 on EVERY
// account in the pool, so it tells us nothing about this account's health.
//
// Match is case-insensitive; the haystack is upstreamMsg + responseBody.
//
// TK (prod P0 2026-06-25, routing_capacity_rejection spike on gpt-5.5): a single
// malformed `/v1/embeddings` request with model=gpt-5.5 hit OAuth Codex accounts
// 9 (GPT-pro1) and 73 (GPT-pro3) at 17:13:39Z. handleOpenAI403 treated the 403 as
// account-level and wrote a 10-minute temp_unschedulable on both — the very
// accounts that serve the bulk of gpt-5.5 CHAT traffic (~9.7k ok rows/24h
// combined). With the two API-key mirror accounts (us3/us6) simultaneously
// saturated and us4/GPT-pro2 already dead, the openai compat pool emptied for the
// full 10-minute cooldown, producing 628 "no available accounts" 429s
// (routing_capacity_rejection) until the cooldown lapsed at 17:23:3xZ and the
// accounts auto-recovered. This is the OpenAI sibling of the Anthropic
// client-induced-400 skip (Wei-Shaw/sub2api#2608, tkIsAnthropicClientInducedBadRequest):
// a deterministic per-request rejection must fail the in-flight request back to
// the caller WITHOUT cooling a shared, healthy account.
var openAIClientInducedCapability403Keywords = []string{
	"embeddings from this model",
	"not allowed to generate embeddings",
}

// tkIsOpenAIClientInducedCapability403 reports whether an OpenAI 403 is a
// client-induced model/endpoint capability rejection (see keyword doc above).
// Such 403s must skip the openai_403 counter increment AND the
// temp_unschedulable write, so a caller sending a wrong-endpoint request cannot
// poison a shared OAuth account for all other models. The in-flight request
// still fails over (handleOpenAI403 returns shouldDisable=true) because this
// account cannot serve THIS request — but every other account would 403 it too,
// so failover just surfaces the deterministic error to the caller.
func tkIsOpenAIClientInducedCapability403(upstreamMsg string, responseBody []byte) string {
	haystack := strings.ToLower(upstreamMsg + " " + string(responseBody))
	return matchTempUnschedKeyword(haystack, openAIClientInducedCapability403Keywords)
}
