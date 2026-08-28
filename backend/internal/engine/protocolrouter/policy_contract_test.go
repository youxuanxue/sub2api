package protocolrouter

import "testing"

func TestPolicyContractProtocolIdentifiers(t *testing.T) {
	got := AllProtocols()
	want := []Protocol{ProtocolMessages, ProtocolChatCompletions, ProtocolResponses, ProtocolGeminiGenerateContent}
	if len(got) != len(want) {
		t.Fatalf("AllProtocols() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllProtocols() = %v, want %v", got, want)
		}
	}
	if !ProtocolGeminiGenerateContent.Valid() {
		t.Fatalf("AllProtocols() = %v, want %v", got, want)
	}
}

func TestPolicyContractPlannerUsesIdentityFirstAndFixedFallbackOrder(t *testing.T) {
	tests := []struct {
		in        Protocol
		supported []Protocol
		want      Protocol
	}{
		{in: ProtocolMessages, supported: []Protocol{ProtocolChatCompletions, ProtocolResponses, ProtocolMessages}, want: ProtocolMessages},
		{in: ProtocolMessages, supported: []Protocol{ProtocolChatCompletions, ProtocolResponses}, want: ProtocolResponses},
		{in: ProtocolMessages, supported: []Protocol{ProtocolGeminiGenerateContent}, want: ProtocolGeminiGenerateContent},
		{in: ProtocolChatCompletions, supported: []Protocol{ProtocolMessages, ProtocolResponses}, want: ProtocolResponses},
		{in: ProtocolChatCompletions, supported: []Protocol{ProtocolGeminiGenerateContent}, want: ProtocolGeminiGenerateContent},
		{in: ProtocolResponses, supported: []Protocol{ProtocolMessages, ProtocolChatCompletions}, want: ProtocolChatCompletions},
		{in: ProtocolResponses, supported: []Protocol{ProtocolGeminiGenerateContent}, want: ProtocolGeminiGenerateContent},
		{in: ProtocolGeminiGenerateContent, supported: []Protocol{ProtocolMessages, ProtocolChatCompletions, ProtocolResponses, ProtocolGeminiGenerateContent}, want: ProtocolGeminiGenerateContent},
	}
	for _, tt := range tests {
		t.Run(string(tt.in)+"_to_"+string(tt.want), func(t *testing.T) {
			plan, err := New(allTestAdapters()).Plan(testRequest(t, tt.in, RequestProfile{ContentKinds: ContentText}), testAccount(t, tt.supported...))
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if plan.TargetProtocol() != tt.want {
				t.Fatalf("Plan target = %q, want %q", plan.TargetProtocol(), tt.want)
			}
		})
	}
}
