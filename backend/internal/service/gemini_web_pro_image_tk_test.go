//go:build unit

package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/stretchr/testify/require"
)

func TestGeminiWebProImageRoutesWithoutAntigravityDowngrade(t *testing.T) {
	for _, relay := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			for _, model := range []string{"gemini-3-pro-image", "nano-banana-pro", "nano-pro"} {
				web := webCandidateAccount(relay)
				target := "gemini-web-nano-banana-pro"
				if relay {
					target = model
				}
				web.Credentials["model_mapping"] = map[string]any{model: target}
				attachTestNativeDeclaredCapability(&web)
				ag := geminiImagesTestAccount()
				ag.Priority = -1
				ag.Credentials["model_mapping"] = map[string]any{"*": "gemini-3.1-flash-image"}
				group := grp(740, PlatformGemini, 1, false)
				group.AllowImageGeneration = true
				group.AllowMessagesDispatch = true
				r, _, key := globalCandidateFixture([]Group{group}, []Account{ag, web})
				if direct {
					key.RoutingMode = RoutingModeDirect
					key.Group = &group
					key.GroupID = &group.ID
				}
				for _, tc := range []struct {
					shape      UniversalShape
					path, body string
				}{
					{ShapeGemini, "/v1beta/models/" + model + ":generateContent", `{"contents":[{"role":"user","parts":[{"text":"blue cup"}]}],"generationConfig":{"responseModalities":["IMAGE"],"imageConfig":{"aspectRatio":"4:3"}}}`},
					{ShapeOpenAIImages, "/v1/images/generations", `{"prompt":"blue cup","aspect_ratio":"21:9","size":"4K"}`},
					{ShapeOpenAIChat, "/v1/chat/completions", `{"messages":[{"role":"user","content":"blue cup"}],"extra_body":{"google":{"image_config":{"image_size":"4K","aspect_ratio":"21:9"}}}}`},
					{ShapeOpenAIChat, "/v1/responses", `{"input":"blue cup"}`},
					{ShapeAnthropicMessages, "/v1/messages", `{"max_tokens":1024,"messages":[{"role":"user","content":"blue cup"}]}`},
				} {
					var body map[string]any
					require.NoError(t, json.Unmarshal([]byte(tc.body), &body))
					if tc.shape != ShapeGemini {
						body["model"] = model
					}
					raw, _ := json.Marshal(body)
					_, state, err := r.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, model, raw, "", "")
					require.NoError(t, err, "relay=%t direct=%t model=%s path=%s", relay, direct, model, tc.path)
					require.Equal(t, web.ID, state.current.account.ID)
					require.Equal(t, target, state.current.plan.ResolvedModel())
					require.Equal(t, protocolrouter.ProtocolGeminiGenerateContent, state.current.plan.TargetProtocol())
				}
			}
		}
	}
}

func TestGeminiWebProImagesRejectUnsupportedOptions(t *testing.T) {
	web := webCandidateAccount(false)
	web.Credentials["model_mapping"] = map[string]any{"gemini-3-pro-image": "gemini-web-nano-banana-pro"}
	attachTestNativeDeclaredCapability(&web)
	group := grp(740, PlatformGemini, 1, false)
	group.AllowImageGeneration = true
	r, _, key := globalCandidateFixture([]Group{group}, []Account{web})
	for _, extra := range []string{`,"size":"8K"`, `,"aspect_ratio":"7:3"`, `,"n":2`, `,"image":"data:image/png;base64,AA=="`} {
		body := []byte(`{"model":"gemini-3-pro-image","prompt":"cup"` + extra + `}`)
		_, state, err := r.PrepareCandidateRequest(context.Background(), key, ShapeOpenAIImages, "/v1/images/generations", "gemini-3-pro-image", body, "", "")
		require.Error(t, err, string(body))
		require.Nil(t, state)
	}
}

func TestAntigravityProMappingPolicyIsSharedWithPlan(t *testing.T) {
	ag := geminiImagesTestAccount()
	for _, model := range domain.GeminiProImageModelIDs() {
		ag.Credentials["model_mapping"] = map[string]any{model: "gemini-3.1-flash-image"}
		require.False(t, accountAdmitsRequestedModel(&ag, model, nil), model)
	}
}

func TestGeminiWebProReferenceAdmissionAcrossProtocols(t *testing.T) {
	web := webCandidateAccount(false)
	web.Credentials["model_mapping"] = map[string]any{"gemini-3-pro-image": "gemini-web-nano-banana-pro"}
	attachTestNativeDeclaredCapability(&web)
	group := grp(740, PlatformGemini, 1, false)
	group.AllowImageGeneration = true
	group.AllowMessagesDispatch = true
	r, _, key := globalCandidateFixture([]Group{group}, []Account{web})
	reference := canvasTestImage(t)
	for _, tc := range []struct {
		name, path string
		shape      UniversalShape
	}{
		{"native", "/v1beta/models/gemini-3-pro-image:generateContent", ShapeGemini},
		{"messages", "/v1/messages", ShapeAnthropicMessages},
		{"chat", "/v1/chat/completions", ShapeOpenAIChat},
		{"responses", "/v1/responses", ShapeOpenAIChat},
	} {
		for _, failure := range []string{"", "invalid_reference", "tools"} {
			invalid := failure != ""
			data := reference
			if failure == "invalid_reference" {
				data = "not-an-image"
			}
			body := map[string]any{"model": "gemini-3-pro-image", "generationConfig": map[string]any{"candidateCount": 1, "imageConfig": map[string]any{"imageSize": "4K", "aspectRatio": "21:9"}}}
			switch tc.name {
			case "native":
				delete(body, "model")
				body["contents"] = []any{map[string]any{"parts": []any{map[string]any{"text": "make cup red"}, map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": data}}}}}
			case "messages":
				body["max_tokens"] = 1024
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "make cup red"}, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data}}}}}
			case "chat":
				url := "data:image/png;base64," + data
				if failure == "invalid_reference" {
					url = "https://example.com/reference.png"
				}
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "make cup red"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}}}}
			case "responses":
				url := "data:image/png;base64," + data
				if failure == "invalid_reference" {
					url = "https://example.com/reference.png"
				}
				body["input"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "make cup red"}, map[string]any{"type": "input_image", "image_url": url}}}}
			}
			if failure == "tools" {
				body["tools"] = []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}}
			}
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			_, state, err := r.PrepareCandidateRequest(context.Background(), key, tc.shape, tc.path, "gemini-3-pro-image", raw, "", "")
			if invalid {
				require.Error(t, err, tc.name)
				require.Nil(t, state, tc.name)
				continue
			}
			require.NoError(t, err, tc.name)
			require.Equal(t, web.ID, state.current.account.ID, tc.name)
			require.Equal(t, "gemini-web-nano-banana-pro", state.current.plan.ResolvedModel(), tc.name)
		}
	}
}
