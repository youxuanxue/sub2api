package protocolrouter

import (
	"errors"
	"testing"
)

func TestPreservesResponsesToChat_AlignsWithConverterSurface(t *testing.T) {
	router := New(allTestAdapters())
	account := testAccount(t, ProtocolChatCompletions)

	for _, tc := range []struct {
		name    string
		profile RequestProfile
		wantOK  bool
	}{
		{"text", RequestProfile{ContentKinds: ContentText}, true},
		{"tools", RequestProfile{Tools: true, ToolChoice: ToolChoiceAuto, ContentKinds: ContentText}, true},
		{"image", RequestProfile{ContentKinds: ContentText | ContentImage}, true},
		{"video", RequestProfile{ContentKinds: ContentText | ContentVideo}, true},
		{"tools_image", RequestProfile{Tools: true, ContentKinds: ContentText | ContentImage}, true},
		{"reasoning_effort", RequestProfile{Reasoning: ReasoningEffort, ContentKinds: ContentText}, true},
		{"prompt_cache", RequestProfile{PromptCache: PromptCacheKey, ContentKinds: ContentText}, true},
		{"audio", RequestProfile{ContentKinds: ContentAudio}, false},
		{"continuation", RequestProfile{Continuation: ContinuationPreviousResponse, ContentKinds: ContentText}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := testRequest(t, ProtocolResponses, tc.profile)
			if got := PreservesResponsesToChat(req); got != tc.wantOK {
				t.Fatalf("PreservesResponsesToChat = %v, want %v", got, tc.wantOK)
			}
			plan, err := router.Plan(req, account)
			if !tc.wantOK {
				if !errors.Is(err, ErrNoLegalRoute) {
					t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.TargetProtocol() != ProtocolChatCompletions || plan.AdapterID() != AdapterResponsesToChat {
				t.Fatalf("Plan = %s/%s, want chat/%s", plan.TargetProtocol(), plan.AdapterID(), AdapterResponsesToChat)
			}
		})
	}
}
