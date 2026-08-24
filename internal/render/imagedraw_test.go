package render

// Stage 4b (byb-8b9.2): the Do operator for /Subtype /Image XObjects. These
// are exact-pixel self-checks: nearest-neighbor sampling against pixel
// centers makes every quadrant boundary of a 2x2 source image land on a
// predictable device column and row, so the assertions are equalities, not
// tolerances.

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
)

var green = color.RGBA{0, 255, 0, 255}

// twoByTwo is a 2x2 source image with four distinct opaque colours. Source
// row 0 (red, blue) is the TOP of the image, which the unit-square convention
// (ISO 32000-1 8.9.5.2) puts at v near 1.
func twoByTwo() image.Image {
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, red)
	im.Set(1, 0, blue)
	im.Set(0, 1, black)
	im.Set(1, 1, green)
	return im
}

func renderImages(t *testing.T, src string, imgs map[string]Image) *image.RGBA {
	t.Helper()
	img, err := Page(context.Background(), []byte(src), box100, 1, func(name string) (Image, bool) {
		im, ok := imgs[name]
		return im, ok
	})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	return img
}

type region struct {
	x0, x1, y0, y1 int
	col            color.RGBA
}

// assertRegions asserts every pixel inside a region is that region's colour
// and every pixel outside all of them is white. Regions must not overlap.
func assertRegions(t *testing.T, img *image.RGBA, regions []region) {
	t.Helper()
	bad := 0
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			want := white
			for _, r := range regions {
				if x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 {
					want = r.col
					break
				}
			}
			if got := pixelAt(img, x, y); got != want {
				bad++
				if bad <= 5 {
					t.Errorf("pixel (%d,%d) = %v; want %v", x, y, got, want)
				}
			}
		}
	}
	if bad > 5 {
		t.Errorf("... and %d more wrong pixels", bad-5)
	}
}

// TestImageDrawExactPixels: the unit square under `40 0 0 40 10 20 cm` is
// user (10,20)-(50,60), device x [10,50) rows [40,80). Source row 0 is the
// top of that square, so red/blue land on the upper rows.
func TestImageDrawExactPixels(t *testing.T) {
	img := renderImages(t, "q 40 0 0 40 10 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, []region{
		{10, 30, 40, 60, red},
		{30, 50, 40, 60, blue},
		{10, 30, 60, 80, black},
		{30, 50, 60, 80, green},
	})
}

// TestImageRotation90: `0 40 -40 0 60 20 cm` maps (u,v) to user
// (60-40v, 20+40u), a 90-degree rotation. Each source quadrant must land on
// the exactly predicted device rectangle -- a renderer that ignores b and c,
// or transposes them, fails on whole quadrants.
func TestImageRotation90(t *testing.T) {
	img := renderImages(t, "q 0 40 -40 0 60 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, []region{
		{20, 40, 60, 80, red},   // u<.5 v>.5 -> user x(20,40) y(20,40)
		{20, 40, 40, 60, blue},  // u>.5 v>.5 -> user y(40,60)
		{40, 60, 60, 80, black}, // u<.5 v<.5 -> user x(40,60) y(20,40)
		{40, 60, 40, 60, green},
	})
}

// TestImageFlip: a negative horizontal scale mirrors the image. Under
// `-40 0 0 40 50 20 cm`, user x = 50-40u, so red (u<.5) lands on the RIGHT
// half -- the mirror of TestImageDrawExactPixels.
func TestImageFlip(t *testing.T) {
	img := renderImages(t, "q -40 0 0 40 50 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, []region{
		{30, 50, 40, 60, red},
		{10, 30, 40, 60, blue},
		{30, 50, 60, 80, black},
		{10, 30, 60, 80, green},
	})
}

// TestImageShear: `40 0 40 40 10 20 cm` shears x by v: user
// x = 10+40u+40v, y = 20+40v. The top half (v>.5) is offset right of the
// bottom half; probes sit at quadrant centres of the sheared parallelogram.
func TestImageShear(t *testing.T) {
	img := renderImages(t, "q 40 0 40 40 10 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	for _, p := range []struct {
		ux, uy int
		want   color.RGBA
		what   string
	}{
		{50, 50, red, "u=.25 v=.75"},   // x=10+10+30, y=20+30
		{70, 50, blue, "u=.75 v=.75"},  // x=10+30+30
		{30, 30, black, "u=.25 v=.25"}, // x=10+10+10, y=20+10
		{50, 30, green, "u=.75 v=.25"},
		{15, 55, white, "left of the sheared top edge"},
	} {
		if got := pixelAt(img, p.ux, 100-p.uy); got != p.want {
			t.Errorf("%s at user (%d,%d) = %v; want %v", p.what, p.ux, p.uy, got, p.want)
		}
	}
}

