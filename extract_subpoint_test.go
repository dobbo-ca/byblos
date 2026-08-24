package byblos

import (
	"bytes"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// TestExtractPageRasterNeverReportsAnEmptyBoundsBesideARaster is byb-62t.
//
// A placement squeezed below a point on an axis used to come back as raster
// bytes against an EMPTY Bounds: `q .4 0 0 .4 10 10 cm /Im0 Do Q` extracted with
// Bounds=(10,10)-(10,10), because boxRect rounded 10 and 10.4 to the same
// integer. No reader of that record can tell it from a page with nothing on it,
// and PageGeometry's raster_box said [10,10,10.4,10.4] at the same time.
//
// The page is not the problem — poppler renders both of these — so the answer is
// to report the rectangle honestly rather than to refuse the page. See boxRect
// (inspect.go) for the projection rule and classify (extract.go) for where
// visibility is decided instead.
func TestExtractPageRasterNeverReportsAnEmptyBoundsBesideARaster(t *testing.T) {
	tests := []struct {
		name string
		cm   [6]float64
		want image.Rectangle
	}{
		// Both axes collapse: 0.4 point square at (10,10).
		{"sub-point on both axes", corpus.SubPointPlacement, image.Rect(10, 10, 11, 11)},
		// Only x collapses. The y extent is the whole page and must be reported
		// exactly as it always was, so a fix that widens every edge instead of
		// only the collapsing one fails here.
		{"sub-point on one axis", corpus.SubPointStripePlacement, image.Rect(10, 0, 11, 792)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ExtractPageRaster(bytes.NewReader(corpus.ScanPlacedAt(tt.cm)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster diverted a page poppler renders: %v", err)
			}
			if r.Image == nil {
				t.Fatal("no image returned; this test is about the record beside the bytes")
			}
			if r.Bounds.Empty() {
				t.Fatalf("Bounds = %v is empty beside a %v raster", r.Bounds, r.Image.Bounds())
			}
			if r.Bounds != tt.want {
				t.Errorf("Bounds = %v, want %v", r.Bounds, tt.want)
			}
		})
	}
}

// TestInspectNeverReportsAnEmptyBoundsForAPlacedImage is the same claim on the
// other consumer of boxRect. ImageRef.Bounds and PageRaster.Bounds are the same
// projection of the same box, and a caller reading Inspect's answer is entitled
// to the same coherence as one reading the raster's.
func TestInspectNeverReportsAnEmptyBoundsForAPlacedImage(t *testing.T) {
	pages, err := Inspect(bytes.NewReader(corpus.ScanPlacedAt(corpus.SubPointPlacement)))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("got %d pages carrying %d images, want 1 and 1", len(pages), len(pages[0].Images))
	}
	if got, want := pages[0].Images[0].Bounds, image.Rect(10, 10, 11, 11); got != want {
		t.Errorf("ImageRef.Bounds = %v, want %v", got, want)
	}
}

