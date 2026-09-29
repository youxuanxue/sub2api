package service

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIImagesAspectRatioAllowlist(t *testing.T) {
	t.Parallel()
	for _, ar := range openAIImagesAllowedAspectRatioList {
		got, err := normalizeOpenAIImagesAspectRatio(ar)
		require.NoError(t, err, ar)
		require.Equal(t, ar, got)
	}
	got, err := normalizeOpenAIImagesAspectRatio(" 16 : 9 ")
	require.NoError(t, err)
	require.Equal(t, "16:9", got)

	_, err = normalizeOpenAIImagesAspectRatio("4:3")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported aspect_ratio")

	_, err = normalizeOpenAIImagesAspectRatio("99:1")
	require.Error(t, err)

	_, err = normalizeOpenAIImagesAspectRatio("widescreen")
	require.Error(t, err)
}

func TestApplyOpenAIImagesAspectRatioMarkerIdempotent(t *testing.T) {
	t.Parallel()
	require.Equal(t, "draw a cat, marker AR=16:9", applyOpenAIImagesAspectRatioMarker("draw a cat", "16:9"))
	require.Equal(t, "draw a cat, marker AR=9:16", applyOpenAIImagesAspectRatioMarker("draw a cat, marker AR=9:16", "16:9"))
	require.Equal(t, "marker AR=1:1", applyOpenAIImagesAspectRatioMarker("", "1:1"))
	require.Equal(t, "plain", applyOpenAIImagesAspectRatioMarker("plain", ""))
}

func TestOpenAIImagesAspectRatioFromSize(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1:1", openAIImagesAspectRatioFromSize("1024x1024"))
	require.Equal(t, "3:2", openAIImagesAspectRatioFromSize("1536x1024"))
	require.Equal(t, "2:3", openAIImagesAspectRatioFromSize("1024x1536"))
	require.Equal(t, "16:9", openAIImagesAspectRatioFromSize("2048x1152"))
	require.Equal(t, "9:16", openAIImagesAspectRatioFromSize("1152x2048"))
	require.Equal(t, "16:9", openAIImagesAspectRatioFromSize("3840x2160"))
	require.Equal(t, "9:16", openAIImagesAspectRatioFromSize("2160x3840"))
	require.Equal(t, "1:1", openAIImagesAspectRatioFromSize("2048x2048"))
	require.Empty(t, openAIImagesAspectRatioFromSize("auto"))
	require.Empty(t, openAIImagesAspectRatioFromSize("1254x1254"))
}

func TestOpenAIImagesKnownSizeTable_SSOTInvariants(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, openAIImagesKnownSizeTable)
	studioByRatio := map[string]string{}
	for _, row := range openAIImagesKnownSizeTable {
		require.NotEmpty(t, row.Size)
		require.NotEmpty(t, row.Ratio)
		require.Equal(t, row.Ratio, openAIImagesAspectRatioFromSize(row.Size), row.Size)
		if !row.StudioChip {
			continue
		}
		if prev, ok := studioByRatio[row.Ratio]; ok {
			t.Fatalf("duplicate StudioChip for ratio %s: %s and %s", row.Ratio, prev, row.Size)
		}
		studioByRatio[row.Ratio] = row.Size
	}
	chips := ExportGPTImageStudioSizeChips()
	require.Len(t, chips, len(studioByRatio))
	for _, chip := range chips {
		require.Equal(t, studioByRatio[chip.Ratio], chip.Value)
	}
	require.Equal(t, []string{"1:1", "3:2", "2:3", "16:9", "9:16"}, openAIImagesAllowedAspectRatioList)
}

func TestParseOpenAIImagesRequestAspectRatioAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &OpenAIGatewayService{}

	okBody := []byte(`{"model":"gpt-image-2","prompt":"draw","aspect_ratio":"16:9"}`)
	okReq := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(okBody))
	okReq.Header.Set("Content-Type", "application/json")
	okRec := httptest.NewRecorder()
	okCtx, _ := gin.CreateTestContext(okRec)
	okCtx.Request = okReq
	parsed, err := svc.ParseOpenAIImagesRequest(okCtx, okBody)
	require.NoError(t, err)
	require.True(t, parsed.ExplicitAspectRatio)
	require.Equal(t, "16:9", parsed.AspectRatio)

	badBody := []byte(`{"model":"gpt-image-2","prompt":"draw","aspect_ratio":"4:3"}`)
	badReq := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(badBody))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	badCtx, _ := gin.CreateTestContext(badRec)
	badCtx.Request = badReq
	_, err = svc.ParseOpenAIImagesRequest(badCtx, badBody)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported aspect_ratio")

	typeBody := []byte(`{"model":"gpt-image-2","prompt":"draw","aspect_ratio":1}`)
	typeReq := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(typeBody))
	typeReq.Header.Set("Content-Type", "application/json")
	typeRec := httptest.NewRecorder()
	typeCtx, _ := gin.CreateTestContext(typeRec)
	typeCtx.Request = typeReq
	_, err = svc.ParseOpenAIImagesRequest(typeCtx, typeBody)
	require.Error(t, err)
	require.Contains(t, err.Error(), "aspect_ratio must be a string")
}

func TestBuildOpenAIImagesOAuthPayloadInjectsAspectRatioMarker(t *testing.T) {
	t.Parallel()
	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-2",
		Prompt:              "a tiny gray cube",
		AspectRatio:         "9:16",
		ExplicitAspectRatio: true,
		Body:                []byte(`{"model":"gpt-image-2","prompt":"a tiny gray cube","aspect_ratio":"9:16"}`),
	}
	body, target, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, chatgptCodexURL, target)
	require.Equal(t, "a tiny gray cube, marker AR=9:16", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.False(t, gjson.GetBytes(body, "aspect_ratio").Exists())
	require.Equal(t, "image_generation", gjson.GetBytes(body, "tools.0.type").String())
}

