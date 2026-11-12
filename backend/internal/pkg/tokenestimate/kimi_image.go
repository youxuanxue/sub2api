package tokenestimate

import (
	"encoding/base64"
	"io"
	"math"
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// KimiK3ImageInputTokens sums MoonViT media-expansion tokens for image_url
// parts whose pixel size can be read from a data URL. https URLs are skipped:
// the estimate must not fetch them, and an unknown size stays uncounted.
//
// The pad count is MoonViT NaViT (patch 14, 2x2 merge, 65536-patch budget).
// The header addend is the K3 media envelope measured against the public KVV
// vision ground truth: 10 tokens when either side is >= 1000, otherwise 8.
// Those totals are the per-image expansion (case total minus the shared 27
// text tokens of that suite).
func KimiK3ImageInputTokens(body []byte) int {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return 0
	}
	total := 0
	root := gjson.ParseBytes(body)
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		total += kimiK3ContentImageTokens(msg.Get("content"))
		return true
	})
	input := root.Get("input")
	if input.Type == gjson.String {
		return total
	}
	input.ForEach(func(_, item gjson.Result) bool {
		total += kimiK3ContentImageTokens(item.Get("content"))
		return true
	})
	return total
}

// KimiK3MediaTokens is the prompt expansion of one image at width x height.
func KimiK3MediaTokens(width, height int) int {
	if width < 1 || height < 1 {
		return 0
	}
	const (
		patch   = 14
		merge   = 2
		inLimit = 65536
		side    = 512
	)
	s1 := math.Sqrt(inLimit / (math.Max(1, float64(width/patch)) * math.Max(1, float64(height/patch))))
	s2 := float64(side*patch) / float64(width)
	s3 := float64(side*patch) / float64(height)
	scale := math.Min(1, math.Min(s1, math.Min(s2, s3)))
	newW := min(max(1, int(float64(width)*scale)), side*patch)
	newH := min(max(1, int(float64(height)*scale)), side*patch)
	factor := merge * patch
	padW := (factor - newW%factor) % factor
	padH := (factor - newH%factor) % factor
	pads := ((newH + padH) / patch) * ((newW + padW) / patch) / (merge * merge)
	header := 8
	if width >= 1000 || height >= 1000 {
		header = 10
	}
	return pads + header
}

func kimiK3ContentImageTokens(content gjson.Result) int {
	switch {
	case content.IsArray():
		total := 0
		content.ForEach(func(_, part gjson.Result) bool {
			total += kimiK3ImagePartTokens(part)
			return true
		})
		return total
	case content.IsObject():
		return kimiK3ImagePartTokens(content)
	default:
		return 0
	}
}

func kimiK3ImagePartTokens(part gjson.Result) int {
	if part.Get("type").String() != "image_url" {
		return 0
	}
	url := part.Get("image_url.url").String()
	if url == "" && part.Get("image_url").Type == gjson.String {
		url = part.Get("image_url").String()
	}
	w, h, ok := dataImageDimensions(url)
	if !ok {
		return 0
	}
	return KimiK3MediaTokens(w, h)
}

func dataImageDimensions(rawURL string) (int, int, bool) {
	url := strings.TrimSpace(rawURL)
	if !strings.HasPrefix(strings.ToLower(url), "data:") {
		return 0, 0, false
	}
	comma := strings.IndexByte(url, ',')
	if comma < 0 {
		return 0, 0, false
	}
	meta := strings.ToLower(url[:comma])
	if !strings.Contains(meta, "image/") || !strings.Contains(meta, "base64") {
		return 0, 0, false
	}
	payload := url[comma+1:]
	// PNG/GIF headers are at the front. JPEG SOF can follow a short APP
	// segment; stop at 256KiB so a multi-megabyte data URL is not fully decoded.
	buf := make([]byte, 256*1024)
	n, err := io.ReadFull(base64.NewDecoder(base64.StdEncoding, skipSpaceReader{r: strings.NewReader(payload)}), buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		if n == 0 {
			return 0, 0, false
		}
	}
	return imageDimensions(buf[:n])
}

type skipSpaceReader struct{ r io.ByteReader }

func (s skipSpaceReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		b, err := s.r.ReadByte()
		if err != nil {
			if n > 0 {
				return n, nil
			}
			return 0, err
		}
		if unicode.IsSpace(rune(b)) {
			continue
		}
		p[n] = b
		n++
	}
	return n, nil
}

func imageDimensions(b []byte) (int, int, bool) {
	if w, h, ok := pngDimensions(b); ok {
		return w, h, true
	}
	if w, h, ok := gifDimensions(b); ok {
		return w, h, true
	}
	if w, h, ok := webpDimensions(b); ok {
		return w, h, true
	}
	if w, h, ok := jpegDimensions(b); ok {
		return w, h, true
	}
	return 0, 0, false
}

func pngDimensions(b []byte) (int, int, bool) {
	if len(b) < 24 || string(b[:8]) != "\x89PNG\r\n\x1a\n" || string(b[12:16]) != "IHDR" {
		return 0, 0, false
	}
	w := int(uint32(b[16])<<24 | uint32(b[17])<<16 | uint32(b[18])<<8 | uint32(b[19]))
	h := int(uint32(b[20])<<24 | uint32(b[21])<<16 | uint32(b[22])<<8 | uint32(b[23]))
	return w, h, w > 0 && h > 0
}

func gifDimensions(b []byte) (int, int, bool) {
	if len(b) < 10 || (string(b[:6]) != "GIF87a" && string(b[:6]) != "GIF89a") {
		return 0, 0, false
	}
	w := int(b[6]) | int(b[7])<<8
	h := int(b[8]) | int(b[9])<<8
	return w, h, w > 0 && h > 0
}

func webpDimensions(b []byte) (int, int, bool) {
	if len(b) < 30 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WEBP" || string(b[12:16]) != "VP8X" {
		return 0, 0, false
	}
	w := 1 + int(b[24]) | int(b[25])<<8 | int(b[26])<<16
	h := 1 + int(b[27]) | int(b[28])<<8 | int(b[29])<<16
	return w, h, w > 0 && h > 0
}

func jpegDimensions(b []byte) (int, int, bool) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0, 0, false
	}
	i := 2
	for i+8 < len(b) {
		if b[i] != 0xFF {
			return 0, 0, false
		}
		for i < len(b) && b[i] == 0xFF {
			i++
		}
		if i >= len(b) {
			return 0, 0, false
		}
		marker := b[i]
		i++
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD9) {
			continue
		}
		if i+1 >= len(b) {
			return 0, 0, false
		}
		segLen := int(b[i])<<8 | int(b[i+1])
		if segLen < 2 || i+segLen > len(b) {
			return 0, 0, false
		}
		// SOF markers except DHT (C4), JPG (C8) and DAC (CC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			if segLen < 7 {
				return 0, 0, false
			}
			h := int(b[i+3])<<8 | int(b[i+4])
			w := int(b[i+5])<<8 | int(b[i+6])
			return w, h, w > 0 && h > 0
		}
		i += segLen
	}
	return 0, 0, false
}
