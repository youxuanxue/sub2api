package logredact

import (
	"bytes"
	"encoding/json"
	"strings"
)

// InputFormat describes the bounded wire representation recognized by the
// capture redaction path. Unknown input is deliberately retained verbatim.
type InputFormat uint8

const (
	FormatUnknown InputFormat = iota
	FormatJSON
	FormatSSE
)

// RedactOptions contains the redaction knobs that affect a one-pass result.
// The options are intentionally small so a capture-local memo can key results
// by input digest and options without retaining content process-wide.
type RedactOptions struct {
	ExtraKeys []string
}

// RedactionResult is the classified, redacted representation of one bounded
// payload. JSON values are returned as decoded Go values so callers can place
// them directly in an existing blob object; SSE and unknown values are strings
// so their framing and byte representation remain unchanged.
type RedactionResult struct {
	Format InputFormat
	Value  any
}

// RedactOnePass classifies and redacts one bounded payload. JSON is decoded
// and traversed once. Identified SSE is sent through the framing-preserving
// SSE path. Inputs that are neither valid JSON nor identifiable SSE are kept
// unchanged, avoiding the legacy regexp fallback for unknown formats.
func RedactOnePass(raw []byte, options RedactOptions) RedactionResult {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return RedactionResult{Format: FormatUnknown, Value: map[string]any{}}
	}
	extra := options.ExtraKeys
	if json.Valid(trimmed) {
		value, err := RedactJSONValue(trimmed, extra...)
		if err == nil {
			return RedactionResult{Format: FormatJSON, Value: value}
		}
	}
	// Keep the original bytes for identified SSE. Trimming an SSE stream would
	// discard its terminal event separator and violate the framing contract.
	text := string(raw)
	if isIdentifiedSSE(text) {
		return RedactionResult{Format: FormatSSE, Value: RedactSSE(text, extra...)}
	}
	return RedactionResult{Format: FormatUnknown, Value: string(raw)}
}

func isIdentifiedSSE(input string) bool {
	// A stream is identified once it contains at least one event with exactly
	// one valid JSON data field. Other events ([DONE], comments, or extensions)
	// remain under RedactSSE's framing-preserving handling.
	for len(input) > 0 {
		start, length := sseEventSeparator(input)
		end := start
		if end < 0 {
			end = len(input)
		}
		if _, _, ok := sseJSONPayloadRange(input[:end]); ok {
			return true
		}
		if start < 0 {
			break
		}
		input = input[start+length:]
	}
	return false
}

// String returns the string form used by SSE and unknown results. It is
// intentionally strict so JSON callers do not silently lose structure.
func (r RedactionResult) String() string {
	if value, ok := r.Value.(string); ok {
		return value
	}
	encoded, err := json.Marshal(r.Value)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(encoded))
}