// TestBoxRectRoundsToNearestAndWidensOnlyToStayNonEmpty pins the projection
// itself, edge by edge, because the two tests above exercise exactly one shape
// each and the rule has to hold either side of every rounding boundary.
func TestBoxRectRoundsToNearestAndWidensOnlyToStayNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		box  content.Box
		want image.Rectangle
	}{
		// Unchanged behaviour: an ordinary box still rounds to nearest, in both
		// directions, and .5 still goes away from zero as math.Round does.
		{"a page-covering box is unchanged", contentBox(0, 0, 612, 792), image.Rect(0, 0, 612, 792)},
		{"each edge rounds to nearest", contentBox(10.4, 10.6, 600.4, 700.6), image.Rect(10, 11, 600, 701)},
		{"a half point rounds away from zero", contentBox(0.5, 1.5, 611.5, 791.5), image.Rect(1, 2, 612, 792)},

		// The widening, and only where an axis would otherwise collapse.
		{"a sub-point square widens on both axes", contentBox(10, 10, 10.4, 10.4), image.Rect(10, 10, 11, 11)},
		{"a sub-point stripe widens only on x", contentBox(10, 0, 10.4, 792), image.Rect(10, 0, 11, 792)},
		// Both edges round UP to the same integer here, so widening the far edge
		// alone leaves the rectangle empty: 10.6 and 10.9 both round to 11 and
		// ceil(10.9) is 11 too. The near edge has to move.
		{"a sub-point box between two integers widens the near edge too",
			contentBox(10.6, 10.6, 10.9, 10.9), image.Rect(10, 10, 11, 11)},
		// A hair either side of an integer boundary: the widened rectangle is
		// still the smallest one containing the box.
		{"a sub-point box straddling an integer", contentBox(9.9, 9.9, 10.1, 10.1), image.Rect(9, 9, 11, 11)},
		// byb-wtp divergence 2: rounding each edge independently can move the
		// reported width a whole point away from round(hi-lo), the value
		// poppler's page-size arithmetic agrees with. round(0.3) = 0 and
		// round(595.6) = 596 independently, an off-by-one width of 596 where
		// the continuous extent 595.3 rounds to 595.
		{"a wide sub-point offset matches poppler's continuous width",
			contentBox(0.3, 0, 595.6, 792), image.Rect(0, 0, 595, 792)},
		// Edges round to different integers (10 and 11) but the continuous
		// extent (0.4) rounds to 0: round(lo) + round(hi-lo) must not collapse
		// this the same way the l == h branch above does not.
		{"edges round apart but the continuous extent rounds to zero",
			contentBox(10.3, 0, 10.7, 792), image.Rect(10, 0, 11, 792)},

		// A box with NO extent on an axis marks nothing, and stays empty. This is
		// what the clipped-away guard is for and it must survive the widening.
		{"a zero-width box stays empty", contentBox(10, 10, 10, 700), image.Rect(10, 10, 10, 700)},
		{"a zero-height box stays empty", contentBox(10, 10, 600, 10), image.Rect(10, 10, 600, 10)},
		{"a point stays empty", contentBox(10, 10, 10, 10), image.Rect(10, 10, 10, 10)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boxRect(tt.box); got != tt.want {
				t.Errorf("boxRect(%v) = %v, want %v", tt.box, got, tt.want)
			}
		})
	}
}

