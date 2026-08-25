// This file holds the CTM census's own arithmetic (shearDegrees,
// skewDegrees, placementDeg) plus a byte-for-byte reimplementation of
// classify (extract.go) and the private helpers it calls, so this tool can
// compute classify's divert reason for every page without decoding a single
// image -- byb-06n's recon (part C) established that every gate up to and
// including placementReason is decidable from one content.Walk, so this is
// legitimate and not a guess.
//
// WHY DUPLICATED RATHER THAN CALLED: classify and its helpers
// (paintsHidden, mrcLayers, opaqueCover, clipNarrowed, covers, contains,
// marks) are unexported in package byblos. This file is a deliberate,
// line-for-line port of extract.go's logic, re-synced as of byb-ntd, kept
// beside the pinned unit tests in census_test.go so a future drift between
// the two is a test failure here, not a silent mismatch between two lanes
// measuring different predicates (see the project's own "two lanes can
// both be right" lesson).
//
// byb-ntd is why that claim is only true as of this commit and not before
// it: the port had already drifted from extract.go -- inkHidden here lacked
// inkCTM and the byb-2mt three-way rotation guard, and classify here lacked
// the quad-vs-quad under-layer check -- for an unmeasured stretch, and
// census_test.go carried zero tests of classify, paintsHidden or inkHidden
// to catch it. TestPaintsHiddenRotatedCoverExcludesUnrotatedInk and
// TestClassifyRotatedTopExcludesUnderLayerOutsideItsTrueQuad below are what
// make "a future drift is a test failure here" true going forward.
package main

