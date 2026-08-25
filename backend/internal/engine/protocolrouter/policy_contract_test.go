package protocolrouter

import "testing"

func TestPolicyContractProtocolIdentifiers(t *testing.T) {
	got := AllProtocols()
	want := []Protocol{ProtocolMessages, ProtocolChatCompletions, ProtocolResponses}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
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
		{in: ProtocolChatCompletions, supported: []Protocol{ProtocolMessages, ProtocolResponses}, want: ProtocolResponses},
		{in: ProtocolResponses, supported: []Protocol{ProtocolMessages, ProtocolChatCompletions}, want: ProtocolChatCompletions},
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
