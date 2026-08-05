package byblos

import (
	"bytes"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// corpusDoc is used by every test in this package that needs corpus bytes.
// Discarding the ok from corpus.ByName would turn a typo'd name into nil data,
// and a test that expects an error would then pass vacuously.
func corpusDoc(t *testing.T, name string) []byte {
	t.Helper()
	data, ok := corpus.ByName(name)
	if !ok {
		t.Fatalf("corpus document %q not found", name)
	}
	return data
}

func inspect(t *testing.T, name string) []PageInfo {
	t.Helper()
	pages, err := Inspect(bytes.NewReader(corpusDoc(t, name)))
	if err != nil {
		t.Fatalf("Inspect(%q) error = %v", name, err)
	}
	return pages
}

var fullPage = image.Rect(0, 0, corpus.PageWidthPt, corpus.PageHeightPt)

func TestInspectBornDigital(t *testing.T) {
	pages := inspect(t, "born-digital")
	if len(pages) != 1 {
		t.Fatalf("got %d pages; want 1", len(pages))
	}
	p := pages[0]
	if p.Index != 1 {
		t.Errorf("Index = %d; want 1", p.Index)
	}
	if p.Bounds != fullPage {
		t.Errorf("Bounds = %v; want %v", p.Bounds, fullPage)
	}
	if len(p.Images) != 0 {
		t.Errorf("Images = %+v; want none", p.Images)
	}
	if p.TextChars != corpus.BornDigitalTextChars {
		t.Errorf("TextChars = %d; want %d", p.TextChars, corpus.BornDigitalTextChars)
	}
}

func TestInspectSingleImageScan(t *testing.T) {
	pages := inspect(t, "scan")
	if len(pages) != 1 {
		t.Fatalf("got %d pages; want 1", len(pages))
	}
	p := pages[0]
	if p.TextChars != 0 {
		t.Errorf("TextChars = %d; want 0", p.TextChars)
	}
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", p.Images)
	}
	img := p.Images[0]
	if img.Bounds != fullPage {
		t.Errorf("image Bounds = %v; want %v (page-covering)", img.Bounds, fullPage)
	}
	if img.Width != corpus.ScanImageW || img.Height != corpus.ScanImageH {
		t.Errorf("image pixels = %dx%d; want %dx%d",
			img.Width, img.Height, corpus.ScanImageW, corpus.ScanImageH)
	}
	if img.Bitonal {
		t.Error("Bitonal = true; the corpus scan is 8-bit grey")
	}
}