func TestBuildOpenAIImagesOAuthPayloadInjectsMarkerFromOfficialSize(t *testing.T) {
	t.Parallel()
	parsed := &OpenAIImagesRequest{
		Model:        "gpt-image-2",
		Prompt:       "a tiny gray cube",
		Size:         "1536x1024",
		ExplicitSize: true,
		Body:         []byte(`{"model":"gpt-image-2","prompt":"a tiny gray cube","size":"1536x1024"}`),
	}
	body, _, err := buildOpenAIImagesOAuthPayload(parsed, "gpt-image-2")
	require.NoError(t, err)
	require.Equal(t, "a tiny gray cube, marker AR=3:2", gjson.GetBytes(body, "input.0.content.0.text").String())
	require.Equal(t, "1536x1024", gjson.GetBytes(body, "tools.0.size").String())
}

func TestRewriteOpenAIImagesAspectRatioMarkerStripsField(t *testing.T) {
	t.Parallel()
	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-2",
		Prompt:              "draw",
		AspectRatio:         "1:1",
		ExplicitAspectRatio: true,
	}
	body := []byte(`{"model":"gpt-image-2","prompt":"draw","aspect_ratio":"1:1"}`)
	rewritten, _, err := rewriteOpenAIImagesModel(body, "application/json", "gpt-image-2")
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(rewritten, "aspect_ratio").Exists())

	marked, _, err := rewriteOpenAIImagesAspectRatioMarker(rewritten, "application/json", parsed)
	require.NoError(t, err)
	require.Equal(t, "draw, marker AR=1:1", gjson.GetBytes(marked, "prompt").String())
	require.False(t, gjson.GetBytes(marked, "aspect_ratio").Exists())
}

func TestRewriteOpenAIImagesAspectRatioMarkerDoesNotDeriveFromSize(t *testing.T) {
	t.Parallel()
	// API-key Images already honors size; do not pollute the prompt with a
	// size-derived soft marker on that path.
	parsed := &OpenAIImagesRequest{
		Model:        "gpt-image-2",
		Prompt:       "a tiny gray cube",
		Size:         "1536x1024",
		ExplicitSize: true,
	}
	body := []byte(`{"model":"gpt-image-2","prompt":"a tiny gray cube","size":"1536x1024"}`)
	marked, _, err := rewriteOpenAIImagesAspectRatioMarker(body, "application/json", parsed)
	require.NoError(t, err)
	require.Equal(t, "a tiny gray cube", gjson.GetBytes(marked, "prompt").String())
	require.Equal(t, "1536x1024", gjson.GetBytes(marked, "size").String())
}

func TestRewriteOpenAIImagesAspectRatioMarkerSkipsWhenAPIKeySizeExplicit(t *testing.T) {
	t.Parallel()
	// Hard size already sets canvas; do not also inject soft AR that can fight WxH.
	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-2",
		Prompt:              "a tiny gray cube",
		Size:                "1024x1024",
		ExplicitSize:        true,
		AspectRatio:         "16:9",
		ExplicitAspectRatio: true,
	}
	body := []byte(`{"model":"gpt-image-2","prompt":"a tiny gray cube","size":"1024x1024","aspect_ratio":"16:9"}`)
	marked, _, err := rewriteOpenAIImagesAspectRatioMarker(body, "application/json", parsed)
	require.NoError(t, err)
	require.Equal(t, "a tiny gray cube", gjson.GetBytes(marked, "prompt").String())
}

func TestRewriteOpenAIImagesMultipartAspectRatioMarker(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	require.NoError(t, writer.WriteField("model", "gpt-image-2"))
	require.NoError(t, writer.WriteField("prompt", "draw a cube"))
	require.NoError(t, writer.WriteField("aspect_ratio", "16:9"))
	require.NoError(t, writer.Close())

	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-2",
		Prompt:              "draw a cube",
		AspectRatio:         "16:9",
		ExplicitAspectRatio: true,
	}
	rewritten, contentType, err := rewriteOpenAIImagesAspectRatioMarker(buf.Bytes(), writer.FormDataContentType(), parsed)
	require.NoError(t, err)

	reader := multipart.NewReader(bytes.NewReader(rewritten), boundaryFromContentType(t, contentType))
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err != nil {
			break
		}
		name := part.FormName()
		var b bytes.Buffer
		_, _ = b.ReadFrom(part)
		_ = part.Close()
		fields[name] = b.String()
	}
	require.Equal(t, "draw a cube, marker AR=16:9", fields["prompt"])
	_, hasAR := fields["aspect_ratio"]
	require.False(t, hasAR)
}

func TestBuildOpenAIImagesResponsesRequestInjectsAspectRatioMarker(t *testing.T) {
	t.Parallel()
	parsed := &OpenAIImagesRequest{
		Model:               "gpt-image-1",
		Prompt:              "draw a cat",
		AspectRatio:         "2:3",
		ExplicitAspectRatio: true,
	}
	body, err := buildOpenAIImagesResponsesRequest(parsed, "gpt-image-1")
	require.NoError(t, err)
	require.Equal(t, "draw a cat, marker AR=2:3", gjson.GetBytes(body, "input.0.content.0.text").String())
}

func boundaryFromContentType(t *testing.T, contentType string) string {
	t.Helper()
	const prefix = "boundary="
	idx := strings.Index(contentType, prefix)
	require.GreaterOrEqual(t, idx, 0)
	return contentType[idx+len(prefix):]
}
