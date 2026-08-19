package byblos

import (
	"bytes"
	"fmt"
	"testing"
)

// The four fixtures below are byb-2mt: replacing the axis-aligned
// bounding-box test with a true-quadrilateral one at the covers/contains and
// vector-ink sites. The matrix is exactly what byblos's own Straighten emits
// for StraightenSpec{Deg: 1.9} on a 612x792 page -- not invented, and not
// 2.0 degrees, because writing that at %.4f perturbs it past MaxSkewDeg and
// classify diverts "rotated-placement" instead, silencing every fixture here.
const rotated1p9 = "611.6635 20.2910 -26.2589 791.5646 13.2977 -9.9278"

// FIXTURE 1: a rotated top placement over a full-page axis-aligned
// under-layer. This does NOT stop at the "covers" gate -- covers is
// deliberately left AABB-only (see the comment above that call in classify),
// and top's AABB does cover the page here. It stops one check later, at
// "contains": top's TRUE quadrilateral does not reach the under-layer's own
// (page-sized) quad -- rotation cuts four corner triangles off it -- even
// though top's axis-aligned bounding box does contain it. Same code path as
// fixture 3, a different under-layer shape (full page rather than a small
// offset box) and with the lone-placement control fixture 3 does not carry.
func TestClassifyRotatedTopOverFullPageUnderlayerDivertsOnContains(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)

	t.Run("stacked under a rotated top", func(t *testing.T) {
		src := "q 612 0 0 792 0 0 cm /Im0 Do Q\n" +
			"q " + rotated1p9 + " cm /Im1 Do Q\n"
		_, got := classify(page, walkPage(t, src, ""), plainFacts)
		if got != "multiple-images" {
			t.Errorf("classify() = %q, want %q", got, "multiple-images")
		}
	})

	// The lone control: byb-b1.3 says a lone raster is never asked to cover
	// the page, and the `top > 0` gate is what preserves that -- this must
	// stay extractable, not divert, after byb-2mt.
	t.Run("lone rotated placement stays extractable", func(t *testing.T) {
		src := "q " + rotated1p9 + " cm /Im1 Do Q\n"
		idx, got := classify(page, walkPage(t, src, ""), plainFacts)
		if got != "" {
			t.Errorf("classify() = %q, want extractable (\"\")", got)
		}
		if idx != 0 {
			t.Errorf("classify() idx = %d, want 0", idx)
		}
	})
}

// FIXTURE 2: vector ink sitting in a corner triangle a rotated raster's true
// quadrilateral does not reach, even though the raster's axis-aligned
// bounding box does. This stops at inkHidden (extract.go, reached from
// paintsHidden), a DIFFERENT line from fixture 1. The fill must come first --
// inkHidden skips any placement with img.Index < order.
func TestClassifyVectorInkHiddenByRotatedTopOnly(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	src := "0 0 10 10 re f\n" +
		"q " + rotated1p9 + " cm /Im1 Do Q\n"
	_, got := classify(page, walkPage(t, src, ""), plainFacts)
	if got != "vector-paint" {
		t.Errorf("classify() = %q, want %q", got, "vector-paint")
	}
}

// FIXTURE 3: isolates the "contains" site from "covers". An oversized rotated
// top placement covers the page on both the AABB test and the true-quad test
// (it is bigger than the page even after rotation cuts its corners), so the
// covers gate passes either way. But a small axis-aligned under-layer sitting
// in one of the top's cut corners passes the AABB "contains" test while
// failing the true-quad one -- only the contains site can catch this. The
// under-layer is painted first so it really is the under-layer.
func TestClassifyContainsDivertsOnUnderLayerNotCoveredByTopQuad(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	src := "q 20 0 0 20 -160 -165 cm /Im0 Do Q\n" +
		"q 899.5052 29.8397 -36.4707 1099.3952 -125.5172 -168.6174 cm /Im1 Do Q\n"
	_, got := classify(page, walkPage(t, src, ""), plainFacts)
	if got != "multiple-images" {
		t.Errorf("classify() = %q, want %q", got, "multiple-images")
	}
}

