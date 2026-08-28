package handler

import (
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestExecuteOpenAIGeminiRouteUsesOnlyPlannedProfile(t *testing.T) {
	tests := []struct {
		name            string
		profile         protocolrouter.GeminiEndpointProfile
		wantAntigravity int
		wantVertex      int
		wantRequestID   string
	}{
		{name: "antigravity", profile: protocolrouter.GeminiEndpointAntigravityCloudCode, wantAntigravity: 1, wantRequestID: "ag"},
		{name: "vertex", profile: protocolrouter.GeminiEndpointVertexServiceAccount, wantVertex: 1, wantRequestID: "vertex"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			antigravityCalls := 0
			vertexCalls := 0
			got, err := executeOpenAIGeminiRoute(
				tc.profile,
				func() (*service.ForwardResult, error) {
					antigravityCalls++
					return &service.ForwardResult{RequestID: "ag", Usage: service.ClaudeUsage{InputTokens: 3}}, nil
				},
				func() (*service.ForwardResult, error) {
					vertexCalls++
					return &service.ForwardResult{RequestID: "vertex", Usage: service.ClaudeUsage{InputTokens: 5}}, nil
				},
			)
			if err != nil {
				t.Fatalf("executeOpenAIGeminiRoute: %v", err)
			}
			if antigravityCalls != tc.wantAntigravity || vertexCalls != tc.wantVertex {
				t.Fatalf("calls = antigravity:%d vertex:%d", antigravityCalls, vertexCalls)
			}
			if got == nil || got.RequestID != tc.wantRequestID || got.Usage.InputTokens == 0 {
				t.Fatalf("result = %#v", got)
			}
		})
	}
}

func TestExecuteOpenAIGeminiRouteRejectsUnknownProfileBeforeForward(t *testing.T) {
	calls := 0
	forward := func() (*service.ForwardResult, error) {
		calls++
		return &service.ForwardResult{}, nil
	}
	_, err := executeOpenAIGeminiRoute(protocolrouter.GeminiEndpointNone, forward, forward)
	if !errors.Is(err, service.ErrProtocolRouteUnavailable) {
		t.Fatalf("error = %v, want ErrProtocolRouteUnavailable", err)
	}
	if calls != 0 {
		t.Fatalf("forward calls = %d, want 0", calls)
	}
}
