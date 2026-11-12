//go:build unit

package tokenestimate

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestKimiK3MediaTokensMatchKVVVisionExpansion(t *testing.T) {
	// Per-image expansion from KVV vision_cases (commit 7b938c38):
	// case expected_prompt_tokens minus the suite's shared 27 text tokens,
	// divided by the image count. Swapped dimensions share one total.
	cases := []struct {
		w, h, want int
	}{
		{1536, 1024, 2045},
		{1024, 1536, 2045},
		{1024, 1024, 1379},
		{768, 512, 540},
		{512, 768, 540},
		{512, 512, 369},
		{384, 256, 148},
		{256, 384, 148},
		{128, 128, 33},
		{64, 64, 17},
		{3584, 3584, 16394},
	}
	for _, tc := range cases {
		if got := KimiK3MediaTokens(tc.w, tc.h); got != tc.want {
			t.Fatalf("%dx%d tokens=%d want %d", tc.w, tc.h, got, tc.want)
		}
	}
}

func TestKimiK3ImageInputTokensReadsPNGDataURL(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 128, 128))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	url := "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`)
	if got := KimiK3ImageInputTokens(body); got != 33 {
		t.Fatalf("png tokens=%d want 33", got)
	}
	remote := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}]}`)
	if got := KimiK3ImageInputTokens(remote); got != 0 {
		t.Fatalf("remote url tokens=%d want 0", got)
	}
}

func TestKimiK3ImageInputTokensReadsJPEG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	url := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`)
	if got := KimiK3ImageInputTokens(body); got != KimiK3MediaTokens(64, 64) {
		t.Fatalf("jpeg tokens=%d want %d", got, KimiK3MediaTokens(64, 64))
	}
}
