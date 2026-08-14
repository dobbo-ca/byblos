package pdfdoc

// The straighten contract and the lossless content matrix (byb-16j.4,
// byb-16j.2), design spec sections 2-4.

import (
	"fmt"
	"math"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// StraightenSpec is an absolute correction to apply to one page's content.
//
// Deg is the rotation byblos applies, in degrees, positive COUNTER-CLOCKWISE
// in PDF default user space. That is the same signed convention as
// skew.Estimate.Deg (internal/skew/skew.go:70-74), pinned there by
// TestSignIsUserSpace, and it is stated in exactly one place for both sides
// of the repository boundary.
//
// It is ABSOLUTE. It is the angle from the ORIGINAL page, never a delta on
// whatever is already applied. kleio redelivers a job at least once
// (ocr.go:55-60), so a transform that composes with what is already there
// corrupts the page on a retry. This is the same argument byb-yul.4 settled
// for PageSource.Rotate.
//
// ABSOLUTE IS ENFORCED, NOT ASSUMED. This package's BuildFromPages only
// applies the angle it is given; it has no way to see a document's own
// provenance. The enforcement lives one layer up, in the root package's
// BuildFromPagesContext: the rotation it hands down to this package is Deg
// minus whatever the source page's provenance already records as
// Straightened.Deg, defaulting to zero, and the record it writes afterwards
// is always the total (Deg), never the increment actually applied.
//
// To straighten a page flat: measure the raster's content angle D with
// internal/skew, read the placement angle p from ImageRef.PlacementDeg, and
// set Deg = -(p + D). The two cancel on a page that already reads straight.
type StraightenSpec struct {
	Deg float64
	// Crop is [llx lly urx ury] in the source page's unrotated PDF user
	// space (design spec section 6). It is refused when non-nil: cropping
	// is not implemented in this version, and refusing is what lets the
	// field exist in the contract now without a caller silently getting a
	// page that ignored it.
	Crop *[4]float64
}

// applyStraighten wraps every straightened page's content in a rotation
// about its own CropBox (or MediaBox, absent one) centre.
//
// It runs AFTER buildContext has assembled ctx's whole page tree, not during
// migratePage. WrapContent resolves a page number by walking the catalog's
// /Pages (xt.PageDict), and that tree is only a reservation -- Kids is empty
// -- until buildContext's own /Pages object is filled in at the very end of
// the walk.
func applyStraighten(ctx *model.Context, pages []PageSource) error {
	d := &doc{ctx: ctx}
	for i, p := range pages {
		if p.Straighten == nil {
			continue
		}
		n := i + 1
		cx, cy, err := pageCentre(ctx.XRefTable, n)
		if err != nil {
			return fmt.Errorf("byblos/pdfdoc: build from pages: page %d: straighten: %w", n, err)
		}
		m := rotationMatrix(p.Straighten.Deg, cx, cy)
		before := []byte(fmt.Sprintf("q\n%.10f %.10f %.10f %.10f %.10f %.10f cm\n",
			m.a, m.b, m.c, m.d, m.e, m.f))
		// Leading "\n": pdfcpu's PageContent (and every reader) concatenates
		// /Contents array members with no separator, so this wrapper's own
		// "Q" would otherwise fuse with whatever byte the LAST existing
		// stream happens to end on -- a real shape ("...Do Q" with no
		// trailing newline) that made a straightened export unreadable on
		// its own re-straighten. "before" needs no matching fix: it already
		// ends in "\n" from the cm line above, and it is prepended before
		// the existing content rather than appended after it.
		after := []byte("\nQ\n")
		if err := d.WrapContent(n, before, after); err != nil {
			return fmt.Errorf("byblos/pdfdoc: build from pages: page %d: straighten: %w", n, err)
		}
	}
	return nil
}

// pageCentre returns the centre of page n's CropBox, or its MediaBox when it
// declares no CropBox -- the same fallback Page() applies, and the box the
// design spec's section 3 names as the one byblos itself treats as the page
// (inspect.go:247, extract.go:369, extract.go:523, extract.go:560).
//
// migratePage has already given every output page a /MediaBox by the time
// this runs (buildpages.go), so a nil box here would be this package's own
// bug rather than a caller's malformed input.
func pageCentre(xt *model.XRefTable, n int) (cx, cy float64, err error) {
	_, _, inh, err := xt.PageDict(n, false)
	if err != nil {
		return 0, 0, fmt.Errorf("page %d dict: %w", n, err)
	}
	if inh == nil {
		return 0, 0, fmt.Errorf("page %d has no inherited attributes", n)
	}
	box := inh.CropBox
	if box == nil {
		box = inh.MediaBox
	}
	if box == nil {
		return 0, 0, fmt.Errorf("page %d has no page box", n)
	}
	return (box.LL.X + box.UR.X) / 2, (box.LL.Y + box.UR.Y) / 2, nil
}

// rotationMatrix is the design spec section 3 matrix for angle tDeg about
// centre (cx, cy):
//
//	a =  cos t     c = -sin t     e = cx(1 - cos t) + cy*sin t
//	b =  sin t     d =  cos t     f = cy(1 - cos t) - cx*sin t
//
// (cx, cy) is a fixed point of the result: applying it to (cx, cy) returns
// (cx, cy) unchanged, whatever t is. Rotating about the origin instead was
// measured to push a placement off the page edge (design spec section 3).
func rotationMatrix(tDeg, cx, cy float64) matrix {
	t := tDeg * math.Pi / 180
	cosT, sinT := math.Cos(t), math.Sin(t)
	return matrix{
		a: cosT, b: sinT, c: -sinT, d: cosT,
		e: cx*(1-cosT) + cy*sinT,
		f: cy*(1-cosT) - cx*sinT,
	}
}
