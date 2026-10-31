package batchcontract

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateRequestLinesAcceptsBothOpenAIEndpoints(t *testing.T) {
	for _, endpoint := range []string{EndpointChatCompletions, EndpointResponses} {
		lines := []RequestLine{{CustomID: "row-1", Method: "POST", URL: endpoint, Body: json.RawMessage(`{"model":"test"}`)}}
		if err := ValidateRequestLines(lines, endpoint); err != nil {
			t.Fatalf("%s: %v", endpoint, err)
		}
	}
}

func TestValidateRequestLinesRejectsDuplicateAndMismatchedEndpoint(t *testing.T) {
	lines := []RequestLine{{CustomID: "same", Method: "POST", URL: EndpointResponses, Body: json.RawMessage(`{}`)}, {CustomID: "same", Method: "POST", URL: EndpointResponses, Body: json.RawMessage(`{}`)}}
	err := ValidateRequestLines(lines, EndpointResponses)
	var ce *ContractError
	if !errors.As(err, &ce) || ce.Code != ErrorDuplicateCustomID {
		t.Fatalf("want duplicate error, got %v", err)
	}
	lines[1].CustomID = "other"
	lines[1].URL = EndpointChatCompletions
	err = ValidateRequestLines(lines, EndpointResponses)
	if !errors.As(err, &ce) || ce.Code != ErrorInvalidEndpoint {
		t.Fatalf("want endpoint error, got %v", err)
	}
	lines[1].URL = EndpointResponses
	lines[1].Body = json.RawMessage(`[]`)
	err = ValidateRequestLines(lines, EndpointResponses)
	if !errors.As(err, &ce) || ce.Code != ErrorInvalidRequest {
		t.Fatalf("want object body error, got %v", err)
	}
}

func TestBatchTransitions(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		ok       bool
	}{
		{BatchStatusValidating, BatchStatusInProgress, true},
		{BatchStatusInProgress, BatchStatusCancelling, true},
		{BatchStatusFinalizing, BatchStatusCompleted, true},
		{BatchStatusCompleted, BatchStatusInProgress, false},
		{BatchStatusValidating, BatchStatusCompleted, false},
	} {
		if got := CanTransitionBatch(tc.from, tc.to); got != tc.ok {
			t.Errorf("%s -> %s = %v, want %v", tc.from, tc.to, got, tc.ok)
		}
	}
}

func TestNativeFallbackRequiresNoSideEffects(t *testing.T) {
	capability := Capability{Managed: true, NativeBatch: true}
	if !CanFallbackToManaged(ExecutionModeNative, true, false, false, capability) {
		t.Fatal("expected explicit unsupported native request to fall back")
	}
	if CanFallbackToManaged(ExecutionModeNative, true, true, false, capability) || CanFallbackToManaged(ExecutionModeNative, true, false, true, capability) {
		t.Fatal("must not fall back after provider/item side effects")
	}
}

func TestExecutionSelectionValidatesMode(t *testing.T) {
	for _, mode := range []string{ExecutionModePending, ExecutionModeNative, ExecutionModeManaged} {
		if err := ValidateExecutionMode(mode); err != nil {
			t.Fatalf("%q: %v", mode, err)
		}
	}
	if err := ValidateExecutionMode("bogus"); err == nil {
		t.Fatal("expected invalid execution mode")
	}
}

func TestBatchJSONUsesStandardNullFields(t *testing.T) {
	raw, err := json.Marshal(Batch{ID: "batch_1", Object: "batch", Status: BatchStatusValidating})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" {
		t.Fatal("empty JSON")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["output_file_id"] != nil || decoded["errors"] != nil {
		t.Fatalf("nullable fields must encode null: %s", raw)
	}
}