// TestRectOfNeverCollapsesAPageBoxPopplerRenders is byb-67j, the half byb-62t
// deliberately left alone: boxRect projects the RASTER box and rectOf projects
// the PAGE box, and only the first learned to stop collapsing.
//
// A page box is not a placement box, so the rule was re-derived from poppler
// rather than copied across. Measured against poppler 26.06.0 on hand-written
// one-page PDFs, each painting a black rectangle over its whole MediaBox:
//
//	MediaBox      pdfinfo         72 DPI       300 DPI
//	0.1 x 0.1     0.1 x 0.1 pts   1x1, inked   1x1, inked
//	0.4 x 0.4     0.4 x 0.4 pts   1x1, inked   2x2, inked
//	0.4 x 792     0.4 x 792 pts   1x792        2x3300
//	1.2 x 0.48    1.2 x 0.48 pts  2x1          5x2
//	2.9 x 2.9     2.9 x 2.9 pts   3x3          13x13
//
// Poppler reports a sub-point MediaBox VERBATIM and renders every one of them to
// at least one inked pixel. It applies no minimum, which is worth stating
// because ISO 32000-1 names 3 units as the smallest page size and poppler does
// substitute a default for a MISSING MediaBox (byb-8ly) -- so it has opinions
// here, and this is not one of them.
//
// Collapsing such a page to an empty rectangle therefore says "no page" about a
// page poppler draws, which is the trade byb-3jq settled against.
func TestRectOfNeverCollapsesAPageBoxPopplerRenders(t *testing.T) {
	tests := []struct {
		name string
		box  pdfdoc.Rect
		want image.Rectangle
	}{
		// Unchanged: an ordinary page box still rounds to nearest.
		{"letter is unchanged", pdfdoc.Rect{URX: 612, URY: 792}, image.Rect(0, 0, 612, 792)},
		{"each edge rounds to nearest",
			pdfdoc.Rect{LLX: 10.4, LLY: 10.6, URX: 600.4, URY: 700.6}, image.Rect(10, 11, 600, 701)},

		// byb-67j's own producer: BuildPDF of a 5x2 px image at 300 DPI is a
		// 1.2 x 0.48 pt page, and only the y axis collapses.
		{"a 5x2 px page at 300 DPI widens only on y",
			pdfdoc.Rect{URX: 1.2, URY: 0.48}, image.Rect(0, 0, 1, 1)},
		{"a sub-point page widens on both axes",
			pdfdoc.Rect{URX: 0.4, URY: 0.4}, image.Rect(0, 0, 1, 1)},
		{"the smallest page poppler still inks widens too",
			pdfdoc.Rect{URX: 0.1, URY: 0.1}, image.Rect(0, 0, 1, 1)},
		{"a sub-point page offset from the origin",
			pdfdoc.Rect{LLX: 10.6, LLY: 10.6, URX: 10.9, URY: 10.9}, image.Rect(10, 10, 11, 11)},

		// A box with no extent stays empty, and this is load-bearing rather than
		// incidental: CoversPage's Page.Empty() guard exists for the ZERO
		// PageRaster every error return hands back, and it only keeps working
		// while a genuinely zero box still projects to an empty rectangle.
		{"the zero rect stays empty", pdfdoc.Rect{}, image.Rect(0, 0, 0, 0)},
		{"a zero-height page stays empty",
			pdfdoc.Rect{URX: 612}, image.Rect(0, 0, 612, 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rectOf(tt.box); got != tt.want {
				t.Errorf("rectOf(%v) = %v, want %v", tt.box, got, tt.want)
			}
		})
	}
}

// TestASubPointPageIsCoveredByItsOwnFullPageRaster is the end-to-end claim, and
// it is the asymmetry byb-67j was filed for: byb-62t made ImageRef.Bounds and
// PageRaster.Bounds non-empty on this document while Page stayed empty, so a
// raster covering 100% of its page reported Bounds NOT contained in Page.
//
// The document is byblos's own output. BuildPDF of a 5x2 px image at 300 DPI is
// a 1.2 x 0.48 pt page, and it is the only known producer of one.
func TestASubPointPageIsCoveredByItsOwnFullPageRaster(t *testing.T) {
	const w, h = 5, 2
	doc := buildOrFatal(t, []BuildPage{{Image: EncodedImage{
		Width: w, Height: h, BPC: 8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, make([]byte, w*h)),
	}, DPI: 300}})

	pages, err := Inspect(bytes.NewReader(doc))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("got %d pages, want 1", len(pages))
	}
	if pages[0].Bounds.Empty() {
		t.Errorf("PageInfo.Bounds = %v is empty for a page poppler renders as 5x2 px at 300 DPI",
			pages[0].Bounds)
	}

	r, err := ExtractPageRaster(bytes.NewReader(doc), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	if r.Page.Empty() {
		t.Fatalf("PageRaster.Page = %v is empty beside Bounds = %v", r.Page, r.Bounds)
	}
	if !r.CoversPage() {
		t.Errorf("CoversPage() = false for Bounds = %v against Page = %v; the raster IS the page",
			r.Bounds, r.Page)
	}
}

// TestTheZeroPageRasterStillReportsNoCoverage pins the reason rectOf must leave
// a zero box alone. CoversPage's guard exists so that the zero PageRaster --
// what every error return hands back -- does not answer true, because
// image.Rectangle.In is true for an empty receiver. Widening a page box must not
// reach that value.
func TestTheZeroPageRasterStillReportsNoCoverage(t *testing.T) {
	if (PageRaster{}).CoversPage() {
		t.Error("the zero PageRaster reports itself as a full-page scan")
	}
}
