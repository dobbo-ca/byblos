package byblos

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/skew"
)

// linesPage draws parallel ink lines rotated by deg degrees in IMAGE
// coordinates (y running down), the same backward-mapping technique
// internal/skew/skew_test.go's textPage uses to synthesise ground truth
// analytically rather than by resampling a bitmap. It is reproduced here,
// smaller, because textPage is unexported and this package cannot reach it:
// a positive deg carries the right-hand end of each line DOWNWARD in the
// image, exactly as textPage's own doc comment states.
func linesPage(w, h int, deg float64) *image.Gray {
	rad := deg * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	cx, cy := float64(w)/2, float64(h)/2
	img := image.NewGray(image.Rect(0, 0, w, h))
	const lineH, lineGap, margin = 11.0, 9.0, 60.0
	for y := range h {
		for x := range w {
			dx, dy := float64(x)-cx, float64(y)-cy
			px := dx*cos + dy*sin + cx
			py := -dx*sin + dy*cos + cy
			v := uint8(235)
			if px >= margin && px <= float64(w)-margin &&
				py >= margin && py <= float64(h)-margin &&
				math.Mod(py-margin, lineH+lineGap) <= lineH {
				v = 30
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// TestPlacementDegMatchesSkewConvention is byb-16j.4's real obligation for
// this step: internal/skew/skew.go:71-72 justifies its whole sign convention
// by saying it matches "ImageRef.Placement's atan2(b, a)" -- a computation
// that, before PlacementDeg, existed nowhere in production, only in
// skew_probe_test.go. Nothing would have failed if Placement's sign meaning
// drifted. This pins both sides against one shared angle.
//
// It uses TWO INDEPENDENT constructions of the same physical rotation: a
// raster rotated in pixel space and read by package skew's projection-profile
// estimator, and a paint matrix built directly from cos/sin and read by
// PlacementDeg's atan2. THE TWO SIDES SHARE NOTHING, the same property
// TestSkewInstrumentAgainstMeasuredDeskew (skew_probe_test.go:428-455) relies
// on over the real 005393.pdf -- but this runs on plain `go test ./...`, with
// no corpus file and no BYBLOS_SKEW_ANCHOR.
func TestPlacementDegMatchesSkewConvention(t *testing.T) {
	const want = 5.0 // arbitrary, nonzero, and positive so a math.Abs bug is visible

	t.Run("agrees with skew.Estimate in sign", func(t *testing.T) {
		// TestSignIsUserSpace and TestRecoversKnownAngle (internal/skew) pin
		// that a raster drawn with image-space parameter P reads back as
		// user-space angle -P: draw at -want to recover +want.
		est := skew.Measure(linesPage(700, 900, -want), skew.Options{})
		if !est.OK {
			t.Fatalf("no estimate: conf %.3f", est.Confidence)
		}
		const tol = 0.2 // skew's own recovery tolerance is 4*fineStep = 0.1
		if math.Abs(est.Deg-want) > tol {
			t.Fatalf("content angle: got %+.3f, want %+.3f +/- %.2f", est.Deg, want, tol)
		}

		// An independent construction: a paint matrix rotated by the SAME
		// angle, built directly from cos/sin rather than from a rasterized
		// pattern -- the matrix form spec section 2 gives for StraightenSpec.
		rad := want * math.Pi / 180
		m := [6]float64{math.Cos(rad), math.Sin(rad), -math.Sin(rad), math.Cos(rad), 0, 0}
		got := placementDeg(m)
		if math.Abs(got-want) > 1e-6 {
			t.Fatalf("placementDeg(%v) = %+.6f, want %+.6f", m, got, want)
		}

		// The point of the test: content skew and placement angle, measured
		// by two implementations that share no code, agree on which
		// direction is positive. Inverting either one -- a sign flip in
		// skew.Estimate.Deg, or computing atan2(a, b) instead of
		// atan2(b, a) -- fails this.
		if math.Signbit(est.Deg) != math.Signbit(got) {
			t.Fatalf("sign mismatch: content skew %+.3f, placement angle %+.3f", est.Deg, got)
		}
	})

	t.Run("axis-aligned is exactly zero", func(t *testing.T) {
		m := [6]float64{306, 0, 0, 396, 0, 0} // a page-covering, unrotated placement
		if got := placementDeg(m); got != 0 {
			t.Errorf("placementDeg(axis-aligned) = %v, want exactly 0", got)
		}
	})

	t.Run("is signed, not math.Abs", func(t *testing.T) {
		pos := deskewedPlacement(3)[0].CTM
		neg := deskewedPlacement(-3)[0].CTM
		gotPos := placementDeg([6]float64(pos))
		gotNeg := placementDeg([6]float64(neg))
		if gotPos <= 0 {
			t.Errorf("placementDeg(+3deg placement) = %v, want positive", gotPos)
		}
		if gotNeg >= 0 {
			t.Errorf("placementDeg(-3deg placement) = %v, want negative", gotNeg)
		}
		if math.Abs(gotPos-3) > 1e-6 || math.Abs(gotNeg+3) > 1e-6 {
			t.Errorf("placementDeg = %+.6f / %+.6f, want +3 / -3", gotPos, gotNeg)
		}
		// math.Abs would collapse these two to the same value; assert it does
		// not, so a caller relying on the sign catches a regression here.
		if math.Abs(gotPos) != math.Abs(gotNeg) {
			t.Fatalf("test fixture is not a mirror pair: |%v| != |%v|", gotPos, gotNeg)
		}
		if gotPos == gotNeg {
			t.Errorf("placementDeg(+3) == placementDeg(-3) == %v; PlacementDeg must be signed", gotPos)
		}
	})
}