// TestImageMaskStencil: /ImageMask true paints the CURRENT FILL COLOUR where
// the decoded sample is 0 (ISO 32000-1 8.9.6.2), and Invert (a /Decode of
// [1 0]) inverts which samples paint. Unpainted samples leave the background.
func TestImageMaskStencil(t *testing.T) {
	mask := image.NewGray(image.Rect(0, 0, 2, 2))
	mask.SetGray(0, 0, color.Gray{Y: 0}) // sample 0: marks
	mask.SetGray(1, 0, color.Gray{Y: 255})
	mask.SetGray(0, 1, color.Gray{Y: 255})
	mask.SetGray(1, 1, color.Gray{Y: 0})
	const src = "1 0 0 rg q 40 0 0 40 10 20 cm /M Do Q"

	img := renderImages(t, src, map[string]Image{"M": {Data: mask, Stencil: true}})
	assertRegions(t, img, []region{
		{10, 30, 40, 60, red},
		{30, 50, 60, 80, red},
	})

	inverted := renderImages(t, src,
		map[string]Image{"M": {Data: mask, Stencil: true, Invert: true}})
	assertRegions(t, inverted, []region{
		{30, 50, 40, 60, red},
		{10, 30, 60, 80, red},
	})
}

// TestUnsupportedImageSkipsDraw: a name the resolver declines (a JPX the
// caller could not decode, a form, a missing resource) skips the draw and
// keeps rendering -- it must never error the whole page.
func TestUnsupportedImageSkipsDraw(t *testing.T) {
	img := renderImages(t, "q 40 0 0 40 10 20 cm /Im0 Do Q 1 0 0 rg 10 20 30 40 re f", nil)
	assertRect(t, img, 10, 40, 40, 80, red)
}

// TestImageWithNilResolver: a Do with no resolver at all (stage 4a callers)
// is likewise a clean no-op.
func TestImageWithNilResolver(t *testing.T) {
	img := render100(t, "q 40 0 0 40 10 20 cm /Im0 Do Q 1 0 0 rg 10 20 30 40 re f")
	assertRect(t, img, 10, 40, 40, 80, red)
}

// TestImageDegenerateCTMSkips: a singular CTM maps the unit square to zero
// area; file-supplied garbage must not divide by zero or error.
func TestImageDegenerateCTMSkips(t *testing.T) {
	img := renderImages(t, "q 0 0 0 0 50 50 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, nil)
}

// TestClipRestrictsImage: the 4a rectangular clip applies to image draws.
// Clip user (20,20)-(40,40) is device x [20,40) rows [60,80), the bottom
// half of the image: black then green.
func TestClipRestrictsImage(t *testing.T) {
	img := renderImages(t, "20 20 20 20 re W n q 40 0 0 40 10 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, []region{
		{20, 30, 60, 80, black},
		{30, 40, 60, 80, green},
	})
}

// TestImageAlphaComposites: a half-transparent red source pixel over the
// white canvas comes out pink by source-over -- exact under Go's
// premultiplied 16-bit arithmetic.
func TestImageAlphaComposites(t *testing.T) {
	one := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	one.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 128})
	img := renderImages(t, "q 40 0 0 40 10 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: one}})
	want := color.RGBA{255, 127, 127, 255}
	if got := pixelAt(img, 20, 50); got != want {
		t.Errorf("composited pixel = %v; want %v", got, want)
	}
	if got := pixelAt(img, 5, 50); got != white {
		t.Errorf("outside pixel = %v; want white", got)
	}
}

// TestImageHugeTranslationSkips: a finite-but-huge translation operand (301
// digits -- PDF numbers have no exponent form, and matrixOperands rejects
// only Inf/NaN) places the image far off-canvas. The device bounds must be
// intersected in float space BEFORE int conversion: unclamped, the
// conversion wraps to minInt64 on amd64, the empty-range check passes, and
// the draw loop hangs for ~9e18 iterations. arm64 saturates the conversion
// instead, so there this test passed even before the clamp existed; the
// regression it pins is amd64's (linux CI and prod).
func TestImageHugeTranslationSkips(t *testing.T) {
	huge := "1" + strings.Repeat("0", 300)
	img := renderImages(t, "q 1 0 0 1 "+huge+" 0 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: twoByTwo()}})
	assertRegions(t, img, nil)
}

// TestImageRGBAFastPath: samplerFor's premultiplied *image.RGBA fast path
// must place the same quadrants as the generic At path (twoByTwo's NRGBA and
// the stencil test's Gray cover the other two fast paths).
func TestImageRGBAFastPath(t *testing.T) {
	im := image.NewRGBA(image.Rect(0, 0, 2, 2))
	im.Set(0, 0, red)
	im.Set(1, 0, blue)
	im.Set(0, 1, black)
	im.Set(1, 1, green)
	img := renderImages(t, "q 40 0 0 40 10 20 cm /Im0 Do Q",
		map[string]Image{"Im0": {Data: im}})
	assertRegions(t, img, []region{
		{10, 30, 40, 60, red},
		{30, 50, 40, 60, blue},
		{10, 30, 60, 80, black},
		{30, 50, 60, 80, green},
	})
}

// TestImageWriteBudget: destination writes go through the same maxFillWork
// budget as fills, so a full-canvas image draw is cut off like a full-canvas
// fill.
func TestImageWriteBudget(t *testing.T) {
	defer func(v int64) { maxFillWork = v }(maxFillWork)
	maxFillWork = 16
	src := "q 100 0 0 100 0 0 cm /Im0 Do Q"
	if _, err := Page(context.Background(), []byte(src), box100, 1,
		func(string) (Image, bool) { return Image{Data: twoByTwo()}, true }); err == nil {
		t.Fatal("Page accepted image write work beyond the budget")
	}
}
