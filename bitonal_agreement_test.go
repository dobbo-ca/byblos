package byblos

import (
	"bytes"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestBitonalAgreesBetweenInspectAndExtract pins byb-05w's obligation from the
// only side byblos can pin it.
//
// The declared depth reaches a caller by two independent routes:
// ImageRef.Bitonal from Inspect, keyed by ObjNr, which is what ReplaceImages
// substitutes on; and PageRaster.Bitonal from ExtractPageRaster, which is where
// the pixels are. A caller that downsamples a page it extracted and then
// substitutes the result by object number crosses from one route to the other,
// and if the two ever disagree it passes the wrong declaredBPC to
// DownsampleDeclaredBPC. Passing 8 for a 1-bpc source is byb-plj: Catmull-Rom
// blends a bilevel scan into grey levels it cannot hold, measured at 99
// distinct levels on a 600x800 text scan.
//
// extract.go computes PageRaster.Bitonal with the same expression inspect.go
// uses for ImageRef.Bitonal, and a comment there says the two must never
// disagree. This is that comment made executable.
func TestBitonalAgreesBetweenInspectAndExtract(t *testing.T) {
	var compared, bitonal int

	for _, d := range corpus.All() {
		raster, err := ExtractPageRaster(bytes.NewReader(d.Data), 1)
		if err != nil {
			continue // diverts and unreadable files are not this test's subject
		}
		pages, err := Inspect(bytes.NewReader(d.Data))
		if err != nil {
			t.Errorf("%s: ExtractPageRaster succeeded but Inspect failed: %v", d.Name, err)
			continue
		}
		if len(pages) == 0 {
			t.Errorf("%s: Inspect reported no pages for a document that extracted", d.Name)
			continue
		}
		// Only a page Inspect lists exactly one image for can be correlated
		// without an ObjNr on PageRaster. That is the gap byb-xcx's notes
		// describe from the other side; where it is ambiguous, this test
		// declines rather than guesses.
		if len(pages[0].Images) != 1 {
			continue
		}
		compared++
		if got, want := raster.Bitonal, pages[0].Images[0].Bitonal; got != want {
			t.Errorf("%s: PageRaster.Bitonal = %v but ImageRef.Bitonal = %v; a caller crossing "+
				"from Inspect to ExtractPageRaster would pass the wrong declaredBPC",
				d.Name, got, want)
		}
		if want := pages[0].Images[0].Bitonal; want {
			bitonal++
		}
	}

	// Both arms, or the agreement is vacuous: with only contone pages, two
	// routes that both hard-code false agree perfectly and prove nothing.
	if compared == 0 {
		t.Fatal("no page was comparable; this test proves nothing")
	}
	if bitonal == 0 {
		t.Fatal("no BITONAL page was comparable; agreement on contone pages alone is vacuous")
	}
	t.Logf("compared %d extracted pages, %d of them bitonal", compared, bitonal)
}