// FIXTURE 4: the regression guard for correcting the design's "contains" fix.
// Two placements at the IDENTICAL rotated matrix -- the dup-raster/"stacked"
// shape 16,241 measured Internet Archive pages have -- must still reduce to
// one raster and extract: for identical matrices the under-layer's true quad
// is exactly the top's, so quad-vs-quad containment holds even though
// quad-vs-box containment (what an uncorrected design would test) does not.
//
// This deliberately does NOT reuse rotated1p9: at 1.9 degrees the top's own
// true quad already falls short of covering the page (fixture 1's whole
// point), so the covers gate above would divert this fixture regardless of
// the contains fix under test, and the fixture would prove nothing about
// correction 1. 1.0 degree -- the same angle the end-to-end check below uses
// -- passes the covers gate and isolates the contains site.
func TestClassifyIdenticalRotatedStackStillExtracts(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	rotated1deg := straightenedMatrixString(t, 1.0)
	src := "q " + rotated1deg + " cm /Im0 Do Q\n" +
		"q " + rotated1deg + " cm /Im1 Do Q\n"
	idx, got := classify(page, walkPage(t, src, ""), plainFacts)
	if got != "" {
		t.Errorf("classify() = %q, want extractable (\"\") -- identical rotated matrices must still reduce to one raster", got)
	}
	if idx != 1 {
		t.Errorf("classify() idx = %d, want 1 (the top placement)", idx)
	}
}

// FIXTURE 5: the regression a review of byb-2mt found (background-wash under
// a straighten). "background-wash" is a page-covering wash rectangle painted
// then a page-covering raster painted over it -- both go through the SAME
// outer rotation Straighten wraps the whole content stream in
// (internal/pdfdoc/straighten.go), so the wash's Paint.Box is the
// axis-aligned bounding box of an ALREADY-ROTATED shape. inkHidden's quad
// conjunct, naively applied to that box's own corners against img's true
// quad, rejects it: an AABB's corners are not real points of a rotated path.
// Every degree here must still extract -- "must NOT divert" is the corpus
// entry's own label -- exactly as it does at HEAD before byb-2mt.
func TestExtractBackgroundWashStraightenedStillExtracts(t *testing.T) {
	for _, deg := range []float64{0, 0.1, 0.5, 1.0, 1.9} {
		t.Run(fmt.Sprintf("deg=%v", deg), func(t *testing.T) {
			src := corpusDoc(t, "background-wash")
			var out bytes.Buffer
			if err := BuildFromPages(&out, []PageSource{
				{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: deg}},
			}); err != nil {
				t.Fatalf("BuildFromPages() error = %v", err)
			}
			if _, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1); err != nil {
				t.Errorf("ExtractPageRaster() error = %v, want success: background-wash must not divert", err)
			}
		})
	}
}

// straightenedMatrixString runs byblos's own Straighten at deg over the
// "scan" corpus fixture and returns the resulting placement's CTM formatted
// as the six space-separated operands a `cm` operator takes, so a classify
// fixture can reuse the EXACT matrix Straighten produces rather than a
// hand-derived approximation of it.
func straightenedMatrixString(t *testing.T, deg float64) string {
	t.Helper()
	var out bytes.Buffer
	if err := BuildFromPages(&out, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: deg}},
	}); err != nil {
		t.Fatalf("BuildFromPages(Deg: %v) error = %v", deg, err)
	}
	pages, err := Inspect(bytes.NewReader(out.Bytes()))
	if err != nil || len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("Inspect() = %+v, %v, want one page with one image", pages, err)
	}
	m := pages[0].Images[0].Placement
	return fmt.Sprintf("%.4f %.4f %.4f %.4f %.4f %.4f", m[0], m[1], m[2], m[3], m[4], m[5])
}

// The end-to-end half of fixture 4: the real corpus document 16,241 measured
// Internet Archive pages are shaped like (internal/corpus/corpus.go:208,
// extract.go:171-176 and :756) must still extract after a straighten pass
// that leaves the placement rotated within MaxSkewDeg. An uncorrected design
// that tested quad-vs-box at the contains site breaks this document.
func TestExtractStackedStraightenedStillExtracts(t *testing.T) {
	src := corpusDoc(t, "stacked")
	var out bytes.Buffer
	if err := BuildFromPages(&out, []PageSource{
		{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: 1.0}},
	}); err != nil {
		t.Fatalf("BuildFromPages() error = %v", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1); err != nil {
		t.Errorf("ExtractPageRaster() error = %v, want success: byb-2mt must not break the stacked shape", err)
	}
}
