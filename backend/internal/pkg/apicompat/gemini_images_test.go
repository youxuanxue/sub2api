package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestImagesToGeminiGeneration(t *testing.T) {
	for _, tc := range []struct{ name, extra, size, ratio string }{
		{"default", "", "2K", "1:1"},
		{"square", `,"size":"1024x1024"`, "1K", "1:1"},
		{"4K landscape", `,"size":"4K","aspect_ratio":"21:9","n":1,"response_format":"b64_json","stream":false`, "4K", "21:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, body, err := ImagesToGeminiGeneration([]byte(`{"model":"nano-2","prompt":"  blue cup  "` + tc.extra + `}`))
			require.NoError(t, err)
			require.Equal(t, "nano-2", model)
			require.Equal(t, "  blue cup  ", gjson.GetBytes(body, "contents.0.parts.0.text").String())
			require.Equal(t, tc.size, gjson.GetBytes(body, "generationConfig.imageConfig.imageSize").String())
			require.Equal(t, tc.ratio, gjson.GetBytes(body, "generationConfig.imageConfig.aspectRatio").String())
			require.JSONEq(t, `["TEXT","IMAGE"]`, gjson.GetBytes(body, "generationConfig.responseModalities").Raw)
		})
	}
}
func TestImagesToGeminiGenerationRejectsLossyOptions(t *testing.T) {
	for _, extra := range []string{`,"n":2`, `,"n":0`, `,"n":null`, `,"n":1.1`, `,"stream":true`, `,"stream":"false"`, `,"stream":null`, `,"quality":"hd"`, `,"background":"transparent"`, `,"response_format":"url"`, `,"size":"1024x1536"`, `,"size":"1024x1024","aspect_ratio":"16:9"`, `,"aspect_ratio":"7:3"`, `,"size":null`, `,"image":"https://example.invalid/input.png"`} {
		_, _, err := ImagesToGeminiGeneration([]byte(`{"model":"nano-2","prompt":"cup"` + extra + `}`))
		require.Error(t, err, extra)
	}
	for _, body := range []string{`null`, `[]`, `{}`, `{"model":"nano-2","prompt":" "}`, `{"model":"nano-2","prompt":5}`} {
		_, _, err := ImagesToGeminiGeneration([]byte(body))
		require.Error(t, err, body)
	}
}
func TestGeminiGenerationToImages(t *testing.T) {
	body := []byte(`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"done"},{"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}]}}]}`)
	out, err := GeminiGenerationToImages(body)
	require.NoError(t, err)
	require.Equal(t, "aW1hZ2U=", gjson.GetBytes(out, "data.0.b64_json").String())
	require.Equal(t, int64(1), gjson.GetBytes(out, "data.#").Int())
	require.Greater(t, gjson.GetBytes(out, "created").Int(), int64(0))
	var doc map[string]any
	require.NoError(t, json.Unmarshal(out, &doc))
	require.NotContains(t, doc, "candidates")
	for _, bad := range []string{`{}`, `{"candidates":[{"finishReason":"SAFETY"}]}`, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"cannot draw"}]}}]}`, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"!!!"}}]}}]}`} {
		_, err := GeminiGenerationToImages([]byte(bad))
		require.Error(t, err, bad)
	}
}
