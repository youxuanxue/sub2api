//go:build unit

package kiro

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func png1x1() []byte {
	// Minimal valid 1x1 PNG (IHDR+IDAT+IEND).
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x00, 0x03, 0x00, 0x01, 0x00, 0x05, 0xfe,
		0xd4, 0xef, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
		0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
}

func TestSniffKiroImageFormat(t *testing.T) {
	if got := sniffKiroImageFormat(png1x1()); got != "png" {
		t.Fatalf("png sniff: got %q", got)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0x00, 0x10}
	if got := sniffKiroImageFormat(jpeg); got != "jpeg" {
		t.Fatalf("jpeg sniff: got %q", got)
	}
	gif := []byte("GIF89a............")
	if got := sniffKiroImageFormat(gif); got != "gif" {
		t.Fatalf("gif sniff: got %q", got)
	}
	webp := append([]byte("RIFF"), make([]byte, 4)...)
	webp = append(webp, []byte("WEBP")...)
	if got := sniffKiroImageFormat(webp); got != "webp" {
		t.Fatalf("webp sniff: got %q", got)
	}
	if got := sniffKiroImageFormat([]byte("not-an-image")); got != "" {
		t.Fatalf("opaque sniff: got %q", got)
	}
}

func TestParseBase64Image_CorrectsMIMEMismatch(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(png1x1())
	img := parseBase64Image(b64, "webp") // client lied
	if img == nil {
		t.Fatal("expected image")
	}
	if img.Format != "png" {
		t.Fatalf("expected sniffed png, got %q", img.Format)
	}
}

func TestParseBase64Image_JpgAlias(t *testing.T) {
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	b64 := base64.StdEncoding.EncodeToString(jpeg)
	img := parseBase64Image(b64, "jpg")
	if img == nil || img.Format != "jpeg" {
		t.Fatalf("got %#v", img)
	}
}

func TestValidateKiroImages_RejectsUnsupportedFormat(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("not-a-real-image-container"))
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage.Images = []KiroImage{{
		Format: "heic",
		Source: struct {
			Bytes string `json:"bytes"`
		}{Bytes: b64},
	}}
	err := ValidateKiroImages(payload)
	var unsupported *ErrUnsupportedKiroImage
	if !errors.As(err, &unsupported) {
		t.Fatalf("expected ErrUnsupportedKiroImage, got %v", err)
	}
	if !strings.Contains(unsupported.Reason, "heic") {
		t.Fatalf("reason=%q", unsupported.Reason)
	}
}

func TestValidateKiroImages_AcceptsCorrectedPNG(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(png1x1())
	img := parseBase64Image(b64, "image/gif") // mismatch corrected at parse
	payload := &KiroPayload{}
	payload.ConversationState.CurrentMessage.UserInputMessage.Images = []KiroImage{*img}
	if err := ValidateKiroImages(payload); err != nil {
		t.Fatalf("expected accept after correction, got %v", err)
	}
	if img.Format != "png" {
		t.Fatalf("format=%q", img.Format)
	}
}

func TestClaudeToKiro_ImageMIMEMismatchCorrected(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString(png1x1())
	req := &ClaudeRequest{
		Model:     "claude-haiku-4.5",
		MaxTokens: 16,
		Messages: []ClaudeMessage{{
			Role: "user",
			Content: []any{
				map[string]any{
					"type": "image",
					"source": map[string]any{
						"type":       "base64",
						"media_type": "image/webp",
						"data":       b64,
					},
				},
				map[string]any{"type": "text", "text": "color?"},
			},
		}},
	}
	payload := ClaudeToKiro(req, false)
	if payload == nil {
		t.Fatal("nil payload")
	}
	images := payload.ConversationState.CurrentMessage.UserInputMessage.Images
	if len(images) != 1 {
		t.Fatalf("expected 1 image, got %d", len(images))
	}
	if images[0].Format != "png" {
		t.Fatalf("expected corrected png, got %q", images[0].Format)
	}
	if err := ValidateKiroImages(payload); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
