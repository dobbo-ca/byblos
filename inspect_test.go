package byblos

import (
	"bytes"
	"context"
	"errors"
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
	if p.Rotate != 90 {
		t.Errorf("Rotate = %d; want 90", p.Rotate)
	}
	// /Rotate is a display attribute. Content space is unaffected, so Bounds
	// stays the MediaBox and the placement still covers it.
	if p.Bounds != fullPage {
		t.Errorf("Bounds = %v; want %v", p.Bounds, fullPage)
	}
	if len(p.Images) != 1 || p.Images[0].Bounds != fullPage {
		t.Errorf("Images = %+v; want one page-covering placement", p.Images)
	}
}

func TestInspectPageRotateNoRotateAnywhereReportsZero(t *testing.T) {
	if got := inspect(t, "scan")[0].Rotate; got != 0 {
		t.Errorf("Rotate = %d; want 0 (no /Rotate anywhere)", got)
	}
}

func TestInspectPageRotateInheritsFromPagesNode(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.RotateInheritance()))
	if err != nil {
		t.Fatalf("Inspect(RotateInheritance) error = %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages; want 2", len(pages))
	}
	if pages[0].Rotate != 90 {
		t.Errorf("page 1 Rotate = %d; want 90 (inherited from the /Pages node, no own /Rotate)", pages[0].Rotate)
	}
	if pages[1].Rotate != 180 {
		t.Errorf("page 2 Rotate = %d; want 180 (its own /Rotate overrides the inherited 90)", pages[1].Rotate)
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

// A content stream that did not decode is not a blank page. Byblos used to
// refuse the whole document over one, because reporting a damaged page as empty
// is a silent wrong answer and byb-cqs's fix must not introduce one while
// removing a loud one.
//
// byb-3jq keeps that intent and puts it where poppler puts it. Poppler's
// Error.h calls BOTH of its syntax categories "PDF syntax error which can be
// worked around", separated only by whether the output is "probably correct" or
// "probably incorrect" — a syntax problem never removes a page. So the page
// survives here too, carrying a SeverityError diagnostic that says its numbers
// are not to be trusted. The wrong answer stops being SILENT, which was the
// concern, rather than stopping being reported.
//
// pdfdoc is unchanged and still refuses the page: see
// TestPageWithACorruptContentStreamIsStillAnError. The tolerance is Inspect's
// alone, because Inspect is the only caller whose job survives a missing page.
func TestInspectRecordsACorruptContentStreamInsteadOfRefusingTheDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"one stream", corpus.CorruptContentStream()},
		{"array of streams", corpus.CorruptContentStreamInArray()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages, err := Inspect(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatalf("Inspect refused a whole document over one page: %v", err)
			}
			if len(pages) != 2 {
				t.Fatalf("got %d pages, want 2", len(pages))
			}
			// Page 1 is the ordinary scan and must come back untouched. This is
			// the whole point: it is what the old behaviour threw away.
			if n := len(pages[0].Diagnostics); n != 0 {
				t.Errorf("page 1 carries %d diagnostics; want none: %+v", n, pages[0].Diagnostics)
			}
			if n := len(pages[0].Images); n != 1 {
				t.Errorf("page 1 has %d images; want the page-covering scan", n)
			}
			d := pages[1].Diagnostics
			if len(d) != 1 {
				t.Fatalf("page 2 carries %d diagnostics; want exactly one: %+v", len(d), d)
			}
			if d[0].Severity != SeverityError {
				t.Errorf("page 2 severity = %v; want SeverityError, which is the half that "+
					"says the page's numbers are probably wrong", d[0].Severity)
			}
			if d[0].Message == "" {
				t.Error("page 2 diagnostic has no message; the reason must survive")
			}
		})
	}
}

// The page still has to be REPORTED, in its right place, so a caller indexing
// by Index or ranging in order does not silently shift every page after the
// damaged one.
func TestInspectKeepsAnUnparseablePageInItsPlace(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.MixedPageTwoUnreadable()))
	if err != nil {
		t.Fatalf("Inspect refused the document: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	for i, p := range pages {
		if p.Index != i+1 {
			t.Errorf("pages[%d].Index = %d; want %d", i, p.Index, i+1)
		}
	}
	if len(pages[1].Diagnostics) == 0 {
		t.Error("page 2 is the unreadable one and carries no diagnostic")
	}
}

