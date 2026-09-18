package antigravity

import "testing"

func TestResolveV1InternalRequestType_OmitsPlainText(t *testing.T) {
	t.Parallel()
	got := ResolveV1InternalRequestType("gemini-3.1-pro-preview", false, false, false)
	if got != "" {
		t.Fatalf("plain text: want empty requestType, got %q", got)
	}
}

func TestResolveV1InternalRequestType_ImageGen(t *testing.T) {
	t.Parallel()
	got := ResolveV1InternalRequestType("gemini-3.1-flash-image", false, false, false)
	if got != "image_gen" {
		t.Fatalf("image model: want image_gen, got %q", got)
	}
}

func TestResolveV1InternalRequestType_AgentWhenTools(t *testing.T) {
	t.Parallel()
	got := ResolveV1InternalRequestType("gemini-3.1-pro-preview", false, true, false)
	if got != "agent" {
		t.Fatalf("tools: want agent, got %q", got)
	}
}

func TestResolveV1InternalRequestType_WebSearch(t *testing.T) {
	t.Parallel()
	got := ResolveV1InternalRequestType("gemini-3.1-pro-preview", true, true, false)
	if got != "web_search" {
		t.Fatalf("web_search: want web_search, got %q", got)
	}
}

func TestGeminiRequestHasTools_PositiveAndNegative(t *testing.T) {
	t.Parallel()
	with := map[string]any{
		"tools": []any{
			map[string]any{
				"functionDeclarations": []any{
					map[string]any{"name": "fn"},
				},
			},
		},
	}
	without := map[string]any{"contents": []any{}}
	if !GeminiRequestHasTools(with) {
		t.Fatal("expected tools detected")
	}
	if GeminiRequestHasTools(without) {
		t.Fatal("expected no tools")
	}
}
