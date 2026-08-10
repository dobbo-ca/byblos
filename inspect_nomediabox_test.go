package byblos

import (
	"bytes"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// TestInspectDefaultsAMissingMediaBox pins byb-8ly's largest bucket.
//
// ISO 32000-1 7.7.3.3 makes /MediaBox a required inheritable page attribute, so
// a document without one is malformed. Byblos used to refuse the whole file.
// byb-8ly measured what that cost: 9 of 4,840 govdocs1 documents, every one
// PDF 1.0, none using object streams, with the string "MediaBox" absent from
// the file's bytes altogether. Poppler reads all 9 and reports 612x792 for
// them — which is the reader default it applies, not a fact it read out of the
// document.
//
// Byblos now applies the same default and SAYS SO. The saying-so is the point:
// 612x792 is a convention, and a caller deciding whether a raster covers the
// page is entitled to know its page box was supplied rather than read.
func TestInspectDefaultsAMissingMediaBox(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.NoMediaBox()))
	if err != nil {
		t.Fatalf("Inspect refused a document with no /MediaBox: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	// The US Letter box poppler defaults to, in points.
	if got, want := pages[0].Bounds.Dx(), 612; got != want {
		t.Errorf("page width = %d pt, want the %d pt default", got, want)
	}
	if got, want := pages[0].Bounds.Dy(), 792; got != want {
		t.Errorf("page height = %d pt, want the %d pt default", got, want)
	}
	if len(pages[0].Images) != 1 {
		t.Fatalf("got %d images, want the one page-covering raster", len(pages[0].Images))
	}
}

// TestExtractPageRasterAcceptsAMissingMediaBox is the half that matters to the
// consumer. Reading the page is worth nothing if classification then diverts
// it: the whole point of byb-8ly is that these are ordinary scanned pages
// behind a missing required entry.
func TestExtractPageRasterAcceptsAMissingMediaBox(t *testing.T) {
	r, err := ExtractPageRaster(bytes.NewReader(corpus.NoMediaBox()), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster on a defaulted page box: %v", err)
	}
	if !r.CoversPage() {
		t.Errorf("CoversPage() = false; the raster is page-covering AT THE DEFAULT SIZE, "+
			"so a wrong default would show up exactly here (Bounds %v, Page %v)",
			r.Bounds, r.Page)
	}
}

// TestOptimizeStillRefusesAMissingMediaBox pins the limit of this fix, so the
// next person does not read "byblos handles missing MediaBox" and assume the
// write path does too.
//
// Reading defaults the box; WRITING one still fails, because pdfcpu validates
// a page dict on write and requires the entry. That is pdfcpu's rule, not
// byblos's, and byb-8ly is about what byblos refuses to read.
func TestOptimizeStillRefusesAMissingMediaBox(t *testing.T) {
	var out bytes.Buffer
	err := Optimize(&out, bytes.NewReader(corpus.NoMediaBox()), OptimizeOptions{})
	if err == nil {
		t.Fatal("Optimize accepted a page with no /MediaBox; if pdfcpu has gained the " +
			"ability to write one, this test and byb-8ly's scope both need revisiting")
	}
}
