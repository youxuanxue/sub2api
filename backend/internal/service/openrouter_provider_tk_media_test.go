package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenRouterProviderImageRoute(t *testing.T) {
	if got := OpenRouterProviderImageRoute(PlatformAntigravity, "gemini-2.5-flash-image"); got != OpenRouterImageRouteAntigravityChat {
		t.Fatalf("antigravity gemini image=%q", got)
	}
	if got := OpenRouterProviderImageRoute(PlatformGrok, "grok-imagine-image"); got != OpenRouterImageRouteGrok {
		t.Fatalf("grok image=%q", got)
	}
	if got := OpenRouterProviderImageRoute(PlatformNewAPI, "imagen-4.0-fast-generate-001"); got != OpenRouterImageRouteOpenAICompat {
		t.Fatalf("imagen=%q", got)
	}
	if got := OpenRouterProviderImageRoute(PlatformAntigravity, "gemini-2.5-flash"); got != OpenRouterImageRouteOpenAICompat {
		t.Fatalf("antigravity text=%q", got)
	}
}

func TestOpenRouterProviderImageRoute_UniversalKeyIgnoresEmptyPlatform(t *testing.T) {
	// Repro: OR inference api_keys.group_id IS NULL → Group.Platform "".
	if got := OpenRouterProviderImageRoute("", "tokenkey/gemini-2.5-flash-image"); got != OpenRouterImageRouteAntigravityChat {
		t.Fatalf("tokenkey-prefixed gemini image with empty platform=%q", got)
	}
	if got := OpenRouterProviderImageRoute("", "tokenkey/gemini-3-pro-image"); got != OpenRouterImageRouteAntigravityChat {
		t.Fatalf("pro-image=%q", got)
	}
	if got := OpenRouterProviderImageRoute(PlatformOpenAI, "tokenkey/gemini-3.1-flash-image"); got != OpenRouterImageRouteAntigravityChat {
		t.Fatalf("wrong bound platform must not force openai path=%q", got)
	}
	if got := OpenRouterProviderImageRoute("", "tokenkey/grok-imagine-image"); got != OpenRouterImageRouteGrok {
		t.Fatalf("tokenkey-prefixed grok image=%q", got)
	}
	if got := OpenRouterProviderImageRoute("", "tokenkey/imagen-4.0-fast-generate-001"); got != OpenRouterImageRouteOpenAICompat {
		t.Fatalf("imagen stays openai-compat=%q", got)
	}
}

func TestTranslateOpenRouterImageToChatCompletions(t *testing.T) {
	body := []byte(`{"model":"gemini-2.5-flash-image","prompt":"a cat","aspect_ratio":"16:9"}`)
	out, err := TranslateOpenRouterImageToChatCompletions(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "model").String() != "gemini-2.5-flash-image" {
		t.Fatalf("model=%q", gjson.GetBytes(out, "model").String())
	}
	if gjson.GetBytes(out, "messages.0.content").String() != "a cat" {
		t.Fatalf("prompt=%q", gjson.GetBytes(out, "messages.0.content").String())
	}
	if gjson.GetBytes(out, "extra_body.google.image_config.aspect_ratio").String() != "16:9" {
		t.Fatalf("aspect_ratio=%q", gjson.GetBytes(out, "extra_body.google.image_config.aspect_ratio").String())
	}
	if gjson.GetBytes(out, "stream").Bool() {
		t.Fatal("stream must be false")
	}
}

func TestTranslateOpenRouterImageToChatCompletions_RequiresPrompt(t *testing.T) {
	_, err := TranslateOpenRouterImageToChatCompletions([]byte(`{"model":"gemini-2.5-flash-image"}`))
	if err == nil {
		t.Fatal("expected prompt required error")
	}
}

func TestTranslateChatCompletionsImageResponseToOpenRouter(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"rendered:\n![image](data:image/png;base64,aGVsbG8=)"}}],"usage":{"total_tokens":12}}`)
	out, err := TranslateChatCompletionsImageResponseToOpenRouter(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "data.0.b64_json").String() != "aGVsbG8=" {
		t.Fatalf("b64=%q", gjson.GetBytes(out, "data.0.b64_json").String())
	}
	if gjson.GetBytes(out, "data.0.media_type").String() != "image/png" {
		t.Fatalf("media_type=%q", gjson.GetBytes(out, "data.0.media_type").String())
	}
}

func TestTranslateChatCompletionsImageResponseToOpenRouter_ImageURLPart(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"data:image/webp;base64,d2VicA=="}}]}}]}`)
	out, err := TranslateChatCompletionsImageResponseToOpenRouter(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "data.0.b64_json").String() != "d2VicA==" {
		t.Fatalf("b64=%q", gjson.GetBytes(out, "data.0.b64_json").String())
	}
}

