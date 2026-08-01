package byblos

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the RED stage of byb-b3's QuantizePNG. QuantizePNG does not
// exist yet, so nothing here compiles -- that is the point.
//
// Signature (design spec, byb-b3):
//
//	func QuantizePNG(img image.Image, colors int) ([]byte, error)

// --- degenerate cases, decided on the bead -----------------------------

func TestQuantizePNGNilImageErrors(t *testing.T) {
	if _, err := QuantizePNG(nil, 16); err == nil {
		t.Fatal("QuantizePNG(nil, 16): want error, got nil")
	}
}

func TestQuantizePNGEmptyBoundsErrors(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := QuantizePNG(empty, 16); err == nil {
		t.Fatal("QuantizePNG on an empty-bounds image: want error, got nil")
	}
}

func TestQuantizePNGColorsBelowTwoErrors(t *testing.T) {
	for _, n := range []int{-1, 0, 1} {
		if _, err := QuantizePNG(corpus.Scanpage(), n); err == nil {
			t.Errorf("QuantizePNG(_, %d): want error, got nil", n)
		}
	}
}

func TestQuantizePNGColorsAbove256Errors(t *testing.T) {
	if _, err := QuantizePNG(corpus.Scanpage(), 257); err == nil {
		t.Fatal("QuantizePNG(_, 257): want error, got nil")
	}
}

func TestQuantizePNGColorsAtBoundsSucceeds(t *testing.T) {
	for _, n := range []int{2, 256} {
		if _, err := QuantizePNG(corpus.Photo(), n); err != nil {
			t.Errorf("QuantizePNG(_, %d): want no error, got %v", n, err)
		}
	}
}

func TestQuantizePNGNonOpaqueInputErrors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{100, 100, 100, 128})
		}
	}
	if _, err := QuantizePNG(img, 16); err == nil {
		t.Fatal("QuantizePNG on a non-opaque image: want error, got nil")
	}
}

// --- core behaviour ------------------------------------------------------

// decodePalette decodes b as a PNG and requires it to be palette-indexed,
// returning the *image.Paletted so callers can inspect Palette directly.
func decodePalette(t *testing.T, b []byte) *image.Paletted {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decoding QuantizePNG output as PNG: %v", err)
	}
	pi, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("QuantizePNG output decoded as %T; want *image.Paletted", img)
	}
	return pi
}

func TestQuantizePNGOutputIsValidPalettedPNG(t *testing.T) {
	out, err := QuantizePNG(corpus.Photo(), 64)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	if len(pi.Palette) > 64 {
		t.Errorf("palette has %d entries; want <= 64", len(pi.Palette))
	}
	if pi.Bounds().Dx() != corpus.ImageW || pi.Bounds().Dy() != corpus.ImageH {
		t.Errorf("output bounds %v; want %dx%d", pi.Bounds(), corpus.ImageW, corpus.ImageH)
	}
}

// TestQuantizePNGPaletteSizeRespected checks the palette length actually
// tracks the requested budget rather than always saturating at 256 --
// pinned separately from the bound-checking test above because a stub that
// ignores `colors` and always emits 256 entries would otherwise slip past
// "<= colors" trivially at colors=256 but be caught here at a small N on an
// image with many more than N natural colours.
func TestQuantizePNGPaletteSizeRespected(t *testing.T) {
	out, err := QuantizePNG(corpus.Photo(), 8)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	if len(pi.Palette) > 8 {
		t.Fatalf("palette has %d entries; want <= 8", len(pi.Palette))
	}
}

// TestQuantizePNGBitDepthByPaletteSize checks image/png picked the smallest
// IHDR bit depth the palette size allows -- 1/2/4/8 for palette lengths
// 2/4/16/256, verified achievable in the design's prototyping.
func TestQuantizePNGBitDepthByPaletteSize(t *testing.T) {
	for _, n := range []int{2, 4, 16, 256} {
		out, err := QuantizePNG(corpus.Photo(), n)
		if err != nil {
			t.Fatalf("QuantizePNG(_, %d): %v", n, err)
		}
		if len(out) < 25 {
			t.Fatalf("QuantizePNG(_, %d) output too short to hold an IHDR", n)
		}
		// IHDR is always the first chunk, at a fixed offset: 8-byte
		// signature + 4-byte length + 4-byte "IHDR" + 13-byte data. Bit
		// depth is data byte 8, i.e. overall offset 8+4+4+8 = 24.
		depth := out[24]
		want := map[int]byte{2: 1, 4: 2, 16: 4, 256: 8}[n]
		if depth != want {
			t.Errorf("QuantizePNG(_, %d): IHDR bit depth = %d; want %d", n, depth, want)
		}
	}
}

// TestQuantizePNGLosslessOnFewColourImage checks that an image with at most
// N distinct colours quantized to N is reproduced exactly -- Scanpage has a
// handful of flat colours by construction.
func TestQuantizePNGLosslessOnFewColourImage(t *testing.T) {
	out, err := QuantizePNG(corpus.Scanpage(), 256)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	psnr := psnrRGB(corpus.Scanpage(), pi)
	if !math.IsInf(psnr, 1) {
		t.Errorf("QuantizePNG(Scanpage, 256) PSNR = %v dB; want +Inf (lossless)", psnr)
	}
}

func TestQuantizePNGDeterministic(t *testing.T) {
	a, err := QuantizePNG(corpus.Photo(), 32)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	b, err := QuantizePNG(corpus.Photo(), 32)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("QuantizePNG produced different bytes across two calls on the same input")
	}
}