// byb-b1.2: Bounds is the axis-aligned bounding box of the placement, so it
// reports the same page-covering rectangle for a clean scan and for one a
// scanner deskewed by a fraction of a degree. Placement is what carries that
// residual affine out — to PageProvenance, and to anyone mapping raster
// coordinates back onto the page.
func TestInspectReportsThePlacementMatrix(t *testing.T) {
	for _, tc := range []struct {
		doc  string
		want [6]float64
	}{
		{"scan", [6]float64{corpus.PageWidthPt, 0, 0, corpus.PageHeightPt, 0, 0}},
		{"scan-deskewed", corpus.DeskewPlacement},
		{"scan-mirrored", corpus.MirrorPlacement},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			p := inspect(t, tc.doc)[0]
			if len(p.Images) != 1 {
				t.Fatalf("Images = %+v; want exactly one", p.Images)
			}
			if got := p.Images[0].Placement; got != tc.want {
				t.Errorf("Placement = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestInspectTiledReportsBothHalves(t *testing.T) {
	p := inspect(t, "tiled")[0]
	if len(p.Images) != 2 {
		t.Fatalf("Images = %+v; want two", p.Images)
	}
	half := corpus.PageWidthPt / 2
	wantLeft := image.Rect(0, 0, half, corpus.PageHeightPt)
	wantRight := image.Rect(half, 0, corpus.PageWidthPt, corpus.PageHeightPt)
	if p.Images[0].Bounds != wantLeft {
		t.Errorf("left image Bounds = %v; want %v", p.Images[0].Bounds, wantLeft)
	}
	if p.Images[1].Bounds != wantRight {
		t.Errorf("right image Bounds = %v; want %v", p.Images[1].Bounds, wantRight)
	}
	for i, img := range p.Images {
		if img.Width != corpus.TileImageW || img.Height != corpus.TileImageH {
			t.Errorf("tile %d pixels = %dx%d; want %dx%d",
				i, img.Width, img.Height, corpus.TileImageW, corpus.TileImageH)
		}
	}
}

// Both layers of a stacked page are real placements. Inspect reports what the
// page contains; deciding that only the upper one is visible is classification's
// job, not Inspect's.
func TestInspectStackedReportsBothLayers(t *testing.T) {
	p := inspect(t, "stacked")[0]
	if len(p.Images) != 2 {
		t.Fatalf("Images = %+v; want two", p.Images)
	}
	for i, img := range p.Images {
		if img.Bounds != fullPage {
			t.Errorf("layer %d Bounds = %v; want %v (both are page-covering)", i, img.Bounds, fullPage)
		}
	}
}

// The image lives inside a Form XObject, so its placement can only be found by
// composing the form's /Matrix with the page CTM.
func TestInspectSeesThroughAForm(t *testing.T) {
	p := inspect(t, "scan-in-form")[0]
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want one", p.Images)
	}
	if p.Images[0].Bounds != fullPage {
		t.Errorf("image Bounds = %v; want %v", p.Images[0].Bounds, fullPage)
	}
}

// The regression the research demands: a form-borne text overlay is invisible
// to an image count, so TextChars must come from the walk, not from pdfcpu.
func TestInspectCountsTextInsideAForm(t *testing.T) {
	p := inspect(t, "overlay-text")[0]
	if len(p.Images) != 1 {
		t.Errorf("Images = %+v; want one", p.Images)
	}
	if p.TextChars != corpus.OverlayTextChars {
		t.Errorf("TextChars = %d; want %d", p.TextChars, corpus.OverlayTextChars)
	}
}

// TextChars and the divert decision were split apart by byb-b1.1 and must stay
// split. TextChars is a born-digital signal and an invisible OCR layer is still
// text, so it keeps counting; only classify stopped treating it as ink. These
// pages all extract, which the divert tests assert separately.
func TestInspectCountsInvisibleTextAsText(t *testing.T) {
	for _, name := range []string{
		"invisible-text",
		"invisible-text-in-form",
		"invisible-text-form-inherits",
		"invisible-text-bracketed",
	} {
		t.Run(name, func(t *testing.T) {
			p := inspect(t, name)[0]
			if p.TextChars != corpus.InvisibleTextChars {
				t.Errorf("TextChars = %d; want %d", p.TextChars, corpus.InvisibleTextChars)
			}
		})
	}
}

func TestInspectVectorOverlayStillReportsTheImage(t *testing.T) {
	p := inspect(t, "overlay-vector")[0]
	if len(p.Images) != 1 {
		t.Errorf("Images = %+v; want one", p.Images)
	}
	if p.TextChars != 0 {
		t.Errorf("TextChars = %d; want 0", p.TextChars)
	}
}

func TestInspectMultiPage(t *testing.T) {
	pages := inspect(t, "mixed")
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	if pages[0].Index != 1 || pages[1].Index != 2 {
		t.Errorf("indices = %d, %d; want 1, 2", pages[0].Index, pages[1].Index)
	}
	if pages[0].TextChars != corpus.BornDigitalTextChars || len(pages[0].Images) != 0 {
		t.Errorf("page 1 = %+v; want the born-digital page", pages[0])
	}
	if pages[1].TextChars != 0 || len(pages[1].Images) != 1 {
		t.Errorf("page 2 = %+v; want the scan page", pages[1])
	}
}

func TestInspectRotatedPageReportsUnrotatedBounds(t *testing.T) {
	p := inspect(t, "scan-rotated")[0]
	// /Rotate is a display attribute. Content space is unaffected, so Bounds
	// stays the MediaBox and the placement still covers it.
	if p.Bounds != fullPage {
		t.Errorf("Bounds = %v; want %v", p.Bounds, fullPage)
	}
	if len(p.Images) != 1 || p.Images[0].Bounds != fullPage {
		t.Errorf("Images = %+v; want one page-covering placement", p.Images)
	}
}

func TestInspectMalformedReturnsAnError(t *testing.T) {
	if _, err := Inspect(bytes.NewReader(corpusDoc(t, "malformed"))); err == nil {
		t.Fatal("Inspect(malformed): want an error, got nil")
	}
}

// One blank page must not fail the whole document.
//
// byb-uxb ran Inspect against pdfinfo over 200 govdocs1 files and got seven
// disagreements; six were this, and none of the seven was a wrong number —
// every one was Byblos erroring where poppler succeeds. The page reads as
// what it is: a valid box, no images, no text. See byb-cqs.
func TestInspectBlankPageDoesNotFailTheDocument(t *testing.T) {
	pages := inspect(t, "blank-page")
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	if len(pages[0].Images) != 1 || pages[0].Images[0].Bounds != fullPage {
		t.Errorf("page 1 = %+v; want one page-covering placement", pages[0].Images)
	}
	blank := pages[1]
	if blank.Index != 2 {
		t.Errorf("Index = %d; want 2", blank.Index)
	}
	if blank.Bounds != fullPage {
		t.Errorf("blank page Bounds = %v; want %v", blank.Bounds, fullPage)
	}
	if len(blank.Images) != 0 {
		t.Errorf("blank page Images = %+v; want none", blank.Images)
	}
	if blank.TextChars != 0 {
		t.Errorf("blank page TextChars = %d; want 0", blank.TextChars)
	}
}

// A content stream that did not decode is not a blank page, and Inspect must
// still refuse it. Byblos is deliberately less permissive than poppler here —
// poppler renders nothing for the page and moves on — because reporting a
// damaged page as empty is a silent wrong answer, which is the failure mode
// byb-cqs's fix must not introduce while removing a loud one.
func TestInspectCorruptContentStreamIsStillAnError(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"one stream", corpus.CorruptContentStream()},
		{"array of streams", corpus.CorruptContentStreamInArray()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Inspect(bytes.NewReader(tc.data)); err == nil {
				t.Fatal("Inspect(corrupt content stream): want an error, got nil")
			}
		})
	}
}

