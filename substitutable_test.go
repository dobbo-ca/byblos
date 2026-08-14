package byblos

import (
	"bytes"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestImageRefSubstitutableLetsACallerPreFilterASubstitutionMap is byb-js5.2's
// acceptance. ReplaceImages is all-or-nothing -- "the first failure aborts the
// whole call and nothing is written" -- so a caller driving the per-image loop
// under byb-fp6's option (P) has to be able to see a refusal coming. Before this
// field, ImageRef exposed nothing about /SMask, /Mask, /ImageMask or a direct
// object, and the caller learned about them as an untyped error string after the
// whole batch was discarded.
func TestImageRefSubstitutableLetsACallerPreFilterASubstitutionMap(t *testing.T) {
	in := corpusDoc(t, "stacked-smask")
	pages, err := Inspect(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 2 {
		t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 2",
			len(pages), len(pages[0].Images))
	}
	all := pages[0].Images
	good := flateGrayImage(t, 8, 8, 3)

	// The unfiltered map must actually be refused, or the filtered one below
	// proves nothing.
	unfiltered := map[int]EncodedImage{}
	for _, ref := range all {
		unfiltered[ref.ObjNr] = good
	}
	var out bytes.Buffer
	if err := ReplaceImages(&out, bytes.NewReader(in), unfiltered); err == nil {
		t.Fatal("substituting every image on the page succeeded; the fixture no " +
			"longer exercises a refusal and this test cannot detect one")
	}
	if out.Len() != 0 {
		t.Errorf("wrote %d bytes for a call that failed", out.Len())
	}

	filtered := map[int]EncodedImage{}
	for _, ref := range all {
		if ref.Substitutable {
			filtered[ref.ObjNr] = good
		}
	}
	// It has to remove SOME and keep SOME. A field stuck at false would give an
	// empty map, which ReplaceImages refuses for a different reason entirely;
	// one stuck at true would give back the map that just failed.
	if len(filtered) == 0 || len(filtered) == len(unfiltered) {
		t.Fatalf("Substitutable kept %d of %d images; the page holds one image "+
			"the seam accepts and one it refuses", len(filtered), len(unfiltered))
	}
	out.Reset()
	if err := ReplaceImages(&out, bytes.NewReader(in), filtered); err != nil {
		t.Fatalf("a substitution map filtered by Substitutable was still refused: %v", err)
	}
	if out.Len() == 0 {
		t.Error("the filtered substitution wrote nothing")
	}
}

// TestImageRefSubstitutableIsFalseForAnSMask pins the mapping itself, not just
// the aggregate above: an image the seam refuses reports false, and one it takes
// reports true, on the same page.
func TestImageRefSubstitutableIsFalseForAnSMask(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpusDoc(t, "stacked-smask")))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	good := flateGrayImage(t, 8, 8, 3)
	for _, ref := range pages[0].Images {
		var out bytes.Buffer
		err := ReplaceImages(&out, bytes.NewReader(corpusDoc(t, "stacked-smask")),
			map[int]EncodedImage{ref.ObjNr: good})
		accepted := err == nil
		if accepted != ref.Substitutable {
			t.Errorf("image %d reports Substitutable %v; ReplaceImages accepted it: %v (%v)",
				ref.ObjNr, ref.Substitutable, accepted, err)
		}
	}
}

// TestImageRefBitonalDoesNotImplySubstitutable is the conflation byb-js5.2
// names. Bitonal is "1 bit per component, OR an image mask", so a caller
// selecting bilevel candidates for a JBIG2 substitution by Bitonal alone picks
// exactly the images the seam rejects. The two fields answer different
// questions and both have to be read.
func TestImageRefBitonalDoesNotImplySubstitutable(t *testing.T) {
	in := corpus.ScanImageMask()
	pages, err := Inspect(bytes.NewReader(in))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("fixture is %d pages with %d images on page 1; want 1 and 1",
			len(pages), len(pages[0].Images))
	}
	ref := pages[0].Images[0]
	if !ref.Bitonal {
		t.Fatal("the fixture's /ImageMask stencil does not report Bitonal, so it " +
			"does not exercise the conflation")
	}
	if ref.Substitutable {
		t.Error("an /ImageMask stencil reports Substitutable; ReplaceImages refuses it")
	}

	var out bytes.Buffer
	err = ReplaceImages(&out, bytes.NewReader(in),
		map[int]EncodedImage{ref.ObjNr: flateGrayImage(t, 8, 8, 3)})
	if err == nil {
		t.Error("substituting an /ImageMask stencil succeeded; the refusal this " +
			"field reports is gone")
	}
}
