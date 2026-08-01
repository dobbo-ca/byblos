package byblos

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the RED stage of byb-b3's Downsample. Downsample does not
// exist yet, so nothing here compiles -- that is the point.
//
// Signature (design spec, byb-b3):
//
//	func Downsample(img image.Image, srcDPI, dstDPI float64) (image.Image, error)

func TestDownsampleNilImageErrors(t *testing.T) {
	if _, err := Downsample(nil, 300, 150); err == nil {
		t.Fatal("Downsample(nil, ...): want error, got nil")
	}
}

func TestDownsampleEmptyBoundsErrors(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := Downsample(empty, 300, 150); err == nil {
		t.Fatal("Downsample on an empty-bounds image: want error, got nil")
	}
}

func TestDownsampleBadDPIErrors(t *testing.T) {
	img := corpus.Scanpage()
	for _, tc := range []struct {
		name           string
		srcDPI, dstDPI float64
	}{
		{"src zero", 0, 150},
		{"src negative", -300, 150},
		{"dst zero", 300, 0},
		{"dst negative", 300, -150},
		{"src NaN", math.NaN(), 150},
		{"dst NaN", 300, math.NaN()},
		{"src +Inf", math.Inf(1), 150},
		{"dst +Inf", 300, math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Downsample(img, tc.srcDPI, tc.dstDPI); err == nil {
				t.Fatalf("Downsample(_, %v, %v): want error, got nil", tc.srcDPI, tc.dstDPI)
			}
		})
	}
}

// TestDownsampleNoOpWhenSrcNotGreaterThanDst checks the design's settled
// no-op rule: srcDPI <= dstDPI returns the input image unchanged (and
// specifically does not error -- Kleio's compression ladder must be able to
// ask for 300 on a 150 DPI scan and get the scan back, not a failure).
func TestDownsampleNoOpWhenSrcNotGreaterThanDst(t *testing.T) {
	img := corpus.Scanpage()
	for _, tc := range []struct{ src, dst float64 }{
		{150, 300}, {150, 150},
	} {
		out, err := Downsample(img, tc.src, tc.dst)
		if err != nil {
			t.Fatalf("Downsample(_, %v, %v): %v", tc.src, tc.dst, err)
		}
		if out != image.Image(img) {
			t.Errorf("Downsample(_, %v, %v) did not return the identical image value", tc.src, tc.dst)
		}
	}
}

// TestDownsampleOutputDimensions checks the output is scaled by the DPI
// ratio, rounded, floored at 1.
func TestDownsampleOutputDimensions(t *testing.T) {
	img := corpus.Scanpage() // 600x800
	out, err := Downsample(img, 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	wantW := int(math.Round(float64(corpus.ImageW) * 150 / 300))
	wantH := int(math.Round(float64(corpus.ImageH) * 150 / 300))
	b := out.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("Downsample(300->150) output %dx%d; want %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
}

// TestDownsampleRatioRoundsToIdenticalDimensionsIsNoOp checks the second
// no-op case the design settled: even with srcDPI > dstDPI, if the rounded
// output dimensions equal the input's, Downsample returns the input
// unchanged rather than a resampled copy that happens to be the same size.
func TestDownsampleRatioRoundsToIdenticalDimensionsIsNoOp(t *testing.T) {
	img := corpus.Scanpage()
	// A tiny DPI delta that rounds back to the same pixel dimensions.
	out, err := Downsample(img, 300, 299.9)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	if out != image.Image(img) {
		t.Error("Downsample with a ratio rounding to identical dimensions did not return the input unchanged")
	}
}

// TestDownsampleGraySurvivesAsGray checks a *image.Gray in produces a
// *image.Gray out -- CatmullRom.Scale accepts a Gray destination directly.
func TestDownsampleGraySurvivesAsGray(t *testing.T) {
	gray := image.NewGray(image.Rect(0, 0, corpus.ImageW, corpus.ImageH))
	for y := 0; y < corpus.ImageH; y++ {
		for x := 0; x < corpus.ImageW; x++ {
			gray.SetGray(x, y, color.Gray{Y: uint8((x + y) % 256)})
		}
	}
	out, err := Downsample(gray, 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	if _, ok := out.(*image.Gray); !ok {
		t.Errorf("Downsample on *image.Gray returned %T; want *image.Gray", out)
	}
}

// TestDownsampleSolidRegionSurvives checks a known solid-colour region is
// not garbage after resampling: a Scanpage's paper region, well away from any
// edge, must remain close to the paper colour.
func TestDownsampleSolidRegionSurvives(t *testing.T) {
	out, err := Downsample(corpus.Scanpage(), 300, 150)
	if err != nil {
		t.Fatalf("Downsample: %v", err)
	}
	// (10,200) in the half-size output maps to source row ~400 -- well
	// inside the paper region. (10,10) (source row ~20) is NOT a safe
	// choice: it sits exactly on the letterhead band's boundary (source rows
	// 20-59), and CatmullRom.Scale's kernel support at a 2x downsample ratio
	// reaches source rows 17-24, mixing band into every scaler's output
	// there (measured: CatmullRom, ApproxBiLinear, BiLinear and
	// NearestNeighbor all return something between paper and band at (10,10)
	// on this fixture).
	r, g, b, _ := out.At(10, 200).RGBA()
	wantR, wantG, wantB := uint32(250), uint32(246), uint32(236)
	tol := uint32(10)
	if absDiff(r>>8, wantR) > tol || absDiff(g>>8, wantG) > tol || absDiff(b>>8, wantB) > tol {
		t.Errorf("Downsample paper region at (10,10) = (%d,%d,%d); want close to (%d,%d,%d)",
			r>>8, g>>8, b>>8, wantR, wantG, wantB)
	}
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// TestDownsampleNonZeroOriginBounds checks a source image whose bounds do
// not start at (0,0) is handled correctly.
func TestDownsampleNonZeroOriginBounds(t *testing.T) {
	full := corpus.Scanpage()
	sub := image.NewRGBA(image.Rect(100, 200, 100+corpus.ImageW, 200+corpus.ImageH))
	for y := sub.Bounds().Min.Y; y < sub.Bounds().Max.Y; y++ {
		for x := sub.Bounds().Min.X; x < sub.Bounds().Max.X; x++ {
			sub.Set(x, y, full.At(x-100, y-200))
		}
	}
	out, err := Downsample(sub, 300, 150)
	if err != nil {
		t.Fatalf("Downsample on a non-zero-origin image: %v", err)
	}
	wantW := int(math.Round(float64(corpus.ImageW) * 150 / 300))
	wantH := int(math.Round(float64(corpus.ImageH) * 150 / 300))
	b := out.Bounds()
	if b.Dx() != wantW || b.Dy() != wantH {
		t.Errorf("Downsample non-zero-origin output %dx%d; want %dx%d", b.Dx(), b.Dy(), wantW, wantH)
	}
}
