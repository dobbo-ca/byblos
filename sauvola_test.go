package byblos

import (
	"image"
	"image/color"
	"testing"
)

func grayUniform(w, h int, v uint8) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return g
}

func TestSauvolaRejectsNilImage(t *testing.T) {
	if _, err := Sauvola(nil); err == nil {
		t.Fatal("Sauvola(nil): want error, got nil")
	}
}

func TestSauvolaRejectsEmptyBounds(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 0, 0))
	if _, err := Sauvola(img); err == nil {
		t.Fatal("Sauvola on empty bounds: want error, got nil")
	}
}

// A uniform page (blank paper, no text) has zero local variance everywhere,
// so no pixel should ever cross its own local threshold: Sauvola must not
// invent ink out of a blank scan.
func TestSauvolaUniformImageProducesNoInk(t *testing.T) {
	img := grayUniform(64, 64, 235) // near-white paper
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if bm.Width != 64 || bm.Height != 64 {
		t.Fatalf("bitmap size = %dx%d, want 64x64", bm.Width, bm.Height)
	}
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if bm.At(x, y) != 0 {
				t.Fatalf("At(%d,%d) = 1, want 0 (uniform image, no ink)", x, y)
			}
		}
	}
}

// A page that is mostly white with a small black glyph-sized mark should mark
// the mark's pixels as ink and leave the surrounding white background
// untouched. The mark (6x6) is deliberately small relative to the 31x31
// window, as real text strokes are: Sauvola's threshold at a pixel is set by
// its window's local mean and variance, and a window entirely INSIDE a large
// uniform dark fill (far larger than the window) has near-zero variance and a
// near-zero mean, driving the local threshold itself to near zero -- so large
// solid dark fills are a known Sauvola blind spot, not something this test
// exercises. A window straddling a small dark mark against a light
// background instead sees strong local contrast, which is exactly the case
// Sauvola is designed for.
func TestSauvolaMarksASmallDarkMarkAsInk(t *testing.T) {
	const w, h = 100, 100
	img := grayUniform(w, h, 255)
	for y := 47; y < 53; y++ {
		for x := 47; x < 53; x++ {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if got := bm.At(50, 50); got != 1 {
		t.Errorf("At(50,50) (mark) = %d, want 1 (ink)", got)
	}
	if got := bm.At(5, 5); got != 0 {
		t.Errorf("At(5,5) (background corner) = %d, want 0 (no ink)", got)
	}
}

// Sauvola must be a pure function of its input: same image in, same bitmap
// out, every time.
func TestSauvolaIsDeterministic(t *testing.T) {
	const w, h = 50, 37
	img := grayUniform(w, h, 128)
	for y := 10; y < 20; y++ {
		for x := 5; x < 45; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x * 7) % 256)})
		}
	}
	first, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, err := Sauvola(img)
		if err != nil {
			t.Fatalf("Sauvola (run %d): %v", i, err)
		}
		if !first.Equal(again) {
			t.Fatalf("Sauvola produced different output on run %d", i)
		}
	}
}

// Sauvola accepts any image.Image, not only *image.Gray: a caller extracting
// a colour source image and thresholding it should not need to convert it by
// hand first.
func TestSauvolaAcceptsNonGrayImage(t *testing.T) {
	rgba := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			rgba.Set(x, y, color.White)
		}
	}
	if _, err := Sauvola(rgba); err != nil {
		t.Fatalf("Sauvola(*image.RGBA): %v", err)
	}
}

// A sub-image (or any image.Image whose Bounds().Min is not (0,0)) must be
// read at its own coordinates, not (0,0)-relative ones: a pixel read that
// silently offsets by Min turns real pixels into the zero value, corrupting
// every mean/variance in the window it falls in.
func TestSauvolaHandlesNonZeroOriginBounds(t *testing.T) {
	full := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			full.Set(x, y, color.White)
		}
	}
	for y := 10; y < 12; y++ {
		for x := 10; x < 12; x++ {
			full.Set(x, y, color.Black)
		}
	}
	sub := full.SubImage(image.Rect(5, 5, 15, 15)) // Bounds().Min = (5,5)

	bm, err := Sauvola(sub)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	// The black mark sits at absolute (10,10)-(12,12), i.e. relative (5,5) in
	// the 10x10 sub-image; the rest of the sub-image is white background.
	if got := bm.At(5, 5); got != 1 {
		t.Errorf("At(5,5) (mark) = %d, want 1 (ink)", got)
	}
	if got := bm.At(0, 0); got != 0 {
		t.Errorf("At(0,0) (background) = %d, want 0 (no ink)", got)
	}
}

