package service

import (
	"bytes"
	"image"
	stddraw "image/draw"
	"image/png"

	xdraw "golang.org/x/image/draw"
)

const (
	// Single-step CatmullRom jumps beyond ~2× look soft on large canvases
	// (e.g. ~1.2K upstream → 3840). Progressive steps keep each hop ≤2×.
	// No Real-ESRGAN / external upscaler in this path — enlargement is local
	// only, driven by the client explicit WxH canvas (not -2k/-4k aliases).
	openAIImagesUpscaleMaxStep = 2.0
)

// scaleOpenAIImageProgressive enlarges src to exactly dw×dh with progressive
// CatmullRom steps (each hop ≤2×). Downscale / equal size uses one hop.
func scaleOpenAIImageProgressive(srcImg image.Image, bounds image.Rectangle, dw, dh int) *image.RGBA {
	sw, sh := bounds.Dx(), bounds.Dy()
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 {
		return image.NewRGBA(image.Rect(0, 0, max(dw, 1), max(dh, 1)))
	}
	if sw == dw && sh == dh {
		dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
		stddraw.Draw(dst, dst.Bounds(), srcImg, bounds.Min, stddraw.Src)
		return dst
	}

	curW, curH := sw, sh
	cur := image.Image(srcImg)
	curBounds := bounds

	for {
		scaleW := float64(dw) / float64(curW)
		scaleH := float64(dh) / float64(curH)
		factor := scaleW
		if scaleH < factor {
			factor = scaleH
		}
		if factor <= 1.0 {
			break
		}
		step := factor
		if step > openAIImagesUpscaleMaxStep {
			step = openAIImagesUpscaleMaxStep
		}
		nextW := int(float64(curW)*step + 0.5)
		nextH := int(float64(curH)*step + 0.5)
		if nextW < 1 {
			nextW = 1
		}
		if nextH < 1 {
			nextH = 1
		}
		if nextW >= dw && nextH >= dh {
			nextW, nextH = dw, dh
		}
		if nextW == curW && nextH == curH {
			break
		}
		next := image.NewRGBA(image.Rect(0, 0, nextW, nextH))
		xdraw.CatmullRom.Scale(next, next.Bounds(), cur, curBounds, xdraw.Src, nil)
		cur = next
		curBounds = next.Bounds()
		curW, curH = nextW, nextH
		if curW == dw && curH == dh {
			return next
		}
		if step < openAIImagesUpscaleMaxStep {
			break
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), cur, curBounds, xdraw.Src, nil)
	return dst
}

func encodeOpenAIImagePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
