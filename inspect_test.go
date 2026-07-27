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