// shadedTextPage builds the one fixture in this package that can tell an
// ADAPTIVE thresholder apart from a global one, and returns it with its
// ground-truth ink mask. It is the acceptance gate's input because byb-jj5's
// stated risk -- a binarizer that "degrades downstream OCR accuracy WITHOUT
// showing up as a size regression" -- is invisible on the corpus's scan
// images: scanpage carries FOUR distinct grey levels and zero midtones, so
// every thresholder ever written lands within a percent of every other one
// there and the algorithm is simply unobservable.
//
// The page is a strong illumination ramp -- paper running from grey 40 in the
// shadowed left edge to grey 180 at the right -- carrying stroke rows that sit
// a constant inkDepth BELOW whatever the local paper is (floored at 0). That
// makes the ink and paper intensity bands OVERLAP: ink runs 0..80 and paper
// runs 40..180, so the interval 40..80 contains both. No single cutoff can
// separate two overlapping bands, which is exactly what
// TestSauvolaSeparatesTextUnderUnevenIllumination asserts by sweeping all 257
// of them.
//
// The constants are chosen so a CORRECT Sauvola still clears the page, which
// is a real constraint rather than a free one: with k=0.5 the threshold is
// m*(0.5 + s/256), so ink only registers when it sits below roughly 0.59 of
// the local mean. At the bright end m is about 157 and s about 42, putting T
// near 105 against ink at 80 -- a 25-level margin. Shrinking inkDepth would
// widen the band overlap but push ink above T and break Sauvola honestly;
// growing it would do the reverse. inkDepth=100 against a 140-level ramp sits
// in the middle of the window where both hold.
func shadedTextPage(w, h int) (*image.Gray, []bool) {
	const (
		illumMin     = 40
		illumMax     = 180
		inkDepth     = 100
		strokePeriod = 8 // rows between the start of one stroke row and the next
		strokeHeight = 2 // rows of ink per period
	)
	g := image.NewGray(image.Rect(0, 0, w, h))
	truth := make([]bool, w*h)
	for y := 0; y < h; y++ {
		stroke := y%strokePeriod < strokeHeight
		for x := 0; x < w; x++ {
			v := illumMin + (illumMax-illumMin)*x/(w-1)
			if stroke {
				v = max(0, v-inkDepth)
				truth[y*w+x] = true
			}
			g.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return g, truth
}

// mismatchesAgainst counts the pixels where bm disagrees with a ground-truth
// ink mask, splitting them into ink invented on paper and ink missed on a
// stroke.
func mismatchesAgainst(bm *Bitmap, truth []bool) (invented, missed int) {
	for y := 0; y < bm.Height; y++ {
		for x := 0; x < bm.Width; x++ {
			ink := bm.At(x, y) == 1
			switch want := truth[y*bm.Width+x]; {
			case ink && !want:
				invented++
			case !ink && want:
				missed++
			}
		}
	}
	return invented, missed
}

// TestSauvolaSeparatesTextUnderUnevenIllumination is byb-jj5's acceptance
// gate. It asserts the property the bead actually bought -- that the
// threshold FOLLOWS THE LOCAL BACKGROUND -- against ground truth derived from
// the Sauvola formula, not against whatever the current code happens to emit
// and not against an external oracle. It therefore has identical kill power
// with and without jbig2enc installed.
//
// The gate is three assertions, and the middle one is the point:
//
//  1. Sauvola's disagreement with ground truth is at most errorMax.
//  2. NO global threshold can do that. All 257 of them are tried; the best
//     is required to be far worse. This is a self-check on the fixture: if a
//     future edit ever made the input separable by a single cutoff, the test
//     fails rather than silently going back to measuring nothing.
//  3. Sauvola beats the do-nothing null (mark no ink at all) by a stated
//     margin, so an implementation that simply stops emitting ink cannot pass.
//
// Measured on this fixture: Sauvola scores 0/57600 pixels wrong; the best
// global threshold (T=40) scores 7.1875%; threshold:=128 scores 47.1875%;
// marking nothing scores 25%; and dropping the k*s/R term so T collapses to
// m*(1-k) scores 0.7604%. errorMax=0.25% is set from that spread -- it clears
// the real implementation outright while staying 3x under the tightest
// surviving mutation.
func TestSauvolaSeparatesTextUnderUnevenIllumination(t *testing.T) {
	const w, h = 240, 240
	const errorMax = 0.0025
	const globalFloor = 0.05 // the best global cutoff must be at least this bad
	const nullMargin = 20.0  // Sauvola must beat "mark nothing" by this factor

	img, truth := shadedTextPage(w, h)
	total := float64(w * h)

	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	invented, missed := mismatchesAgainst(bm, truth)
	rate := float64(invented+missed) / total
	if rate > errorMax {
		t.Errorf("Sauvola disagrees with ground truth on %.4f%% of pixels "+
			"(%d invented on paper, %d missed on strokes); want <= %.4f%%",
			100*rate, invented, missed, 100*errorMax)
	}

	// (2) No single cutoff can reproduce that, because the ink and paper
	// intensity bands overlap. Try every one of them.
	best, bestT := 1.0, -1
	for cut := 0; cut <= 256; cut++ {
		global := NewBitmap(w, h)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if int(img.GrayAt(x, y).Y) < cut {
					global.Set(x, y, 1)
				}
			}
		}
		gi, gm := mismatchesAgainst(global, truth)
		if r := float64(gi+gm) / total; r < best {
			best, bestT = r, cut
		}
	}
	if best < globalFloor {
		t.Errorf("fixture is not discriminating: a global threshold of %d gets "+
			"within %.4f%% of ground truth, so this test would pass a "+
			"non-adaptive binarizer; want the best global cutoff to be at "+
			"least %.2f%% wrong", bestT, 100*best, 100*globalFloor)
	}

	// (3) Margin over the do-nothing null, so "emit no ink" cannot pass.
	null := NewBitmap(w, h)
	ni, nm := mismatchesAgainst(null, truth)
	nullRate := float64(ni+nm) / total
	if rate*nullMargin > nullRate {
		t.Errorf("Sauvola (%.4f%% wrong) does not beat a do-nothing binarizer "+
			"(%.4f%% wrong) by %.0fx", 100*rate, 100*nullRate, nullMargin)
	}
}

