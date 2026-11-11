package protocolrouter

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// Freeze the pre-memo implementations so equivalence tests do not share the
// optimized computation's validation or short-circuit logic.
func freshChatToResponsesPreservation(req CanonicalRequest) bool {
	var chat apicompat.ChatCompletionsRequest
	if json.Unmarshal(req.body, &chat) != nil || (len(chat.Thinking) > 0 && string(chat.Thinking) != "null") || req.profile.PromptCache == PromptCachePlacement {
		return false
	}
	return preservesMessagesToResponsesContent(req) && req.profile.Continuation == ContinuationNone
}

func freshChatToMessagesPreservation(req CanonicalRequest) bool {
	if req.profile.ContentKinds != ContentText || req.profile.Continuation != ContinuationNone ||
		req.profile.PromptCache == PromptCacheKey {
		return false
	}
	var chat apicompat.ChatCompletionsRequest
	var root map[string]json.RawMessage
	if json.Unmarshal(req.body, &root) != nil || len(root["reasoning"]) > 0 {
		return false
	}
	if json.Unmarshal(req.body, &chat) != nil || apicompat.ValidateChatToAnthropic(&chat) != nil {
		return false
	}
	return req.profile.Reasoning == ReasoningNone || (len(chat.Thinking) > 0 && string(chat.Thinking) != "null" && chat.ReasoningEffort == "")
}

