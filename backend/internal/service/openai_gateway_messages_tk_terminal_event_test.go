package service

import "testing"

// TestIsOpenAICompatResponsesTerminalEvent_FullSet pins the six terminal event
// types the Responses-stream consumer must treat as end-of-stream. The
// predicate drives the OpenAI-compat Anthropic Messages handler and the
// OpenAI-compat Chat Completions handler (openai_gateway_messages.go and
// openai_gateway_chat_completions.go); it must stay symmetric with the broader
// helpers in gateway_service.go (openAIStreamEventIsTerminal),
// openai_ws_forwarder.go and openai_ws_v2/passthrough_relay.go. Without
// response.cancelled / response.canceled in this set, an upstream that ends
// the SSE with a legitimate cancellation event falls through to "stream usage
// incomplete: missing terminal event", emitting a spurious openai.forward_failed
// ops_error_logs entry and short-circuiting downstream finalization. See
// upstream Wei-Shaw/sub2api#1322.
func TestIsOpenAICompatResponsesTerminalEvent_FullSet(t *testing.T) {
	terminal := []string{
		"response.completed",
		"response.done",
		"response.incomplete",
		"response.failed",
		"response.cancelled",
		"response.canceled",
	}
	for _, ev := range terminal {
		if !isOpenAICompatResponsesTerminalEvent(ev) {
			t.Fatalf("expected %q to be a terminal event", ev)
		}
		// strings.TrimSpace inside the predicate tolerates surrounding whitespace.
		if !isOpenAICompatResponsesTerminalEvent("  " + ev + " \t") {
			t.Fatalf("expected whitespace-padded %q to be a terminal event", ev)
		}
	}

	nonTerminal := []string{
		"",
		"response.created",
		"response.output_text.delta",
		"response.output_item.added",
		"response.reasoning_summary_text.delta",
		"response.function_call_arguments.delta",
		"completed",            // bare suffix is not the upstream contract.
		"response.completed.x", // prefix is not enough.
		"RESPONSE.COMPLETED",   // upstream contract is lowercase; case sensitivity is required.
	}
	for _, ev := range nonTerminal {
		if isOpenAICompatResponsesTerminalEvent(ev) {
			t.Fatalf("expected %q to NOT be a terminal event", ev)
		}
	}
}
