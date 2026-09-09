package newapi

import (
	"strconv"
	"strings"

	geminitask "github.com/QuantumNous/new-api/relay/channel/task/gemini"
)

const (
	VideoTaskResolution480P  = "480p"
	VideoTaskResolution720P  = "720p"
	VideoTaskResolution1080P = "1080p"
	VideoTaskResolution4K    = "4k"
)

// NormalizeVideoTaskResolution resolves TokenKey's public resolution aliases
// and the pinned new-api TaskSubmitReq size form to one billing/adaptor value.
func NormalizeVideoTaskResolution(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "", "auto", "480", VideoTaskResolution480P, "sd":
		return VideoTaskResolution480P, true
	case "720", VideoTaskResolution720P, "hd":
		return VideoTaskResolution720P, true
	case "1080", VideoTaskResolution1080P, "full_hd", "full-hd", "fhd":
		return VideoTaskResolution1080P, true
	case VideoTaskResolution4K, "2160p", "uhd":
		return VideoTaskResolution4K, true
	}

	parts := strings.SplitN(normalized, "x", 2)
	if len(parts) != 2 {
		return "", false
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return "", false
	}
	return geminitask.SizeToVeoResolution(normalized), true
}
