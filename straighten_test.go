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
