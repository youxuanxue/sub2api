// Command image_fidelity_pp applies TokenKey-style exact-canvas + format post-process
// to one upstream image (stdin or -in). Used by edge fidelity matrix probes.
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/HugoSmits86/nativewebp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

func main() {
	inPath := flag.String("in", "", "input image path (default stdin)")
	size := flag.String("size", "", "target WxH")
	format := flag.String("format", "png", "png|jpeg|webp")
	background := flag.String("background", "opaque", "opaque|transparent")
	quality := flag.String("quality", "medium", "quality hint")
	compression := flag.Int("compression", -1, "0-100 or -1 unset")
	flag.Parse()

	raw, err := readInput(*inPath)
	if err != nil {
		fail(err)
	}
	tw, th, ok := parseWH(*size)
	if !ok {
		fail(fmt.Errorf("invalid size %q", *size))
	}
	var comp *int
	if *compression >= 0 {
		c := *compression
		comp = &c
	}
	out, container, err := process(raw, tw, th, *format, *background, *quality, comp)
	if err != nil {
		fail(err)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{
		"ok":            true,
		"container":     container,
		"dimensions":    fmt.Sprintf("%dx%d", tw, th),
		"bytes":         len(out),
		"b64":           base64.StdEncoding.EncodeToString(out),
		"b64_prefix":    prefix(out, 16),
		"in_bytes":      len(raw),
		"in_sniff":      sniff(raw),
		"in_dimensions": dimsOf(raw),
	})
}

func fail(err error) {
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": err.Error()})
	os.Exit(1)
}

func readInput(path string) ([]byte, error) {
	if path == "" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func parseWH(size string) (int, int, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(size)), "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(strings.TrimSpace(parts[0]))
	h, errH := strconv.Atoi(strings.TrimSpace(parts[1]))
	return w, h, errW == nil && errH == nil && w > 0 && h > 0
}

func process(raw []byte, tw, th int, format, background, quality string, compression *int) ([]byte, string, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	opaque := true
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "jpg" {
		format = "jpeg"
	}
	bg := strings.ToLower(strings.TrimSpace(background))
	if bg == "transparent" && (format == "png" || format == "webp" || format == "") {
		opaque = false
	}
	if format == "jpeg" {
		opaque = true
	}
	canvas := pad(src, tw, th, opaque)
	var buf bytes.Buffer
	switch format {
	case "jpeg":
		q := jpegQuality(quality, compression)
		if err := jpeg.Encode(&buf, canvas, &jpeg.Options{Quality: q}); err != nil {
			return nil, "", err
		}
	case "webp":
		opts := &nativewebp.Options{CompressionLevel: webpLevel(quality, compression)}
		if err := nativewebp.Encode(&buf, canvas, opts); err != nil {
			return nil, "", err
		}
	default:
		format = "png"
		if err := png.Encode(&buf, canvas); err != nil {
			return nil, "", err
		}
	}
	return buf.Bytes(), format, nil
}

func pad(src image.Image, tw, th int, opaque bool) image.Image {
	sb := src.Bounds()
	sw, sh := sb.Dx(), sb.Dy()
	scale := float64(tw) / float64(sw)
	if float64(th)/float64(sh) < scale {
		scale = float64(th) / float64(sh)
	}
	nw := max(1, int(float64(sw)*scale))
	nh := max(1, int(float64(sh)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	if opaque {
		draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{0, 0, 0, 255}}, image.Point{}, draw.Src)
	}
	resized := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.CatmullRom.Scale(resized, resized.Bounds(), src, sb, xdraw.Over, nil)
	ox, oy := (tw-nw)/2, (th-nh)/2
	draw.Draw(dst, image.Rect(ox, oy, ox+nw, oy+nh), resized, image.Point{}, draw.Over)
	return dst
}

func jpegQuality(quality string, compression *int) int {
	if compression != nil {
		c := *compression
		if c < 1 {
			c = 1
		}
		if c > 100 {
			c = 100
		}
		return c
	}
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "low":
		return 45
	case "medium":
		return 72
	case "high":
		return 88
	case "xhigh", "max":
		return 95
	default:
		return 92
	}
}

func webpLevel(quality string, compression *int) nativewebp.CompressionLevel {
	if compression != nil {
		c := *compression
		switch {
		case c <= 30:
			return nativewebp.BestSpeed
		case c >= 80:
			return nativewebp.BestCompression
		default:
			return nativewebp.DefaultCompression
		}
	}
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case "low":
		return nativewebp.BestSpeed
	case "high", "xhigh", "max":
		return nativewebp.BestCompression
	default:
		return nativewebp.DefaultCompression
	}
}

func sniff(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "jpeg"
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return "png"
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return "webp"
	default:
		return ""
	}
}

func dimsOf(raw []byte) string {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%dx%d", cfg.Width, cfg.Height)
}

func prefix(raw []byte, n int) string {
	if len(raw) < n {
		n = len(raw)
	}
	return fmt.Sprintf("%x", raw[:n])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