func TestTranslateChatCompletionsImageResponseToOpenRouter_MissingImage(t *testing.T) {
	_, err := TranslateChatCompletionsImageResponseToOpenRouter([]byte(`{"choices":[{"message":{"content":"text only"}}]}`))
	if err == nil {
		t.Fatal("expected missing image error")
	}
}

func TestTranslateOpenRouterImageRequestToOpenAI_ForcesB64JSON(t *testing.T) {
	body := []byte(`{"model":"tokenkey/imagen-4","prompt":"a cat","resolution":"2K","aspect_ratio":"16:9","n":2}`)
	out, err := TranslateOpenRouterImageRequestToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "response_format").String() != "b64_json" {
		t.Fatalf("response_format=%q", gjson.GetBytes(out, "response_format").String())
	}
	if gjson.GetBytes(out, "size").String() != "2K" {
		t.Fatalf("size=%q", gjson.GetBytes(out, "size").String())
	}
	if gjson.GetBytes(out, "n").Int() != 2 {
		t.Fatalf("n=%v", gjson.GetBytes(out, "n").Int())
	}
}

func TestTranslateOpenAIImageResponseToOpenRouter(t *testing.T) {
	body := []byte(`{"created":1710000001,"data":[{"b64_json":"aGVsbG8=","output_format":"png"}],"usage":{"total_tokens":10}}`)
	out, err := TranslateOpenAIImageResponseToOpenRouter(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "created").Int() != 1710000001 {
		t.Fatalf("created=%v", gjson.GetBytes(out, "created").Int())
	}
	if gjson.GetBytes(out, "data.0.b64_json").String() != "aGVsbG8=" {
		t.Fatalf("b64=%q", gjson.GetBytes(out, "data.0.b64_json").String())
	}
	if gjson.GetBytes(out, "data.0.media_type").String() != "image/png" {
		t.Fatalf("media_type=%q", gjson.GetBytes(out, "data.0.media_type").String())
	}
}

func TestTranslateOpenRouterVideoRequestToOpenAI(t *testing.T) {
	body := []byte(`{"model":"tokenkey/veo-3.1-generate-001","prompt":"sunset","duration":6,"resolution":"720p","aspect_ratio":"16:9"}`)
	out, err := TranslateOpenRouterVideoRequestToOpenAI(body)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "seconds").String() != "6" {
		t.Fatalf("seconds=%q", gjson.GetBytes(out, "seconds").String())
	}
	if gjson.GetBytes(out, "size").String() != "720p" {
		t.Fatalf("size=%q", gjson.GetBytes(out, "size").String())
	}
}

func TestBuildOpenRouterVideoSubmitResponse(t *testing.T) {
	out, err := BuildOpenRouterVideoSubmitResponse("vt_abc", "https://api.tokenkey.dev")
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "status").String() != "pending" {
		t.Fatalf("status=%q", gjson.GetBytes(out, "status").String())
	}
	if gjson.GetBytes(out, "polling_url").String() != "https://api.tokenkey.dev/openrouter/v1/videos/vt_abc" {
		t.Fatalf("polling_url=%q", gjson.GetBytes(out, "polling_url").String())
	}
}

func TestTranslateOpenAIVideoFetchToOpenRouter(t *testing.T) {
	body := []byte(`{"id":"vt_abc","status":"completed","video_url":"https://cdn.example/v.mp4"}`)
	out, err := TranslateOpenAIVideoFetchToOpenRouter("vt_abc", body, "https://api.tokenkey.dev")
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "status").String() != "completed" {
		t.Fatalf("status=%q", gjson.GetBytes(out, "status").String())
	}
	if gjson.GetBytes(out, "unsigned_urls.0").String() != "https://cdn.example/v.mp4" {
		t.Fatalf("unsigned_urls=%q", gjson.GetBytes(out, "unsigned_urls.0").String())
	}
}

func TestMapOpenAIVideoStatusToOpenRouter(t *testing.T) {
	cases := map[string]string{
		"queued":      "pending",
		"in_progress": "in_progress",
		"completed":   "completed",
		"failure":     "failed",
		"cancelled":   "cancelled",
	}
	for in, want := range cases {
		if got := mapOpenAIVideoStatusToOpenRouter(in); got != want {
			t.Fatalf("%s => %q want %q", in, got, want)
		}
	}
}
