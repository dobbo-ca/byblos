package byblos

import (
	"bytes"
	"image"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
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
