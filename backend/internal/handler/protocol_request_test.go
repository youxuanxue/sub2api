package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func TestProtocolPlanEndpointUsesSelectedPlanURL(t *testing.T) {
	if got, want := protocolPlanEndpoint("https://relay.example.test/v1/responses/compact"), "/v1/responses/compact"; got != want {
		t.Fatalf("protocolPlanEndpoint = %q, want %q", got, want)
	}
}

func TestNewCanonicalProtocolRequestProfilesMessagesFeatures(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4-6",
		"stream":true,
		"tool_choice":{"type":"tool","name":"lookup"},
		"thinking":{"type":"enabled","budget_tokens":1024},
		"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"image","source":{"type":"base64","data":"AA=="}}]}]
	}`)

	req, err := newCanonicalProtocolRequest(protocolrouter.ProtocolMessages, protocolrouter.ResponsesPathNone, "claude-sonnet-4-6", true, body)
	if err != nil {
		t.Fatalf("newCanonicalProtocolRequest: %v", err)
	}
	profile := req.Profile()
	if !profile.Stream || profile.ToolChoice != protocolrouter.ToolChoiceNamed {
		t.Fatalf("stream/tool profile = %v/%q", profile.Stream, profile.ToolChoice)
	}
	if profile.Reasoning != protocolrouter.ReasoningEffort || profile.PromptCache != protocolrouter.PromptCachePlacement {
		t.Fatalf("reasoning/cache profile = %q/%q", profile.Reasoning, profile.PromptCache)
	}
	if profile.ContentKinds&protocolrouter.ContentText == 0 || profile.ContentKinds&protocolrouter.ContentImage == 0 {
		t.Fatalf("content kinds = %b, want text+image", profile.ContentKinds)
	}
}

func TestNewCanonicalProtocolRequestProfilesResponsesContinuationAndPath(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"previous_response_id":"resp_123",
		"prompt_cache_key":"cache-1",
		"reasoning":{"effort":"high","summary":"auto"},
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"},{"type":"input_file","file_id":"file_1"}]}]
	}`)

	req, err := newCanonicalProtocolRequest(protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathCompact, "gpt-5.4", false, body)
	if err != nil {
		t.Fatalf("newCanonicalProtocolRequest: %v", err)
	}
	profile := req.Profile()
	if req.ResponsesPath() != protocolrouter.ResponsesPathCompact {
		t.Fatalf("responses path = %q", req.ResponsesPath())
	}
	if profile.Continuation != protocolrouter.ContinuationPreviousResponse {
		t.Fatalf("continuation = %q", profile.Continuation)
	}
	if profile.Reasoning != protocolrouter.ReasoningSummary || profile.PromptCache != protocolrouter.PromptCacheKey {
		t.Fatalf("reasoning/cache profile = %q/%q", profile.Reasoning, profile.PromptCache)
	}
	if profile.ContentKinds&protocolrouter.ContentFile == 0 {
		t.Fatalf("content kinds = %b, want file", profile.ContentKinds)
	}
}

func TestNewCanonicalProtocolRequestRejectsMalformedBody(t *testing.T) {
	_, err := newCanonicalProtocolRequest(protocolrouter.ProtocolChatCompletions, protocolrouter.ResponsesPathNone, "gpt-5.4", false, []byte(`{"model":`))
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestNewCanonicalProtocolRequestMarksToolsAndUnknownContent(t *testing.T) {
	req, err := newCanonicalProtocolRequest(
		protocolrouter.ProtocolMessages,
		protocolrouter.ResponsesPathNone,
		"claude-test",
		false,
		[]byte(`{"model":"claude-test","tools":[{"name":"lookup"}],"messages":[{"role":"user","content":[{"type":"server_tool_use","name":"lookup"}]}]}`),
	)
	if err != nil {
		t.Fatalf("newCanonicalProtocolRequest: %v", err)
	}
	profile := req.Profile()
	if !profile.Tools {
		t.Fatal("profile.Tools = false, want true")
	}
	if profile.ContentKinds&protocolrouter.ContentUnknown == 0 {
		t.Fatalf("profile.ContentKinds = %v, want ContentUnknown", profile.ContentKinds)
	}
}
