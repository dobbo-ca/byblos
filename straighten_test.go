package byblos

// Step 4 of the straighten design (docs/superpowers/specs/2026-08-14-straighten-design.md
// section 2, "Absolute is ENFORCED, not assumed"): the root package is where
// the enforcement lives, because it is the one place that has both the
// caller's absolute request and the source's prior provenance record.

import (
	"bytes"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// placementDegOf builds pages, Inspects the result, and returns page 1's
// sole image placement angle.
func placementDegOf(t *testing.T, out []byte) float64 {
	t.Helper()
	pages, err := Inspect(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("Inspect() = %+v, want one page with one image", pages)
	}
	return pages[0].Images[0].PlacementDeg
}

// TestStraightenIsAbsolute pins byb-yul.4's redelivery argument: kleio may
// call BuildFromPages twice against the SAME pristine source with the same
// absolute Deg -- a retry, not a compounding correction -- and both calls
// must land on the identical placement rather than the second one doubling
// up on the first.
func TestStraightenIsAbsolute(t *testing.T) {
	src := corpusDoc(t, "scan")
	build := func() []byte {
		var out bytes.Buffer
		err := BuildFromPages(&out, []PageSource{
			{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: 3.2}},
		})
		if err != nil {
			t.Fatalf("BuildFromPages() error = %v", err)
		}
		return out.Bytes()
	}

	deg1 := placementDegOf(t, build())
	deg2 := placementDegOf(t, build())
	if math.Abs(deg1-3.2) > 1e-6 {
		t.Errorf("first redelivery: PlacementDeg = %v, want 3.2", deg1)
	}
	if math.Abs(deg2-3.2) > 1e-6 {
		t.Errorf("second redelivery: PlacementDeg = %v, want 3.2 -- not %v (double-applied)",
			deg2, deg1+3.2)
	}
}

// TestStraightenOnAlreadyStraightenedSourceAppliesTheDifference pins the
// enforced-absolute rule itself: Deg 3.2 over a source recording 1.0 must
// apply the DIFFERENCE (2.2) and record the total (3.2); Deg 3.2 over a
// source that already records 3.2 must apply nothing at all, and the
// geometry must not move.
func TestStraightenOnAlreadyStraightenedSourceAppliesTheDifference(t *testing.T) {
	// A genuinely straightened source: built with Deg 1.0 from a fresh scan
	// that has no prior record, so the real placement AND the recorded
	// Straightened.Deg both land on exactly 1.0.
	var already bytes.Buffer
	if err := BuildFromPages(&already, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 1.0}},
	}); err != nil {
		t.Fatalf("building the already-straightened fixture: %v", err)
	}
	rec, err := ReadProvenance(bytes.NewReader(already.Bytes()))
	if err != nil || rec == nil || len(rec.Pages) != 1 ||
		rec.Pages[0].Straightened == nil || rec.Pages[0].Straightened.Deg != 1.0 {
		t.Fatalf("precondition: the fixture's own record is %+v, want Straightened.Deg = 1.0", rec)
	}
	if got := placementDegOf(t, already.Bytes()); math.Abs(got-1.0) > 1e-6 {
		t.Fatalf("precondition: the fixture's real placement is %v, want 1.0", got)
	}

	t.Run("applies the difference", func(t *testing.T) {
		var out bytes.Buffer
		if err := BuildFromPages(&out, []PageSource{
			{Source: bytes.NewReader(already.Bytes()), Page: 1, Straighten: &StraightenSpec{Deg: 3.2}},
		}); err != nil {
			t.Fatalf("BuildFromPages() error = %v", err)
		}
		if got := placementDegOf(t, out.Bytes()); math.Abs(got-3.2) > 1e-6 {
			t.Errorf("PlacementDeg = %v, want 3.2 (1.0 + the 2.2 delta), not 4.2 (1.0 + 3.2 double-applied)", got)
		}
		rec, err := ReadProvenance(bytes.NewReader(out.Bytes()))
		if err != nil || rec == nil || len(rec.Pages) != 1 ||
			rec.Pages[0].Straightened == nil {
			t.Fatalf("ReadProvenance() = %+v, %v", rec, err)
		}
		if got := rec.Pages[0].Straightened.Deg; got != 3.2 {
			t.Errorf("recorded Straightened.Deg = %v, want the TOTAL 3.2, not the 2.2 increment applied", got)
		}
	})

	t.Run("re-asking for the same total applies nothing", func(t *testing.T) {
		beforeDeg := placementDegOf(t, already.Bytes())
		var out bytes.Buffer
		if err := BuildFromPages(&out, []PageSource{
			{Source: bytes.NewReader(already.Bytes()), Page: 1, Straighten: &StraightenSpec{Deg: 1.0}},
		}); err != nil {
			t.Fatalf("BuildFromPages() error = %v", err)
		}
		afterDeg := placementDegOf(t, out.Bytes())
		if math.Abs(afterDeg-beforeDeg) > 1e-9 {
			t.Errorf("PlacementDeg moved from %v to %v; a zero delta must not move the geometry at all",
				beforeDeg, afterDeg)
		}
		rec, err := ReadProvenance(bytes.NewReader(out.Bytes()))
		if err != nil || rec == nil || len(rec.Pages) != 1 ||
			rec.Pages[0].Straightened == nil || rec.Pages[0].Straightened.Deg != 1.0 {
			t.Errorf("recorded record = %+v, %v; want Straightened.Deg = 1.0", rec, err)
		}
	})
}

