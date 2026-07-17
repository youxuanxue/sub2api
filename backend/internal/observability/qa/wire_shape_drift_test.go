//go:build unit

package qa

// Drift guard: the trajectory projector dispatches on the NORMALIZED inbound
// endpoint that this (qa) package stamps onto every QARecord. The two literals
// live in different packages (trajectory cannot import qa without a cycle), so
// this test pins the contract — if normalizeInboundEndpoint ever changes a
// qaEndpoint* value, the projector would silently mis-dispatch (or skip) those
// records; this test fails first instead.

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/observability/trajectory"
)

func TestCaptureEndpointsMapToExpectedWireShape(t *testing.T) {
	cases := []struct {
		name     string
		platform string
		endpoint string
		want     trajectory.WireShape
	}{
		{"messages", domain.PlatformAnthropic, qaEndpointMessages, trajectory.WireAnthropicMessages},
		{"chat_completions", domain.PlatformOpenAI, qaEndpointChatCompletions, trajectory.WireOpenAIChat},
		{"responses", domain.PlatformOpenAI, qaEndpointResponses, trajectory.WireOpenAIResponses},
		{"gemini_models", domain.PlatformGemini, qaEndpointGeminiModels, trajectory.WireGemini},
		// Non-conversation endpoints must remain unprojectable (skip, not garbage).
		{"embeddings", domain.PlatformOpenAI, qaEndpointEmbeddings, trajectory.WireUnknown},
		{"images", domain.PlatformOpenAI, qaEndpointImagesGenerations, trajectory.WireUnknown},
		{"video", domain.PlatformOpenAI, qaEndpointVideoGenerations, trajectory.WireUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trajectory.WireShapeForRecord(&ent.QARecord{Platform: tc.platform, InboundEndpoint: tc.endpoint})
			if got != tc.want {
				t.Fatalf("capture endpoint %q (platform %q) → wire shape %q, want %q — normalizeInboundEndpoint and trajectory.WireShapeForRecord drifted",
					tc.endpoint, tc.platform, got, tc.want)
			}
		})
	}
}
