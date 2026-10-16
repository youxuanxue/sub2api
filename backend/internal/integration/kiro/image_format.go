package kiro

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// Kiro upstream (CodeWhisperer / Bedrock Claude) accepts these image containers.
// Declared MIME that does not match bytes, or formats outside this set, cause
// "Image format is not supported" / "Could not process image" and must not be
// retried across accounts.
var kiroAllowedImageFormats = map[string]struct{}{
	"jpeg": {},
	"png":  {},
	"gif":  {},
	"webp": {},
}

// ErrUnsupportedKiroImage is returned when inbound image bytes or MIME cannot
// be safely forwarded to Kiro without a predictable upstream rejection.
type ErrUnsupportedKiroImage struct {
	Reason string
}

func (e *ErrUnsupportedKiroImage) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return "unsupported image for Kiro"
	}
	return e.Reason
}

func normalizeKiroImageFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	format = strings.TrimPrefix(format, "image/")
	if format == "jpg" {
		return "jpeg"
	}
	return format
}

func isKiroAllowedImageFormat(format string) bool {
	_, ok := kiroAllowedImageFormats[normalizeKiroImageFormat(format)]
	return ok
}

func sniffKiroImageFormat(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "jpeg"
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return "gif"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return ""
	}
}

func decodeKiroImageBase64(data string) ([]byte, string, error) {
	cleaned := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(data, "\n", ""), "\r", ""))
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		raw, err := enc.DecodeString(cleaned)
		if err == nil {
			return raw, cleaned, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("invalid base64 image data")
	}
	return nil, "", lastErr
}

// normalizeKiroImageFormatAgainstBytes prefers magic-byte sniffing over the
// client-declared MIME. Kiro/Bedrock reject MIME/bytes mismatches, so trusting
// bytes is required for PNG/JPEG/WebP/GIF that were mislabeled upstream.
func normalizeKiroImageFormatAgainstBytes(declared string, raw []byte) (string, error) {
	declared = normalizeKiroImageFormat(declared)
	sniffed := sniffKiroImageFormat(raw)
	switch {
	case sniffed != "":
		return sniffed, nil
	case declared != "" && isKiroAllowedImageFormat(declared):
		// Bytes are opaque (truncated payload, exotic codec). Keep the declared
		// allowlisted format and let upstream accept or reject; classifier
		// maps format rejections to non-failover client errors.
		return declared, nil
	case declared == "":
		return "", &ErrUnsupportedKiroImage{Reason: "image bytes are not a supported format (jpeg/png/gif/webp)"}
	default:
		return "", &ErrUnsupportedKiroImage{
			Reason: fmt.Sprintf("image format %q is not supported (supported: jpeg, png, gif, webp)", declared),
		}
	}
}

// ValidateKiroImages walks the translated payload and rejects images that are
// not allowlisted or whose bytes do not match a known container. Call after
// ClaudeToKiro / OpenAIToKiro so Forward can return a client 400 instead of
// burning the account pool on a deterministic upstream rejection.
func ValidateKiroImages(payload *KiroPayload) error {
	if payload == nil {
		return nil
	}
	check := func(images []KiroImage) error {
		for i := range images {
			if err := validateKiroImage(&images[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(payload.ConversationState.CurrentMessage.UserInputMessage.Images); err != nil {
		return err
	}
	for _, msg := range payload.ConversationState.History {
		if msg.UserInputMessage == nil {
			continue
		}
		if err := check(msg.UserInputMessage.Images); err != nil {
			return err
		}
	}
	return nil
}

func validateKiroImage(img *KiroImage) error {
	if img == nil {
		return nil
	}
	raw, _, err := decodeKiroImageBase64(img.Source.Bytes)
	if err != nil {
		return &ErrUnsupportedKiroImage{Reason: "image data is not valid base64"}
	}
	format, err := normalizeKiroImageFormatAgainstBytes(img.Format, raw)
	if err != nil {
		return err
	}
	sniffed := sniffKiroImageFormat(raw)
	if sniffed != "" && sniffed != format {
		return &ErrUnsupportedKiroImage{
			Reason: fmt.Sprintf("image was specified as %s but bytes are %s", format, sniffed),
		}
	}
	if !isKiroAllowedImageFormat(format) {
		return &ErrUnsupportedKiroImage{
			Reason: fmt.Sprintf("image format %q is not supported (supported: jpeg, png, gif, webp)", format),
		}
	}
	img.Format = format
	return nil
}