// Uneven illumination WITHOUT text must produce no ink at all: a shadowed
// page corner is still blank paper, and turning it into a solid black blob is
// the classic global-threshold failure. This is the assertion that separates
// Sauvola from plain local-mean thresholding, which the fixture above cannot:
// thresholding at the bare local mean m scores perfectly on strokes but paints
// ink over half of a texture-free ramp (measured: 48.75%), because near a
// border the clipped window's mean is pulled to the brighter side of the
// pixel. The k*s/R term is what suppresses that, and here it is the only
// thing being tested.
//
// The result is exact, not approximate. On a ramp this shallow s is about 5,
// so T = m*(0.5 + s/256) is under 0.53*m, while every pixel sits within about
// 9 levels of its own window mean; m >= 40 everywhere, so v > T with room to
// spare at every pixel including the clipped borders.
func TestSauvolaEmitsNoInkOnSmoothShadingAlone(t *testing.T) {
	const w, h = 240, 240
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8(40 + 140*x/(w-1))})
		}
	}
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	ink := 0
	firstX, firstY := -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if bm.At(x, y) == 1 {
				if ink == 0 {
					firstX, firstY = x, y
				}
				ink++
			}
		}
	}
	if ink != 0 {
		t.Errorf("smooth illumination ramp, no text: %d ink pixels (first at "+
			"(%d,%d), grey %d); want 0",
			ink, firstX, firstY, img.GrayAt(firstX, firstY).Y)
	}
}

// The bitmap Sauvola returns must be the exact type EncodeJBIG2Generic
// consumes -- that is the whole point of routing binarization through
// byblos's own Bitmap rather than a package-private type.
func TestSauvolaOutputFeedsEncodeJBIG2Generic(t *testing.T) {
	img := grayUniform(40, 40, 255)
	for y := 10; y < 30; y++ {
		for x := 10; x < 30; x++ {
			img.SetGray(x, y, color.Gray{Y: 0})
		}
	}
	bm, err := Sauvola(img)
	if err != nil {
		t.Fatalf("Sauvola: %v", err)
	}
	if _, err := EncodeJBIG2Generic(bm); err != nil {
		t.Fatalf("EncodeJBIG2Generic(Sauvola(img)): %v", err)
	}
}