// TestStraightenRecordsAppliedCapability pins design spec section 7's other
// route: Applied must gain the bare "straighten" capability, so
// anyPageApplied("straighten") (upgrade.go) works. Straightened alone is not
// enough -- Applied is the field every existing capability check reads.
func TestStraightenRecordsAppliedCapability(t *testing.T) {
	var out bytes.Buffer
	if err := BuildFromPages(&out, []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: &StraightenSpec{Deg: 1.7}},
	}); err != nil {
		t.Fatalf("BuildFromPages() error = %v", err)
	}
	rec, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil || rec == nil || len(rec.Pages) != 1 {
		t.Fatalf("ReadProvenance() = %+v, %v", rec, err)
	}
	found := false
	for _, a := range rec.Pages[0].Applied {
		if a == "straighten" {
			found = true
		}
	}
	if !found {
		t.Errorf("Applied = %v, want it to contain %q", rec.Pages[0].Applied, "straighten")
	}
	if !anyPageApplied("straighten")(rec) {
		t.Errorf("anyPageApplied(%q) = false on a straightened record", "straighten")
	}
}

// TestStraightenDoesNotMutateTheCallersPageSources pins editpages.go's own
// claim (BuildFromPagesContext's doc comment): "A copy, not a mutation of
// pages: the caller's slice and its Straighten pointers are not this call's
// to change." A caller that reuses one []PageSource across a retry sends the
// same absolute Deg again; if BuildFromPages rewrote it to the increment
// actually applied, that retry would silently apply the increment as if it
// were the absolute angle.
func TestStraightenDoesNotMutateTheCallersPageSources(t *testing.T) {
	spec := &StraightenSpec{Deg: 3.2}
	pages := []PageSource{
		{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 1, Straighten: spec},
	}

	var out bytes.Buffer
	if err := BuildFromPages(&out, pages); err != nil {
		t.Fatalf("BuildFromPages() error = %v", err)
	}

	if pages[0].Straighten != spec {
		t.Errorf("BuildFromPages replaced the caller's Straighten pointer")
	}
	if spec.Deg != 3.2 {
		t.Errorf("BuildFromPages mutated the caller's StraightenSpec.Deg to %v, want the original 3.2 untouched", spec.Deg)
	}
}

// TestStraightenTreatsUnreadableProvenanceAsUnstraightened pins the safe
// default: a source whose provenance value exists but fails to parse must be
// treated exactly like a source with no record at all, applying the FULL
// requested Deg rather than refusing or guessing at an increment.
func TestStraightenTreatsUnreadableProvenanceAsUnstraightened(t *testing.T) {
	var corrupt bytes.Buffer
	if err := pdfdoc.WriteProperties(bytes.NewReader(corpusDoc(t, "scan")), &corrupt,
		map[string]string{"byblos-provenance": "{not valid json"}); err != nil {
		t.Fatalf("writing the corrupt-provenance fixture: %v", err)
	}
	// Precondition: ReadProvenance must actually see this as unreadable, or
	// the test proves nothing about the fallback it exists to check.
	if _, err := ReadProvenance(bytes.NewReader(corrupt.Bytes())); err == nil {
		t.Fatal("precondition: ReadProvenance() accepted the corrupt fixture; want an error")
	}

	var out bytes.Buffer
	if err := BuildFromPages(&out, []PageSource{
		{Source: bytes.NewReader(corrupt.Bytes()), Page: 1, Straighten: &StraightenSpec{Deg: 2.5}},
	}); err != nil {
		t.Fatalf("BuildFromPages() error = %v", err)
	}
	if got := placementDegOf(t, out.Bytes()); math.Abs(got-2.5) > 1e-6 {
		t.Errorf("PlacementDeg = %v, want the full 2.5 applied against an unreadable prior record", got)
	}
	rec, err := ReadProvenance(bytes.NewReader(out.Bytes()))
	if err != nil || rec == nil || len(rec.Pages) != 1 ||
		rec.Pages[0].Straightened == nil || rec.Pages[0].Straightened.Deg != 2.5 {
		t.Errorf("recorded record = %+v, %v; want Straightened.Deg = 2.5", rec, err)
	}
}

