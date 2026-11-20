package protocolrouter

import (
	"errors"
	"testing"
)

func TestPlanMessagesToChatAdmitsVideoRejectsAudio(t *testing.T) {
	router := New(allTestAdapters())
	account := testAccount(t, ProtocolChatCompletions)

	for _, tc := range []struct {
		name    string
		profile RequestProfile
		wantOK  bool
	}{
		{"text", RequestProfile{ContentKinds: ContentText}, true},
		{"image", RequestProfile{ContentKinds: ContentText | ContentImage}, true},
		{"video", RequestProfile{ContentKinds: ContentText | ContentVideo}, true},
		{"audio", RequestProfile{ContentKinds: ContentText | ContentAudio}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := router.Plan(testRequest(t, ProtocolMessages, tc.profile), account)
			if !tc.wantOK {
				if !errors.Is(err, ErrNoLegalRoute) {
					t.Fatalf("Plan error = %v, want ErrNoLegalRoute", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.TargetProtocol() != ProtocolChatCompletions || plan.AdapterID() != AdapterMessagesToChat {
				t.Fatalf("Plan = %s/%s, want chat/%s", plan.TargetProtocol(), plan.AdapterID(), AdapterMessagesToChat)
			}
		})
	}
}
