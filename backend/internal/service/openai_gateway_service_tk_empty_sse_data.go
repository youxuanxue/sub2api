package service

import "strings"

// openAISSEDataPayloadIsEmpty reports whether an SSE `data:` payload is empty
// or whitespace-only.
//
// Such frames are technically legal per the SSE specification (the field is
// present but its value is the empty string), but real-world JSON-over-SSE
// consumers do not tolerate them. The OpenAI Python SDK in particular calls
// `json.loads()` unconditionally on every `data:` line's contents inside
// `openai/_streaming.py`, and crashes with
// `json.decoder.JSONDecodeError: Expecting value: line 1 column 1 (char 0)`
// the moment the payload is empty.
//
// Upstream `gpt-5.5` on `/v1/responses` has been observed emitting bare
// `data:\n` / `data: \n` frames at roughly a 5-10% rate, causing affected
// clients to abort the stream. The forward paths in
// `openai_gateway_service.go` (passthrough + main streaming) and
// `openai_gateway_chat_completions_raw.go` use this predicate to drop those
// frames before they reach the client.
//
// See upstream Wei-Shaw/sub2api#2298 and router-for-me/CLIProxyAPI#2460.
func openAISSEDataPayloadIsEmpty(data string) bool {
	return strings.TrimSpace(data) == ""
}
