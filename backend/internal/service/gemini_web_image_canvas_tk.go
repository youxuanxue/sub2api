package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

const geminiWebCanvasMaxBytes = 32 << 20

var geminiWebCanvasSlots = make(chan struct{}, 1)

type geminiWebImageCanvas struct{ size, ratio string }

func prepareGeminiWebImageCanvas(account *Account, model string, body []byte) ([]byte, *geminiWebImageCanvas, error) {
	// The relay must preserve intent: resizing happens only beside the local Worker.
	if !isGeminiWebAccount(account) || !geminiWebRuntimeBound(account) || !domain.IsGeminiProImageModel(model) {
		return body, nil, nil
	}
	next, ok := protocolrouter.GeminiWebImageUpstreamBody(body, true)
	if !ok {
		return nil, nil, fmt.Errorf("unsupported Gemini Web image size")
	}
	size := gjson.GetBytes(body, "generationConfig.imageConfig.imageSize").String()
	if size == "" {
		return body, nil, nil
	}
	return next, &geminiWebImageCanvas{size: size, ratio: gjson.GetBytes(body, "generationConfig.imageConfig.aspectRatio").String()}, nil
}

// One postprocessing owner for native, Images and converted Chat/Responses/Messages.
// Google Web streams one buffered SSE response. Process it before any downstream
// write or settlement; errors never trigger a second generation.
func (canvas *geminiWebImageCanvas) apply(ctx context.Context, resp *http.Response) error {
	if canvas == nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	select {
	case geminiWebCanvasSlots <- struct{}{}:
		defer func() { <-geminiWebCanvasSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, geminiWebCanvasMaxBytes+1))
	_ = resp.Body.Close()
	if err != nil || len(raw) > geminiWebCanvasMaxBytes {
		return fmt.Errorf("gemini Web image response exceeds canvas budget")
	}
	streaming := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if streaming {
		var payload []byte
		for _, line := range bytes.Split(raw, []byte("\n")) {
			line = bytes.TrimSpace(line)
			if bytes.HasPrefix(line, []byte("data:")) {
				if payload != nil {
					return fmt.Errorf("unexpected multi-event Web image response")
				}
				payload = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			}
		}
		raw = payload
	}
	out, err := canvas.transform(raw)
	if err != nil {
		return err
	}
	if streaming {
		out = append(append([]byte("data: "), out...), []byte("\n\n")...)
	}
	if len(out) > geminiWebCanvasMaxBytes {
		return fmt.Errorf("processed Gemini image exceeds response budget")
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", fmt.Sprint(len(out)))
	return nil
}

func (canvas *geminiWebImageCanvas) transform(raw []byte) ([]byte, error) {
	var root map[string]any
	if json.Unmarshal(raw, &root) != nil {
		return nil, fmt.Errorf("invalid Gemini Web image response")
	}
	candidates, _ := root["candidates"].([]any)
	count := 0
	for _, value := range candidates {
		candidate, _ := value.(map[string]any)
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		for _, value := range parts {
			part, _ := value.(map[string]any)
			inline, _ := part["inlineData"].(map[string]any)
			if inline == nil {
				continue
			}
			count++
			if count > 4 {
				return nil, fmt.Errorf("too many Gemini Web images")
			}
			encoded, _ := inline["data"].(string)
			source, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("invalid Gemini Web image bytes")
			}
			config, _, err := image.DecodeConfig(bytes.NewReader(source))
			if err != nil {
				return nil, fmt.Errorf("invalid Gemini Web image dimensions")
			}
			width, height, ok := apicompat.GeminiImageCanvas(canvas.ratio, canvas.size)
			if !ok {
				if canvas.ratio != "" {
					return nil, fmt.Errorf("unsupported Gemini image canvas")
				}
				scale := map[string]int{"1K": 1, "2K": 2, "4K": 4}[canvas.size]
				if scale == 0 {
					return nil, fmt.Errorf("unsupported Gemini image size")
				}
				width, height = config.Width*scale/2, config.Height*scale/2
			}
			processed, err := resizeImageExactWithinBounds(source, width, height, true, 6400, 20_000_000)
			if err != nil {
				return nil, err
			}
			_, format, err := image.DecodeConfig(bytes.NewReader(processed))
			if err != nil {
				return nil, err
			}
			mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}[format]
			if mime == "" {
				return nil, fmt.Errorf("unsupported processed image format")
			}
			inline["mimeType"], inline["data"] = mime, base64.StdEncoding.EncodeToString(processed)
		}
	}
	if count == 0 {
		return nil, fmt.Errorf("gemini Web returned no image for canvas processing")
	}
	return json.Marshal(root)
}
