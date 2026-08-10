package byblos

import (
	"bytes"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestPageRasterCarriesTheDeclaredBitonalFlag pins byb-xcx: extractPage holds
// the source XObject's declared depth in d.ImageInfo and used to discard it, so
// a caller holding a PageRaster could not tell a 1-bpc scan from a contone one
// and could not feed DownsampleDeclaredBPC without a second Inspect pass plus a
// correlation from the page's single raster back to the matching ImageRef.
//
// The assertion is on the DECLARED depth, never on the pixels. byb-05w records
// why: an 8-bpc source whose pixels happen to be pure black and white is
// indistinguishable from a genuine 1-bpc source by pixel data alone, and
// Ghostscript keys /MonoImageDownsampleType off the declared depth. A pixel
// sniff here would reintroduce byb-plj.
func TestPageRasterCarriesTheDeclaredBitonalFlag(t *testing.T) {
	// Every extracting corpus document and its DECLARED depth. scan-bilevel is
	// the only bitonal one that reaches ExtractPageRaster; jbig2 is bitonal too
	// but diverts as an undecodable codec, so it never produces a PageRaster.
	want := map[string]bool{"scan-bilevel": true}

	var seenBitonal, seenContone int
	for _, d := range corpus.All() {
		r, err := ExtractPageRaster(bytes.NewReader(d.Data), 1)
		if err != nil {
			continue // diverts and read failures are not this test's subject
		}
		t.Run(d.Name, func(t *testing.T) {
			if got := r.Bitonal; got != want[d.Name] {
				t.Errorf("Bitonal = %v, want %v", got, want[d.Name])
			}
		})
		if want[d.Name] {
			seenBitonal++
		} else {
			seenContone++
		}
	}

	// Without both arms the test passes vacuously: a build that hard-codes
	// false satisfies every contone document, and one that hard-codes true
	// satisfies the bitonal one. Assert the population, not just the verdicts.
	if seenBitonal == 0 {
		t.Fatal("no bitonal document extracted; the fixture is gone and this test proves nothing")
	}
	if seenContone == 0 {
		t.Fatal("no contone document extracted; this test cannot catch a hard-coded true")
	}
	t.Logf("checked %d bitonal and %d contone extracted rasters", seenBitonal, seenContone)
}
