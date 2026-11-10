package protocolrouter

import (
	"bytes"
	"testing"
)

// The memoized Responses→Chat downgrade must return exactly what a fresh
// computation returns, for every copy of the request and across repeated Plans.
func TestResponsesToChatDowngradeMemoMatchesFreshComputation(t *testing.T) {
	body := []byte(`{"model":"m","prompt_cache_key":"k","input":[{"role":"user","content":[{"type":"input_text","text":"hi","cache_control":{"type":"ephemeral"}}]}]}`)
	request, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "m", false, body)
	if err != nil {
		t.Fatal(err)
	}
	fresh, freshAdjustment, freshOK := computeResponsesToChatDowngrade(request)
	copyOfRequest := request
	for i, candidate := range []CanonicalRequest{request, copyOfRequest, request} {
		got, adjustment, ok := downgradeResponsesToChat(candidate)
		if ok != freshOK || adjustment != freshAdjustment || got.Digest() != fresh.Digest() || !bytes.Equal(got.Body(), fresh.Body()) || got.Profile() != fresh.Profile() {
			t.Fatalf("call %d diverged from fresh downgrade", i)
		}
	}
	if !request.BodyEquals(body) {
		t.Fatal("downgrade must not mutate the original request body")
	}

	router := New(allTestAdapters())
	account := testAccount(t, ProtocolChatCompletions)
	first, err := router.Plan(request, account)
	if err != nil {
		t.Fatal(err)
	}
	second, err := router.Plan(copyOfRequest, account)
	if err != nil {
		t.Fatal(err)
	}
	if first.EffectiveRequestDigest() != second.EffectiveRequestDigest() || first.Adjustment() != second.Adjustment() || first.CompatibilityRank() != second.CompatibilityRank() {
		t.Fatal("repeated Plans over one request must agree")
	}
}

// A request built without the memo (zero value) still downgrades correctly.
func TestResponsesToChatDowngradeWithoutMemo(t *testing.T) {
	body := []byte(`{"model":"m","prompt_cache_key":"k","input":"hi"}`)
	request, err := ParseCanonicalRequest(ProtocolResponses, ResponsesPathRoot, "m", false, body)
	if err != nil {
		t.Fatal(err)
	}
	want, wantAdjustment, wantOK := computeResponsesToChatDowngrade(request)
	request.memo = nil
	got, adjustment, ok := downgradeResponsesToChat(request)
	if ok != wantOK || adjustment != wantAdjustment || got.Digest() != want.Digest() {
		t.Fatal("memo-less downgrade diverged")
	}
}

// Matches is true exactly when NewCanonicalRequest(input) would build a request
// with the same digest, profile and validation provenance.
func TestCanonicalRequestMatchesNewCanonicalRequest(t *testing.T) {
	base := CanonicalRequestInput{InboundProtocol: ProtocolResponses, RequestedModel: "m", ResponsesPath: ResponsesPathNone,
		Profile: RequestProfile{Tools: true, ContentKinds: ContentText}, Body: []byte(`{"model":"m","input":"hi"}`), BodyJSONValidated: true}
	request, err := NewCanonicalRequest(base)
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(*CanonicalRequestInput){
		"same":           func(*CanonicalRequestInput) {},
		"padded model":   func(in *CanonicalRequestInput) { in.RequestedModel = " m " },
		"explicit root":  func(in *CanonicalRequestInput) { in.ResponsesPath = ResponsesPathRoot },
		"other model":    func(in *CanonicalRequestInput) { in.RequestedModel = "n" },
		"compact":        func(in *CanonicalRequestInput) { in.ResponsesPath = ResponsesPathCompact },
		"stream":         func(in *CanonicalRequestInput) { in.Profile.Stream = true },
		"unvalidated":    func(in *CanonicalRequestInput) { in.BodyJSONValidated = false },
		"other body":     func(in *CanonicalRequestInput) { in.Body = []byte(`{"model":"n","input":"hi"}`) },
		"other protocol": func(in *CanonicalRequestInput) { in.InboundProtocol = ProtocolChatCompletions },
	}
	for name, mutate := range variants {
		input := base
		mutate(&input)
		rebuilt, err := NewCanonicalRequest(input)
		if err != nil {
			t.Fatal(err)
		}
		equivalent := rebuilt.Digest() == request.Digest() && rebuilt.Profile() == request.Profile() &&
			rebuilt.bodyJSONValidated == request.bodyJSONValidated && rebuilt.policyFacts == request.policyFacts
		if request.Matches(input) != equivalent {
			t.Fatalf("%s: Matches=%v, equivalent=%v", name, request.Matches(input), equivalent)
		}
	}
	if (CanonicalRequest{}).Matches(base) {
		t.Fatal("a zero request never matches")
	}
}
