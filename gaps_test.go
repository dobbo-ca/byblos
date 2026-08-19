package byblos

import (
	"bytes"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
)

// The three gaps the kleio page-editing lane hit against v0.3.0 and the seams
// that close them: G3 (MaxSkewDeg exported), G4 (RasterRefusal), G2
// (ValidatePages). The gap list is kleio
// docs/superpowers/notes/2026-08-17-byblos-page-editing-gaps.md.

// --- G4: the refusal reason is a type ---------------------------------------

// TestRasterRefusalCarriesTheReason is the whole point of G4: a consumer reads
// the fine reason WITHOUT parsing the message. The document/reason table is the
// same one TestExtractPageRasterDiverts pins on the message, deliberately, so a
// new divert reason cannot be added to one and forgotten in the other.
func TestRasterRefusalCarriesTheReason(t *testing.T) {
	// msg is what the MESSAGE still has to say, which is not always the reason
	// -- and that difference is G4 itself. The classify family interpolates its
	// reason verbatim; the codec family names the codec and never the reason
	// string, so a consumer parsing messages could not have recovered
	// "unsupported-codec-jbig2" at all.
	for _, tc := range []struct{ doc, reason, class, msg string }{
		{"born-digital", "no-image", "not-single-raster", "no-image"},
		{"overlay-text", "has-text", "not-single-raster", "has-text"},
		{"tiled", "multiple-images", "not-single-raster", "multiple-images"},
		{"overlay-vector", "vector-paint", "not-single-raster", "vector-paint"},
		{"mrc", "mrc-layers", "not-single-raster", "mrc-layers"},
		{"scan-quarter-turn", "rotated-placement", "not-single-raster", "rotated-placement"},
		{"scan-mirrored", "flipped-placement", "not-single-raster", "flipped-placement"},
		{"jbig2", "unsupported-codec-jbig2", "unsupported-codec-jbig2", "jbig2"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			data := corpusDoc(t, tc.doc)
			_, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if err == nil {
				t.Fatal("ExtractPageRaster() succeeded; want a refusal")
			}
			var ref *RasterRefusal
			if !errors.As(err, &ref) {
				t.Fatalf("errors.As(*RasterRefusal) = false for %v; the refusal reason is "+
					"readable only by parsing the message again", err)
			}
			if ref.Reason != tc.reason {
				t.Errorf("Reason = %q; want %q", ref.Reason, tc.reason)
			}
			if ref.Class != tc.class {
				t.Errorf("Class = %q; want %q", ref.Class, tc.class)
			}
			if ref.Page != 1 {
				t.Errorf("Page = %d; want 1", ref.Page)
			}
			// The type is an ADDITION. Everything a caller could do before it
			// existed still has to work, or the seam is a breaking change.
			if !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error = %q; want it to still say %q in its message", err, tc.msg)
			}
			if ref.Error() != err.Error() {
				t.Errorf("RasterRefusal.Error() = %q; want the unchanged chain message %q", ref.Error(), err)
			}
		})
	}
}

// The sentinels are what kleio's 422-vs-500 split branches on today, and
// RasterRefusal must not have moved them out of reach.
func TestRasterRefusalKeepsTheSentinelsReachable(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want error
	}{
		{"tiled", ErrNotSingleRaster},
		{"jbig2", ErrUnsupportedImageCodec},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			_, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, tc.doc)), 1)
			if !errors.Is(err, tc.want) {
				t.Fatalf("errors.Is(%v) = false; want it still to answer", tc.want)
			}
		})
	}
}

// The refusal a caller reads by type and the one the provenance record carries
// come from one place (refuse), so this pins that they agree. They are not the
// same vocabulary -- Reason is fine, Diverted is divertClass's coarse class --
// and the honest check is Class, not Reason.
func TestRasterRefusalClassMatchesTheProvenanceRecord(t *testing.T) {
	rec, err := RecordExtraction(bytes.NewReader(corpusDoc(t, "tiled")))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(rec.Pages) == 0 {
		t.Fatal("RecordExtraction() recorded no pages")
	}
	_, xerr := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "tiled")), 1)
	var ref *RasterRefusal
	if !errors.As(xerr, &ref) {
		t.Fatalf("errors.As(*RasterRefusal) = false for %v", xerr)
	}
	if ref.Class != rec.Pages[0].Diverted {
		t.Errorf("RasterRefusal.Class = %q but PageProvenance.Diverted = %q; the two "+
			"readings of one refusal disagree", ref.Class, rec.Pages[0].Diverted)
	}
}

// --- G3: the straighten envelope is exported --------------------------------