func TestChatPreservationMemoMatchesFreshComputation(t *testing.T) {
	bodies := map[string]string{
		"valid":              `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		"thinking":           `{"thinking":{"type":"enabled","budget_tokens":1024}}`,
		"thinking null":      `{"thinking":null}`,
		"thinking spaced":    `{"thinking":  null }`,
		"thinking false":     `{"thinking":false}`,
		"thinking unknown":   `{"thinking":{"type":"enabled","budget_tokens":1024,"extra":true}}`,
		"thinking small":     `{"thinking":{"type":"enabled","budget_tokens":1}}`,
		"thinking effort":    `{"thinking":{"type":"adaptive"},"reasoning_effort":"high"}`,
		"reasoning effort":   `{"reasoning_effort":"high"}`,
		"reasoning null":     `{"reasoning":null}`,
		"reasoning object":   `{"reasoning":{}}`,
		"response format":    `{"response_format":{"type":"json_object"}}`,
		"invalid role":       `{"messages":[{"role":"invalid","content":"hi"}]}`,
		"invalid tool":       `{"tools":[{"type":"function","function":{"name":"f","parameters":[]}}]}`,
		"invalid tool turn":  `{"messages":[{"role":"tool","content":"hi","tool_call_id":"missing"}]}`,
		"wrong messages":     `{"messages":{}}`,
		"wrong model":        `{"model":42}`,
		"wrong max tokens":   `{"max_tokens":"bad"}`,
		"wrong stream":       `{"stream":"true"}`,
		"wrong nested tool":  `{"tools":[{"function":{"name":42}}]}`,
		"duplicate thinking": `{"thinking":{},"thinking":null}`,
		"case folded field":  `{"Thinking":{}}`,
		"invalid json":       `{"messages":`,
		"trailing json":      `{} {}`,
		"array":              `[]`,
		"null":               `null`,
		"empty":              ``,
	}
	profiles := map[string]RequestProfile{
		"text":          {ContentKinds: ContentText},
		"placement":     {ContentKinds: ContentText, PromptCache: PromptCachePlacement},
		"cache key":     {ContentKinds: ContentText, PromptCache: PromptCacheKey},
		"continuation":  {ContentKinds: ContentText, Continuation: ContinuationPreviousResponse},
		"conversation":  {ContentKinds: ContentText, Continuation: ContinuationConversation},
		"reasoning":     {ContentKinds: ContentText, Reasoning: ReasoningEffort},
		"image":         {ContentKinds: ContentText | ContentImage},
		"audio":         {ContentKinds: ContentAudio},
		"unknown":       {ContentKinds: ContentUnknown},
		"empty content": {},
	}
	for bodyName, body := range bodies {
		for profileName, profile := range profiles {
			t.Run(bodyName+"/"+profileName, func(t *testing.T) {
				request := CanonicalRequest{body: []byte(body), profile: profile, memo: &requestMemo{}}
				wantResponses := freshChatToResponsesPreservation(request)
				wantMessages := freshChatToMessagesPreservation(request)
				copyOfRequest := request
				withoutMemo := request
				withoutMemo.memo = nil
				for _, candidate := range []CanonicalRequest{request, copyOfRequest, request, withoutMemo} {
					if got := preservesChatToResponses(candidate); got != wantResponses {
						t.Fatalf("Responses preservation = %v, fresh = %v (memo=%v)", got, wantResponses, candidate.memo != nil)
					}
					if got := preservesChatToMessages(candidate); got != wantMessages {
						t.Fatalf("Messages preservation = %v, fresh = %v (memo=%v)", got, wantMessages, candidate.memo != nil)
					}
				}
				if !request.BodyEquals([]byte(body)) {
					t.Fatal("preservation checks mutated the request body")
				}
			})
		}
	}
	if preservesChatToResponses(CanonicalRequest{}) || preservesChatToMessages(CanonicalRequest{}) {
		t.Fatal("zero request must not preserve either conversion")
	}
}

func TestChatToResponsesProfileRejectsBeforeMemo(t *testing.T) {
	for name, profile := range map[string]RequestProfile{
		"placement":    {ContentKinds: ContentText, PromptCache: PromptCachePlacement},
		"content":      {ContentKinds: ContentAudio},
		"continuation": {ContentKinds: ContentText, Continuation: ContinuationPreviousResponse},
	} {
		t.Run(name, func(t *testing.T) {
			request := CanonicalRequest{body: []byte(`{}`), profile: profile, memo: &requestMemo{}}
			if preservesChatToResponses(request) {
				t.Fatal("incompatible profile preserved")
			}
			notComputed := false
			request.memo.chatToResponsesOnce.Do(func() { notComputed = true })
			if !notComputed {
				t.Fatal("profile rejection inspected the body memo")
			}
		})
	}
}

func chatMemoPlanFixture(tb testing.TB, target Protocol) (*Router, CanonicalRequest, []AccountSnapshot) {
	tb.Helper()
	body, err := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]string{
			{"role": "user", "content": strings.Repeat("memo payload ", 2048)},
		},
	})
	if err != nil {
		tb.Fatal(err)
	}
	request, err := ParseCanonicalRequest(ProtocolChatCompletions, ResponsesPathNone, "m", false, body)
	if err != nil {
		tb.Fatal(err)
	}
	accounts := make([]AccountSnapshot, 64)
	for i := range accounts {
		accounts[i], err = NewAccountSnapshot(AccountSnapshotInput{
			AccountID: int64(i + 1), CapabilityKey: "memo-test",
			SupportedProtocols: []Protocol{target}, ResolvedModel: "m",
			CustomBaseURL: "https://relay.example.test/v1",
			ModelAllowed:  map[Protocol]bool{target: true},
			Transports:    []TransportID{TransportHTTP},
		})
		if err != nil {
			tb.Fatal(err)
		}
	}
	return New(allTestAdapters()), request, accounts
}

func TestChatPreservationMemoConcurrentPlansAndAllocations(t *testing.T) {
	for _, target := range []Protocol{ProtocolResponses, ProtocolMessages} {
		t.Run(string(target), func(t *testing.T) {
			router, request, accounts := chatMemoPlanFixture(t, target)
			var wg sync.WaitGroup
			for _, account := range accounts {
				wg.Add(1)
				go func(candidate CanonicalRequest, account AccountSnapshot) {
					defer wg.Done()
					plan, err := router.Plan(candidate, account)
					if err != nil || plan.TargetProtocol() != target || plan.AccountID() != account.accountID ||
						plan.EffectiveRequestDigest() != request.Digest() || plan.Adjustment() != "" {
						t.Errorf("unexpected plan: target=%s account=%d err=%v", plan.TargetProtocol(), plan.AccountID(), err)
					}
				}(request, account)
			}
			wg.Wait()
			preserves := preservesChatToResponses
			once := &request.memo.chatToResponsesOnce
			if target == ProtocolMessages {
				preserves = preservesChatToMessages
				once = &request.memo.chatToMessagesOnce
			}
			once.Do(func() { t.Error("Plan did not populate the preservation memo") })
			if allocs := testing.AllocsPerRun(100, func() {
				if !preserves(request) {
					t.Fatal("warmed preservation rejected request")
				}
			}); allocs != 0 {
				t.Fatalf("warmed body check allocated %g times, want zero", allocs)
			}
			planAll := func(candidate CanonicalRequest) {
				for _, account := range accounts {
					if _, err := router.Plan(candidate, account); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Each measured pass starts with a cold memo: only its first
			// candidate should pay for validation of the shared body.
			cached := testing.AllocsPerRun(10, func() {
				candidate := request
				candidate.memo = &requestMemo{}
				planAll(candidate)
			})
			withoutMemo := request
			withoutMemo.memo = nil
			uncached := testing.AllocsPerRun(10, func() { planAll(withoutMemo) })
			bodyAllocs := testing.AllocsPerRun(10, func() {
				if !preserves(withoutMemo) {
					t.Fatal("fresh preservation rejected request")
				}
			})
			t.Logf("64-account pass allocations: memo=%g no_memo=%g fresh_body=%g", cached, uncached, bodyAllocs)
			// Allow one body's worth of overhead for the memo itself, but
			// require the other candidate decodes to disappear.
			if bodyAllocs == 0 || uncached-cached < float64(len(accounts)-2)*bodyAllocs {
				t.Fatalf("body decode allocations still scale per account: memo=%g no_memo=%g", cached, uncached)
			}
		})
	}
}

func BenchmarkChatPreservationMemoPlanAcrossAccounts(b *testing.B) {
	for _, target := range []Protocol{ProtocolResponses, ProtocolMessages} {
		b.Run(string(target), func(b *testing.B) {
			router, request, accounts := chatMemoPlanFixture(b, target)
			for _, memoized := range []bool{false, true} {
				name := "no_memo"
				if memoized {
					name = "memo"
				}
				b.Run(name, func(b *testing.B) {
					b.ReportAllocs()
					for i := 0; i < b.N; i++ {
						candidate := request
						candidate.memo = nil
						if memoized {
							candidate.memo = &requestMemo{}
						}
						for _, account := range accounts {
							if _, err := router.Plan(candidate, account); err != nil {
								b.Fatal(err)
							}
						}
					}
				})
			}
		})
	}
}

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
