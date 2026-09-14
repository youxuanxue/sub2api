//go:build unit

package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine"
)

func TestCapabilityForEndpointSupported_Truth(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		want     bool
	}{
		{"video_submit_enabled", BridgeEndpointVideoSubmit, true},
		{"video_fetch_enabled", BridgeEndpointVideoFetch, true},
		{"chat_completions_enabled", BridgeEndpointChatCompletions, true},
		{"responses_enabled", BridgeEndpointResponses, true},
		{"embeddings_enabled", BridgeEndpointEmbeddings, true},
		{"images_enabled", BridgeEndpointImages, true},
		{"empty_disabled", "", false},
		{"unknown_disabled", "totally_made_up", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := engine.CapabilityForEndpoint(tc.endpoint); got != tc.want {
				t.Fatalf("CapabilityForEndpointSupported(%q)=%v want %v", tc.endpoint, got, tc.want)
			}
		})
	}
}
