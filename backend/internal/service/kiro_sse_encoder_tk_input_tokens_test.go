//go:build unit

package service

import (
	"bytes"
	"strings"
	"testing"
)

// Regression: the streaming Kiro SSE encoder must emit the locally-estimated
// prompt token count in message_start.usage.input_tokens. It used to hardcode 0,
// so every streamed Kiro request billed at input=0 on the prod relay (which bills
// off the parsed SSE usage) — systematic under-billing. See the inputTokens field
// doc + kiro_gateway_service.go.
func TestKiroSSEEncoder_MessageStartCarriesInputTokens(t *testing.T) {
	var buf bytes.Buffer
	enc := &kiroSSEEncoder{w: &buf, model: "claude-sonnet-4-6", msgID: "msg_x", inputTokens: 1234}

	enc.writeMessageStart()

	out := buf.String()
	if !strings.Contains(out, "message_start") {
		t.Fatalf("expected a message_start event, got: %s", out)
	}
	if !strings.Contains(out, `"input_tokens":1234`) {
		t.Fatalf("message_start must carry estimated input_tokens=1234, got: %s", out)
	}
	// message_start always reports output_tokens=0 (output accrues via message_delta).
	if !strings.Contains(out, `"output_tokens":0`) {
		t.Fatalf("message_start should report output_tokens=0, got: %s", out)
	}
}

// Default (zero) inputTokens still renders 0 — guards against a nil/omitted field
// regression and documents that an unset estimate degrades to the old behavior
// rather than panicking.
func TestKiroSSEEncoder_MessageStartZeroInputTokens(t *testing.T) {
	var buf bytes.Buffer
	enc := &kiroSSEEncoder{w: &buf, model: "claude-sonnet-4-6", msgID: "msg_y"}
	enc.writeMessageStart()
	if !strings.Contains(buf.String(), `"input_tokens":0`) {
		t.Fatalf("unset inputTokens should render 0, got: %s", buf.String())
	}
}
