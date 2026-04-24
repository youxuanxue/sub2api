//go:build unit

package service

import (
	"testing"
)

// TestTkBridgeEndpointEnabled_Truth: tkBridgeEndpointEnabled is a pure
// endpoint-name allow-list; the nil-account / channel_type / kill-switch
// preconditions live in the upstream-shape accountUsesNewAPIAdaptorBridge
// caller. Keeping this helper free of those checks is what makes the
// upstream-shape file's diff a single line; a regression that re-adds them
// here would re-bloat the dispatch gate.
func TestTkBridgeEndpointEnabled_Truth(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"video_submit_enabled", BridgeEndpointVideoSubmit, true},
		{"video_fetch_enabled", BridgeEndpointVideoFetch, true},
		{"empty_disabled", "", false},
		{"unknown_disabled", "totally_made_up", false},
		// The four upstream-shape endpoints MUST NOT be answered here —
		// they are handled by the explicit case in
		// accountUsesNewAPIAdaptorBridge. If tkBridgeEndpointEnabled
		// starts answering true for them we have duplicated source of
		// truth and a maintenance hazard.
		{"chat_completions_handled_upstream", BridgeEndpointChatCompletions, false},
		{"responses_handled_upstream", BridgeEndpointResponses, false},
		{"embeddings_handled_upstream", BridgeEndpointEmbeddings, false},
		{"images_handled_upstream", BridgeEndpointImages, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tkBridgeEndpointEnabled(tc.endpoint); got != tc.want {
				t.Fatalf("tkBridgeEndpointEnabled(%q)=%v want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}