import (
	"math"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// --- byb-06n's own arithmetic, fixed by the task spec ---

// skewDegrees is extract.go's skewDegrees, verbatim.
func skewDegrees(m [6]float64) float64 {
	x := math.Atan2(math.Abs(m[1]), math.Abs(m[0]))
	y := math.Atan2(math.Abs(m[2]), math.Abs(m[3]))
	return max(x, y) * 180 / math.Pi
}

// placementDeg is inspect.go's placementDeg, verbatim.
func placementDeg(m [6]float64) float64 {
	return math.Atan2(m[1], m[0]) * 180 / math.Pi
}

// shearDegrees is the angle between the CTM's two column vectors, away from
// perpendicular: u=(m0,m1), v=(m2,m3), shear = |asin((u.v)/(|u||v|))| in
// degrees. Zero for any placement whose axes are perpendicular -- a pure
// rotation, scale, translation or reflection, none of which shear a
// rectangle -- and away from zero exactly for a parallelogram.
//
// DEGENERATE INPUT: a zero-length column (m0==m1==0 or m2==m3==0) has no
// defined direction, so the angle between it and the other column is
// undefined. shearDegrees returns math.NaN() for that placement, and
// degenerate() (below) reports true so a caller can single it out rather
// than let it silently become a false zero or corrupt a percentile/bucket
// sum with a NaN sorting to an arbitrary place.
func shearDegrees(m [6]float64) float64 {
	if degenerate(m) {
		return math.NaN()
	}
	// atan2 of (|u x v|, u . v) is the angle BETWEEN the columns; how far that
	// sits from perpendicular is the shear. The obvious spelling --
	// asin(dot / (|u| |v|)) -- agrees with this to 2.8e-14 over all 1,490,723
	// placements of the pinned sample and is still the wrong one to ship: the
	// product |u| |v| UNDERFLOWS to zero, or overflows to +Inf, for finite
	// non-degenerate columns, and 0/0 is a NaN that degenerate() does not
	// report. Twenty nested `.0000000001 0 0 .0000000001 0 0 cm` is enough,
	// which is ordinary PDF syntax, and one such page anywhere in a corpus
	// aborts the whole sweep at json.Encode and leaves a zero-byte file.
	// atan2 has neither failure mode and needs no clamp for float slop.
	cross := m[0]*m[3] - m[1]*m[2]
	dot := m[0]*m[2] + m[1]*m[3]
	return math.Abs(90 - math.Atan2(math.Abs(cross), dot)*180/math.Pi)
}

// degenerate reports whether either of the CTM's two columns has zero
// length, which is exactly when shearDegrees has no defined answer.
func degenerate(m [6]float64) bool {
	return (m[0] == 0 && m[1] == 0) || (m[2] == 0 && m[3] == 0)
}

// --- classify, ported from extract.go, and the helpers it needs ---

const (
	coverTolerancePt     = 1.0
	maxSkewDeg           = 2.0
	mrcPatchAreaFrac     = 0.02
	mrcBaseAreaFrac      = 0.90
	paintTolerancePt     = 1e-3
	paintFillTolerancePt = 1.0
)

// classify is extract.go's classify, ported. See the file doc comment for
// why this is a port rather than a call.
func classify(page pdfdoc.Rect, s *content.Scan, imageInfo func(int) (pdfdoc.ImageInfo, bool)) (int, string) {
	switch {
	case len(s.Images) == 0:
		return 0, "no-image"
	case s.InkedTextOps > 0:
		return 0, "has-text"
	case s.InlineImgs > 0:
		return 0, "inline-image"
	case !paintsHidden(s.Images, s.Paints, imageInfo):
		return 0, "vector-paint"
	case s.ShadingOps > 0:
		return 0, "shading"
	case len(s.Unresolved) > 0:
		return 0, "unresolved-xobject"
	}
	if reason := mrcLayers(page, s.Images, imageInfo); reason != "" {
		return 0, reason
	}

	top := len(s.Images) - 1
	if clipNarrowed(s.Images[top]) && !marksBox(s.Images[top].Box) {
		if top > 0 {
			return 0, "multiple-images"
		}
		return 0, "clipped-away"
	}
	if reason := placementReason(s.Images[top]); reason != "" {
		if top > 0 {
			return 0, "multiple-images"
		}
		return 0, reason
	}

	if top > 0 {
		if !covers(s.Images[top].Box, page) {
			return 0, "multiple-images"
		}
		topCTM := s.Images[top].CTM
		if !opaqueCover(s.Images[top], imageInfo) {
			return 0, "transparent-overlay"
		}
		for _, under := range s.Images[:top] {
			if !contains(s.Images[top].Box, under.Box) {
				return 0, "multiple-images"
			}
			// Quad-vs-quad under-layer check, ported from extract.go:903-907
			// (byb-ntd): contains above compares AXIS-ALIGNED boxes, which
			// over-forgives a rotated top layer the same way covers() would if
			// left unguarded -- see extract.go:872-884. Skipped for an
			// axis-aligned top, where Box already IS the true shape.
			if !axisAligned(topCTM) {
				if !topCTM.UnitSquareQuad().ContainsQuad(under.CTM.UnitSquareQuad(), coverTolerancePt) {
					return 0, "multiple-images"
				}
			}
		}
	}
	return top, ""
}

// placementReason is extract.go's placementReason, ported.
func placementReason(p content.Placement) string {
	m := p.CTM
	if skewDegrees([6]float64(m)) > maxSkewDeg {
		return "rotated-placement"
	}
	if m[0] <= 0 || m[3] <= 0 {
		return "flipped-placement"
	}
	return ""
}

func clipNarrowed(p content.Placement) bool {
	return p.Clip != nil && p.Box != p.CTM.UnitSquareBox()
}

func marksBox(b content.Box) bool { return b.URX > b.LLX && b.URY > b.LLY }

func contains(outer, inner content.Box) bool {
	return outer.LLX <= inner.LLX+coverTolerancePt &&
		outer.LLY <= inner.LLY+coverTolerancePt &&
		outer.URX >= inner.URX-coverTolerancePt &&
		outer.URY >= inner.URY-coverTolerancePt
}

func covers(b content.Box, page pdfdoc.Rect) bool {
	return contains(b, content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
}

func area(b content.Box) float64 { return (b.URX - b.LLX) * (b.URY - b.LLY) }

func opaqueCover(p content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	if !p.Opaque {
		return false
	}
	info, ok := imageInfo(p.ID)
	return ok && !info.ImageMask && !info.SMask && !info.Mask
}

func mrcLayers(page pdfdoc.Rect, imgs []content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) string {
	pageArea := area(content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
	if pageArea <= 0 {
		return ""
	}
	var base, patch bool
	for i, p := range imgs {
		info, ok := imageInfo(p.ID)
		if !ok {
			continue
		}
		if info.BPC == 1 || info.ImageMask {
			base = base || (i < len(imgs)-1 && area(p.Box)/pageArea >= mrcBaseAreaFrac)
			continue
		}
		patch = patch || area(p.Box)/pageArea > mrcPatchAreaFrac
	}
	if base && patch {
		return "mrc-layers"
	}
	return ""
}

// axisAligned is extract.go's axisAligned, ported (byb-ntd).
func axisAligned(m content.Matrix) bool {
	return m[1] == 0 && m[2] == 0
}

// sameRotation is extract.go's sameRotation, ported (byb-ntd).
func sameRotation(a, b content.Matrix) bool {
	an, bn := math.Hypot(a[0], a[1]), math.Hypot(b[0], b[1])
	if an == 0 || bn == 0 {
		return false
	}
	cross := a[0]*b[1] - a[1]*b[0]
	dot := a[0]*b[0] + a[1]*b[1]
	return dot > 0 && math.Abs(cross)/(an*bn) < 1e-6
}

func paintsHidden(imgs []content.Placement, paints []content.Paint, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	// covers is opaqueCover per placement, answered once, mirroring
	// extract.go's paintsHidden (byb-kpi).
	covers := make([]bool, len(imgs))
	for i := range imgs {
		covers[i] = opaqueCover(imgs[i], imageInfo)
	}
	for _, p := range paints {
		ink, marks := p.Ink()
		if !marks {
			continue
		}
		tol := paintFillTolerancePt
		if p.Strokes() {
			tol = paintTolerancePt
		}
		if !inkHidden(ink, p.CTM, p.Index, tol, imgs, covers) {
			return false
		}
	}
	return true
}

// inkHidden is extract.go's inkHidden, ported (byb-ntd): the census's own
// copy dropped inkCTM and the AABB-over-forgives-rotation guard, so a
// rotated cover with unrotated ink underneath -- an under-quad triangle the
// AABB test does not see -- reported hidden here and not hidden in
// extract.go. See extract.go:1188-1231 for the full argument.
func inkHidden(ink content.Box, inkCTM content.Matrix, order int, tol float64, imgs []content.Placement, covers []bool) bool {
	for i := range imgs {
		img := &imgs[i]
		if img.Index < order || !covers[i] {
			continue
		}
		if ink.LLX >= img.Box.LLX-tol &&
			ink.LLY >= img.Box.LLY-tol &&
			ink.URX <= img.Box.URX+tol &&
			ink.URY <= img.Box.URY+tol {
			if axisAligned(img.CTM) || sameRotation(inkCTM, img.CTM) || img.CTM.UnitSquareQuad().ContainsBox(ink, tol) {
				return true
			}
		}
	}
	return false
}