// MaxSkewDeg is exported so a UI slider clamp cannot drift from the threshold
// this package actually diverts on. That is only true while ONE constant drives
// both, which is what skewDegrees is compared against here.
func TestMaxSkewDegIsTheDivertThreshold(t *testing.T) {
	if MaxSkewDeg != 2.0 {
		t.Errorf("MaxSkewDeg = %v; kleio's editor pinned 2.0 while this was unexported "+
			"(gap G3), so a change here is a change kleio has to be told about", MaxSkewDeg)
	}
	// Just inside diverts nothing; just outside diverts. Anything else means
	// the exported number is not the number that decides.
	inside := skewDegrees(deskewedPlacement(MaxSkewDeg * 0.99)[0].CTM)
	if inside > MaxSkewDeg {
		t.Errorf("a placement just inside the envelope measured %v degrees, past MaxSkewDeg %v", inside, MaxSkewDeg)
	}
	outside := skewDegrees(deskewedPlacement(MaxSkewDeg * 1.01)[0].CTM)
	if outside <= MaxSkewDeg {
		t.Errorf("a placement just outside the envelope measured %v degrees, within MaxSkewDeg %v", outside, MaxSkewDeg)
	}
}

// --- G2: an edit list can be checked without a build ------------------------

// ValidatePages must refuse everything BuildFromPages refuses BEFORE opening a
// document -- that is the whole 422-at-PUT-time claim -- and it must not read
// from any Source while doing it.
func TestValidatePagesRefusesWithoutOpeningASource(t *testing.T) {
	good := func() io.ReadSeeker { return bytes.NewReader(corpusDoc(t, "scan")) }
	for _, tc := range []struct {
		name  string
		pages []PageSource
		want  string
	}{
		{"no pages", nil, "no pages"},
		{"no source", []PageSource{{Page: 1}}, "no source"},
		{"rotation 45", []PageSource{{Source: good(), Page: 1, Rotate: 45}}, "not one of 0, 90, 180, 270"},
		{"rotation 360", []PageSource{{Source: good(), Page: 1, Rotate: 360}}, "not one of 0, 90, 180, 270"},
		{"straighten NaN", []PageSource{{Source: good(), Page: 1,
			Straighten: &StraightenSpec{Deg: math.NaN()}}}, "not finite"},
		{"straighten Inf", []PageSource{{Source: good(), Page: 1,
			Straighten: &StraightenSpec{Deg: math.Inf(1)}}}, "not finite"},
		{"crop", []PageSource{{Source: good(), Page: 1,
			Straighten: &StraightenSpec{Crop: &[4]float64{0, 0, 10, 10}}}}, "not implemented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePages(tc.pages)
			if err == nil {
				t.Fatalf("ValidatePages() = nil; want an error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("ValidatePages() error = %q; want it to name %q", err, tc.want)
			}
			// The same list has to reach the same verdict through the build,
			// or the pre-check is a second opinion rather than the same one.
			if berr := BuildFromPages(io.Discard, tc.pages); berr == nil {
				t.Error("BuildFromPages() accepted a list ValidatePages refused")
			}
		})
	}
}

// A list ValidatePages accepts and a reader it never touched: the seek offset
// is the evidence. A handler holding a not-yet-fetched source must still be
// able to validate.
func TestValidatePagesAcceptsAGoodListAndReadsNothing(t *testing.T) {
	r := bytes.NewReader(corpusDoc(t, "scan"))
	pages := []PageSource{
		{Source: r, Page: 1, Rotate: 90},
		{Source: r, Page: 1, Straighten: &StraightenSpec{Deg: 1.2}},
	}
	if err := ValidatePages(pages); err != nil {
		t.Fatalf("ValidatePages() error = %v; want nil", err)
	}
	if at, err := r.Seek(0, io.SeekCurrent); err != nil || at != 0 {
		t.Errorf("source read to offset %d (err %v); ValidatePages must not touch a Source", at, err)
	}
}

// The honest limit, pinned so nobody reads a nil return as "this will build":
// an out-of-range page needs the document open, so ValidatePages passes it and
// BuildFromPages is the one that refuses.
func TestValidatePagesCannotSeeAnOutOfRangePage(t *testing.T) {
	pages := []PageSource{{Source: bytes.NewReader(corpusDoc(t, "scan")), Page: 9999}}
	if err := ValidatePages(pages); err != nil {
		t.Fatalf("ValidatePages() error = %v; it cannot know the page count and must not guess", err)
	}
	err := BuildFromPages(io.Discard, pages)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Errorf("BuildFromPages() error = %v; want the out-of-range refusal ValidatePages cannot make", err)
	}
}