// TestStraightenAcceptsAnAngleOutsideOneTurn pins what the design deliberately
// does NOT refuse (section 2, "What validate rejects"). An angle past +-180 is
// legal: the rotation matrix takes it modulo 360 by construction, so 363.2 and
// 3.2 are the same page. This is unlike PageSource.Rotate, whose refusal exists
// because the value is written into the file verbatim and pdfcpu then rejects
// what it wrote.
//
// It is pinned rather than left to arithmetic because the spec makes it a
// stated behaviour, and a later "tidy" that normalized or refused the angle
// would change a documented contract while every other test stayed green.
func TestStraightenAcceptsAnAngleOutsideOneTurn(t *testing.T) {
	src := corpusDoc(t, "scan")
	deg := func(d float64) float64 {
		t.Helper()
		var out bytes.Buffer
		if err := BuildFromPages(&out, []PageSource{
			{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: d}},
		}); err != nil {
			t.Fatalf("BuildFromPages(Deg: %v) error = %v", d, err)
		}
		return placementDegOf(t, out.Bytes())
	}

	for _, tc := range []struct{ over, want float64 }{
		{363.2, 3.2},
		{-356.8, 3.2},
		{723.2, 3.2},
	} {
		if got := deg(tc.over); math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("Deg %v: PlacementDeg = %v, want %v -- one turn is not a difference",
				tc.over, got, tc.want)
		}
	}
}

// TestStraightenCoversPageIsFalseUnderRotation pins byb-2mt's fix for what
// this test used to name the AABB lie: a rotated rectangle cannot cover an
// axis-aligned page -- the four corner triangles rotation cuts off are empty
// -- but CoversPage used to be a plain p.Page.In(p.Bounds), and Bounds is the
// AXIS-ALIGNED bounding box, which rotation GROWS by |cos t| + |sin t|. The
// box used to swallow the page and CoversPage answered true with more
// confidence than before the correction, not less; 3.26% of the page is
// genuinely uncovered at 2 degrees.
//
// CoversPage now also tests the placement's true quadrilateral (RasterQuad),
// so a rotated placement -- reachable at any angle inside maxSkewDeg, not
// only through Straighten -- correctly reports false.
func TestStraightenCoversPageIsFalseUnderRotation(t *testing.T) {
	src := corpusDoc(t, "scan")
	for _, deg := range []float64{0, 0.6, 1.0, 1.9} {
		var out bytes.Buffer
		if err := BuildFromPages(&out, []PageSource{
			{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: deg}},
		}); err != nil {
			t.Fatalf("BuildFromPages(Deg: %v) error = %v", deg, err)
		}
		r, err := ExtractPageRaster(bytes.NewReader(out.Bytes()), 1)
		if err != nil {
			t.Fatalf("Deg %v: ExtractPageRaster() error = %v; a correction inside "+
				"maxSkewDeg must still extract", deg, err)
		}
		if deg == 0 {
			// The axis-aligned no-regression pin: an unrotated placement has
			// no RasterQuad at all, and CoversPage must still say true.
			if !r.CoversPage() {
				t.Errorf("Deg 0: CoversPage() = false, want true (axis-aligned, no rotation to fail on)")
			}
			continue
		}
		if r.CoversPage() {
			t.Errorf("Deg %v: CoversPage() = true, want false: a rotated placement's "+
				"true quadrilateral cannot cover an axis-aligned page", deg)
		}
		// The evidence this used to be a lie: the AABB grew past the page it
		// claims to cover, which a raster that really covered it never would.
		// This was never the lie itself -- it is the evidence -- and stays true.
		if !r.Bounds.Eq(r.Page) && r.Bounds.In(r.Page) {
			t.Errorf("Deg %v: bounds %v sit inside page %v; expected the AABB to "+
				"GROW past the page under rotation", deg, r.Bounds, r.Page)
		}
	}

	// The PageGeometry half: RecordExtraction over the same rotated bytes
	// must agree that the page is not covered, and must have measured a
	// RasterQuad to base that on.
	var out bytes.Buffer
	if err := BuildFromPages(&out, []PageSource{
		{Source: bytes.NewReader(src), Page: 1, Straighten: &StraightenSpec{Deg: 1.9}},
	}); err != nil {
		t.Fatalf("BuildFromPages(Deg: 1.9) error = %v", err)
	}
	rec, err := RecordExtraction(bytes.NewReader(out.Bytes()))
	if err != nil || len(rec.Pages) != 1 || rec.Pages[0].Geometry == nil {
		t.Fatalf("RecordExtraction() = %+v, %v, want one page with Geometry", rec, err)
	}
	geom := rec.Pages[0].Geometry
	if geom.RasterQuad == nil {
		t.Error("Geometry.RasterQuad = nil, want a measured quad for a rotated placement")
	}
	if geom.CoversPage() {
		t.Error("Geometry.CoversPage() = true, want false: a rotated placement's true quadrilateral cannot cover an axis-aligned page")
	}
}