// The half of byb-3jq that the diagnostic alone does not prove: a page that
// fails PARTWAY keeps what it read. Poppler paints those bytes -- 182
// characters on 050734 page 8, out of a stream that stops after 1,156 -- and
// byblos threw them away, so a half-readable page was indexed as nothing.
//
// The assertion is an exact character count, not "more than zero", because
// "more than zero" would also pass if the walk kept going past the damage.
func TestInspectKeepsWhatItReadBeforeAPageFailed(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.PageTwoStopsMidStream()))
	if err != nil {
		t.Fatalf("Inspect refused the document: %v", err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	bad := pages[1]
	if len(bad.Diagnostics) != 1 || bad.Diagnostics[0].Severity != SeverityError {
		t.Fatalf("page 2 diagnostics = %+v; want one SeverityError", bad.Diagnostics)
	}
	if bad.TextChars != corpus.TruncatedContentStreamChars {
		t.Errorf("page 2 TextChars = %d; want %d, the two Tj operands that precede the "+
			"unterminated string. Zero means the partial walk was discarded.",
			bad.TextChars, corpus.TruncatedContentStreamChars)
	}
	// The page box survives too: it comes from the page dictionary, which is
	// intact, not from the content stream.
	if bad.Bounds.Dx() != corpus.PageWidthPt || bad.Bounds.Dy() != corpus.PageHeightPt {
		t.Errorf("page 2 Bounds = %v; want the declared %dx%d page box",
			bad.Bounds, corpus.PageWidthPt, corpus.PageHeightPt)
	}
}

// Cancellation is not a defect in the document. Burying it as a per-page
// diagnostic would hand back a document that was never read, with every
// uninspected page reported as damaged.
//
// This has to cancel INSIDE a page walk, which is why it does not lean on
// context_test.go. TestContextVariantsRefuseAnAlreadyCancelledContext trips the
// entry guard, and TestInterruptibleCallsStopAtTheirNextCheck fires at check 2,
// which is the page loop's own guard -- both return before content.Walk is
// reached, so both stay green with the guard in the error branch deleted.
// Measured: that mutation reddens neither.
func TestInspectDoesNotTurnACancelledWalkIntoADiagnostic(t *testing.T) {
	// Far enough in to be inside the first page's walk rather than at the entry
	// guard or the loop's per-page check.
	cc := &cancelAtCheck{Context: context.Background(), after: 8}
	pages, err := InspectContext(cc, bytes.NewReader(corpusDoc(t, "scan")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("InspectContext cancelled at check %d returned err = %v and %d pages; "+
			"want context.Canceled. A cancelled walk was recorded as a page defect.",
			cc.after+1, err, len(pages))
	}
	if cc.checks <= cc.after {
		t.Fatalf("only %d context checks were made, so the cancellation at check %d "+
			"never happened and this test proved nothing", cc.checks, cc.after+1)
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

// TestInspectReportsAnInlineImage is byb-js5.6: a page whose only raster is a
// BI ... EI inline image used to be invisible to Inspect (Images was empty),
// which measured as 1,235 of the pinned sample's 169,376 pages -- not a
// theoretical gap.
func TestInspectReportsAnInlineImage(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.InlineImageScan()))
	if err != nil {
		t.Fatalf("Inspect(InlineImageScan) error = %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages; want 1", len(pages))
	}
	p := pages[0]
	if len(p.Images) != 1 {
		t.Fatalf("Images = %+v; want exactly one", p.Images)
	}
	img := p.Images[0]
	if !img.Inline {
		t.Error("Inline = false; want true for a BI ... EI placement")
	}
	if img.Bounds != fullPage {
		t.Errorf("image Bounds = %v; want %v (page-covering)", img.Bounds, fullPage)
	}
	if img.Substitutable {
		t.Error("Substitutable = true; an inline image has no cross-reference entry to write back to")
	}
	if img.ObjNr != 0 || img.Width != 0 || img.Height != 0 || img.Filter != "" {
		t.Errorf("inline ImageRef carries object-derived fields: %+v; want all zero", img)
	}
}
