package byblos

import (
	"fmt"
	"image"
	"math"

	"golang.org/x/image/draw"
)

// Downsample resamples img from srcDPI to dstDPI with a Catmull-Rom kernel
// (golang.org/x/image/draw), the x/image scaler that agrees with
// ghostscript's /Bicubic downsampler (design spec byb-b3 section 1, 4).
//
// srcDPI <= dstDPI, or a ratio that rounds to identical output dimensions, is
// a no-op: img is returned unchanged, not resampled. Kleio's compression
// ladder must be able to ask for 300 DPI on a 150 DPI scan and get the scan
// back, not a failure, and upsampling a scan invents detail byblos will not
// produce.
func Downsample(img image.Image, srcDPI, dstDPI float64) (image.Image, error) {
	if img == nil {
		return nil, fmt.Errorf("byblos: downsample: image is nil")
	}
	b := img.Bounds()
	if b.Empty() {
		return nil, fmt.Errorf("byblos: downsample: image bounds %v are empty", b)
	}
	// A source this large (an image.Uniform-style unbounded image, or a
	// lazily-computed tiled one) would drive image.NewRGBA below into a
	// makeslice panic rather than a returned error. Nothing in this package
	// panics; reject it here instead. maxSourcePixels is far above any real
	// scanned page (a few hundred megapixels at most).
	const maxSourcePixels = 500_000_000
	if int64(b.Dx())*int64(b.Dy()) > maxSourcePixels {
		return nil, fmt.Errorf("byblos: downsample: image bounds %v exceed %d pixels", b, maxSourcePixels)
	}
	if srcDPI <= 0 || math.IsNaN(srcDPI) || math.IsInf(srcDPI, 0) {
		return nil, fmt.Errorf("byblos: downsample: srcDPI %v must be positive and finite", srcDPI)
	}
	if dstDPI <= 0 || math.IsNaN(dstDPI) || math.IsInf(dstDPI, 0) {
		return nil, fmt.Errorf("byblos: downsample: dstDPI %v must be positive and finite", dstDPI)
	}
	if srcDPI <= dstDPI {
		return img, nil
	}

	ratio := dstDPI / srcDPI
	w := int(math.Round(float64(b.Dx()) * ratio))
	h := int(math.Round(float64(b.Dy()) * ratio))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	if w == b.Dx() && h == b.Dy() {
		return img, nil
	}

	dstRect := image.Rect(0, 0, w, h)
	var dst draw.Image
	if _, ok := img.(*image.Gray); ok {
		dst = image.NewGray(dstRect)
	} else {
		dst = image.NewRGBA(dstRect)
	}
	draw.CatmullRom.Scale(dst, dstRect, img, b, draw.Src, nil)
	return dst, nil
}
