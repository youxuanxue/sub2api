import { countTokens } from 'gpt-tokenizer';

// This is an explicit fallback for a parked tool call, never provider usage.
// Hidden SDK context and image tokens are unavailable here. The final SDK
// total deducts these advances. Abandoned runs retain the estimate; an estimate
// above a final bucket is retained as a floor (no negative token/refund events).
export function estimateParkedTurn(body, content) {
  return {
    input_tokens: countTokens(JSON.stringify({ system: body?.system, messages: body?.messages, tools: body?.tools })),
    output_tokens: countTokens(JSON.stringify(content)),
    cache_read_input_tokens: 0,
    cache_creation_input_tokens: 0,
  };
}
