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
//
// Downsample is DownsampleDeclaredBPC(img, 8, srcDPI, dstDPI) and nothing else:
// 8 is the depth it has silently assumed since it was written. That assumption
// is wrong for a source the PDF declares /BitsPerComponent 1, which Catmull-Rom
// blends into grey levels a bilevel image cannot hold (byb-plj; measured, a
// 600x800 bitonal text scan comes back with 99 distinct levels). An
// image.Image cannot say what depth it was declared at, so Downsample cannot
// tell that case apart and this signature cannot be made to -- see
// DownsampleDeclaredBPC, which takes the depth as an argument.
func Downsample(img image.Image, srcDPI, dstDPI float64) (image.Image, error) {
	return DownsampleDeclaredBPC(img, 8, srcDPI, dstDPI)
}

// DownsampleDeclaredBPC is Downsample plus the one input an image.Image cannot
// carry: declaredBPC, the source's /BitsPerComponent as its PDF declares it.
// Pass 1 for an /ImageMask stencil, which is bilevel by definition and has no
// /BitsPerComponent entry. Every other rule -- the validation, the no-op cases,
// the output dimensions, the returned concrete type -- is Downsample's, because
// this is the function Downsample calls.
//
// declaredBPC 1 resamples by point subsampling (draw.NearestNeighbor), so the
// output can only ever contain values the source already had and a bilevel scan
// cannot gain grey levels it never had. Every other declared depth interpolates
// with Catmull-Rom, exactly as before.
//
// THE DEPTH IS DECLARED, NEVER DETECTED, and that distinction is the whole
// reason this function exists rather than a fix inside Downsample. A bitonal
// scan widened to 8 bpc -- a bitonal TIFF imported as 8-bpc DeviceGray or
// DeviceRGB, which is legitimate and common -- has nothing but pure black and
// white pixels and is still NOT a mono image to any PDF tool. Ghostscript draws
// the line at the declaration: /MonoImageDownsampleType governs images declared
// 1 bpc, everything else goes through /ColorImageDownsampleType. An earlier
// attempt at byb-plj inferred bilevel-ness by scanning the pixels; measured
// against the Ghostscript oracle it dropped such an 8-bpc page from 41.66 dB to
// 21.24 dB, 13 dB under the gate in downsample_oracle_test.go, because it fired
// on a source Ghostscript downsamples bicubicly.
//
// Point subsampling is not an approximation of what Ghostscript does to a mono
// image, it is the same thing. Measured against gs pdfwrite on a 1-bpc
// DeviceGray page, draw.NearestNeighbor is PIXEL-IDENTICAL to its output for
// /Subsample, /Average AND /Bicubic alike: gs subsamples a mono image whatever
// /MonoImageDownsampleType asks for, because an interpolating filter produces
// values 1 bpc cannot store. Kleio's ladder pins /Subsample (compress.go) and
// gets what it asked for.
//
// Callers already hold the declaration, by either route into this library.
// Inspect surfaces it as ImageRef.Bitonal, "1 bit per component, or an image
// mask", keyed by the same ImageRef.ObjNr that ReplaceImages substitutes on;
// ExtractPageRaster surfaces it as PageRaster.Bitonal, where the pixels are.
// Internally both are pdfdoc.ImageInfo.BPC, the same predicate extract.go's
// mrcLayers branches on, and TestBitonalAgreesBetweenInspectAndExtract pins
// that the two routes never disagree — a caller that extracts a page, resamples
// it, and substitutes the result by object number crosses from one to the other.
//
// The PIXELS still cannot say, and that has not changed: pdfcpu renders a 1-bpc
// DeviceGray image to PNG and image.Decode returns *image.RGBA, so the raster
// is 8 bits per channel with two distinct values in it. PageRaster.Bitonal is
// the only surviving record of what those samples were widened from, which is
// why it is a field beside the image rather than something recoverable from it.
func DownsampleDeclaredBPC(img image.Image, declaredBPC int, srcDPI, dstDPI float64) (image.Image, error) {
	// The /BitsPerComponent values an image XObject may declare, the same set
	// internal/pdfdoc/write.go accepts. 0 is pdfdoc.ImageInfo.BPC's "no such
	// entry", and a caller that does not know the depth must not be silently
	// handed the contone kernel -- being handed it silently IS byb-plj.
	switch declaredBPC {
	case 1, 2, 4, 8, 16:
	default:
		return nil, fmt.Errorf("byblos: downsample: declared bits per component %d is not one of 1, 2, 4, 8, 16", declaredBPC)
	}
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
	scaler := draw.Scaler(draw.CatmullRom)
	if declaredBPC == 1 {
		scaler = draw.NearestNeighbor
	}
	scaler.Scale(dst, dstRect, img, b, draw.Src, nil)
	return dst, nil
}