// Bitonal is the field B2's JBIG2 path selects on, so it needs a document that
// makes it true, not only ones that make it false.
func TestInspectReportsBitonalForOneBitImages(t *testing.T) {
	p := inspect(t, "jbig2")[0]
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want one", p.Images)
	}
	if !p.Images[0].Bitonal {
		t.Error("Bitonal = false; the jbig2 document is /BitsPerComponent 1")
	}
	if p.Images[0].Width != corpus.ScanImageW || p.Images[0].Height != corpus.ScanImageH {
		t.Errorf("image pixels = %dx%d; want %dx%d",
			p.Images[0].Width, p.Images[0].Height, corpus.ScanImageW, corpus.ScanImageH)
	}
}

// The point of the mrc-inset-base document is that its bitonal base is NOT
// page-covering by the containment test, only by area. If it ever drifts to a
// full-page base it stops testing anything the mrc document does not, and the
// MRC guard's field geometry goes uncovered again.
func TestInspectMRCInsetBaseIsShortOfThePageBox(t *testing.T) {
	p := inspect(t, "mrc-inset-base")[0]
	if len(p.Images) != 2 {
		t.Fatalf("Images = %+v; want the bitonal base and its patch", p.Images)
	}
	base, patch := p.Images[0], p.Images[1]
	if !base.Bitonal || patch.Bitonal {
		t.Errorf("Bitonal = %v, %v; want a bitonal base under an 8-bit patch", base.Bitonal, patch.Bitonal)
	}
	if !base.Bounds.In(p.Bounds) || base.Bounds == p.Bounds {
		t.Errorf("base Bounds = %v; want it strictly inside the page box %v", base.Bounds, p.Bounds)
	}
	if !patch.Bounds.In(base.Bounds) || patch.Bounds == base.Bounds {
		t.Errorf("patch Bounds = %v; want it strictly inside the base's %v", patch.Bounds, base.Bounds)
	}
}

// Both pages of dup-raster are page-covering scans, and Inspect must say so for
// each independently.
func TestInspectDupRasterReportsBothPages(t *testing.T) {
	pages := inspect(t, "dup-raster")
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	for _, p := range pages {
		if len(p.Images) != 1 || p.Images[0].Bounds != fullPage {
			t.Errorf("page %d = %+v; want one page-covering placement", p.Index, p.Images)
		}
	}
}

// ImageRef.Filter is byb-dng's split: over a corpus, "bitonal" answers a very
// different question depending on whether the raster is already JBIG2 (nothing
// left to do) or bitonal under some other codec (a re-encode away).
//
// The two documents are chosen so the field cannot be faked by a constant. The
// jbig2 document is bitonal AND JBIG2Decode; the scan document is neither, and
// its FlateDecode is what a hardcoded "JBIG2Decode" would get wrong.
func TestInspectReportsTheDeclaredImageFilter(t *testing.T) {
	for _, tc := range []struct {
		doc, want string
		bitonal   bool
	}{
		{"jbig2", "JBIG2Decode", true},
		{"scan", "FlateDecode", false},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			p := inspect(t, tc.doc)[0]
			if len(p.Images) != 1 {
				t.Fatalf("Images = %+v; want one", p.Images)
			}
			if got := p.Images[0].Filter; got != tc.want {
				t.Errorf("Filter = %q; want %q", got, tc.want)
			}
			if got := p.Images[0].Bitonal; got != tc.bitonal {
				t.Errorf("Bitonal = %v; want %v -- the split this field exists for "+
					"is Bitonal AND Filter together", got, tc.bitonal)
			}
		})
	}
}
