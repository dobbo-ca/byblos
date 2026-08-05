package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

func TestExtractPageRasterSucceeds(t *testing.T) {
	for _, name := range []string{"scan", "scan-rotated", "scan-in-form", "background-wash"} {
		t.Run(name, func(t *testing.T) {
			data := corpusDoc(t, name)
			pr, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v", err)
			}
			// Every document here is placed across the whole page box, so the
			// coverage answer is the uninteresting one. scan-natural-dpi is the
			// case where it is not; see the test below.
			if !pr.CoversPage() {
				t.Errorf("CoversPage() = false; placement %v should fill the page box %v", pr.Bounds, pr.Page)
			}
			b := pr.Image.Bounds()
			if b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
				t.Errorf("raster = %dx%d; want %dx%d",
					b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
			}
		})
	}
}

// byb-b1.12's acceptance criterion, verbatim: "A placement clipped by a form
// /BBox or a clip path reports the visible box, and a clipped page-covering
// image no longer reports CoversPage true." scan-clipped-corner is exactly
// that: a page-covering image narrowed by a `re W n` clip to a 100x100pt
// corner. The page still extracts -- a lone raster is never asked to cover
// the page, byb-b1.3 -- but Bounds must be the visible corner, not the
// raster's own oversized placement, and CoversPage must therefore be false.
func TestExtractPageRasterReportsTheClippedBoxNotTheRasterPlacement(t *testing.T) {
	pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-clipped-corner")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster() error = %v; want the page to extract", err)
	}
	want := image.Rect(0, 0, 100, 100)
	if pr.Bounds != want {
		t.Errorf("Bounds = %v; want %v (the clip corner, not the full-page raster placement)", pr.Bounds, want)
	}
	if pr.CoversPage() {
		t.Error("CoversPage() = true; the visible mark is a 100x100pt corner of a 612x792 page, want false")
	}
}

// A clip disjoint from the placement's own extent narrows Box to zero area
// (intersectBox's clamp, internal/content/walk.go), off the page entirely
// here. Extracting would return the full uncropped raster against a
// raster_box outside page_box -- a record whose bytes and geometry disagree
// (B-review.json's second major finding). classify diverts instead, so no
// such record is ever written.
func TestExtractPageRasterDivertsWhenTheClipLeavesNothingVisible(t *testing.T) {
	_, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-clipped-away")), 1)
	if !errors.Is(err, ErrNotSingleRaster) {
		t.Fatalf("ExtractPageRaster() error = %v; want %v (the clip left nothing visible)", err, ErrNotSingleRaster)
	}

	rec, err := RecordExtraction(bytes.NewReader(corpusDoc(t, "scan-clipped-away")))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(rec.Pages) != 1 {
		t.Fatalf("RecordExtraction() pages = %d; want 1", len(rec.Pages))
	}
	if rec.Pages[0].Diverted == "" {
		t.Error("Diverted = \"\"; want the page reported as diverted, not extracted")
	}
	if rec.Pages[0].Geometry != nil {
		t.Error("Geometry != nil; a diverted page should carry no geometry record")
	}
}

// The end-to-end counterpart of TestExtractPageRasterReportsTheClippedBoxNotTheRasterPlacement:
// the crop comes from a Form XObject's own /BBox, read off the real PDF by
// internal/pdfdoc, not from a page-level `re W n` clip. Every other form
// fixture in this corpus declares a page-sized /BBox that narrows nothing, so
// without this test internal/pdfdoc's /BBox read -- the only wire between a
// real PDF and content.XObject.BBox -- could be deleted and nothing would
// notice (B-mutate.json PROBE 12). This also exercises extract.go's ClipBox
// wiring (RecordExtraction) against a real extraction rather than a
// hand-built PageGeometry struct (PROBE 2/3/18).
func TestExtractPageRasterReportsTheFormBBoxCropNotTheRasterPlacement(t *testing.T) {
	data := corpusDoc(t, "scan-cropped-by-form-bbox")
	pr, err := ExtractPageRaster(bytes.NewReader(data), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster() error = %v; want the page to extract", err)
	}
	want := image.Rect(0, 0, 100, 100)
	if pr.Bounds != want {
		t.Errorf("Bounds = %v; want %v (the form's /BBox corner, not the full-page raster placement)", pr.Bounds, want)
	}
	if pr.CoversPage() {
		t.Error("CoversPage() = true; the visible mark is a 100x100pt corner of a 612x792 page, want false")
	}

	rec, err := RecordExtraction(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(rec.Pages) != 1 {
		t.Fatalf("RecordExtraction() pages = %d; want 1", len(rec.Pages))
	}
	geom := rec.Pages[0].Geometry
	if geom == nil {
		t.Fatal("Geometry = nil; want a measured record")
	}
	if geom.ClipBox == nil {
		t.Fatal("ClipBox = nil; want the form /BBox recorded as the clip that narrowed this placement")
	}
	wantClip := [4]float64{0, 0, 100, 100}
	if *geom.ClipBox != wantClip {
		t.Errorf("ClipBox = %v; want %v", *geom.ClipBox, wantClip)
	}
}

// extract.go's ClipBox must be sourced from placement.Clip, the actual clip
// rectangle, not from placement.Box, the already-narrowed RasterBox: on every
// OTHER clipped fixture in this corpus the two happen to come out identical,
// so a wrong-source bug would pass unnoticed (B-mutate.json PROBE 3). This
// fixture's clip narrows the placement on one axis only, so ClipBox and
// RasterBox are provably different rectangles.
func TestExtractPageRasterClipBoxIsTheClipNotTheNarrowedRasterBox(t *testing.T) {
	data := corpusDoc(t, "scan-clip-narrower-than-raster-box")
	rec, err := RecordExtraction(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("RecordExtraction() error = %v", err)
	}
	if len(rec.Pages) != 1 {
		t.Fatalf("RecordExtraction() pages = %d; want 1", len(rec.Pages))
	}
	geom := rec.Pages[0].Geometry
	if geom == nil {
		t.Fatal("Geometry = nil; want a measured record")
	}
	wantRaster := [4]float64{0, 0, 100, 200}
	if geom.RasterBox != wantRaster {
		t.Fatalf("RasterBox = %v; want %v", geom.RasterBox, wantRaster)
	}
	if geom.ClipBox == nil {
		t.Fatal("ClipBox = nil; want the actual clip rectangle recorded")
	}
	wantClip := [4]float64{-50, -50, 200, 200}
	if *geom.ClipBox != wantClip {
		t.Errorf("ClipBox = %v; want %v (the clip itself, not RasterBox %v)", *geom.ClipBox, wantClip, wantRaster)
	}
}

// The zero value must not claim to be the full page. It is what every error
// return hands back, and image.Rectangle.In answers true for an empty receiver,
// so the natural implementation reports a page nobody extracted as covered. A
// caller that checks CoversPage before checking err would read every failure as
// a clean full-page scan.
func TestZeroPageRasterDoesNotCoverThePage(t *testing.T) {
	if (PageRaster{}).CoversPage() {
		t.Error("PageRaster{}.CoversPage() = true; want false")
	}
	// The same trap one step in: a real raster, but the page box never filled.
	half := PageRaster{Bounds: image.Rect(0, 0, 306, 792)}
	if half.CoversPage() {
		t.Error("CoversPage() = true for a zero Page box; want false")
	}
}

// Two page-covering placements at the identical CTM are one visible raster: the
// second. Returning the first would be a silently wrong document, so the
// assertion is on pixels, not on dimensions — both layers are the same size.
func TestExtractPageRasterTakesTheOccludingLayer(t *testing.T) {
	wantGray := corpus.FirstGray(corpus.StackedTopSeed)
	if wantGray == corpus.FirstGray(corpus.StackedBaseSeed) {
		t.Fatal("the two stacked layers share a first pixel; the assertion below would pass on either")
	}
	for _, name := range []string{"stacked", "stacked-in-form"} {
		t.Run(name, func(t *testing.T) {
			pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, name)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v", err)
			}
			r, _, _, _ := pr.Image.At(0, 0).RGBA()
			if got := uint8(r >> 8); got != wantGray {
				t.Errorf("first pixel = %d; want %d (the top layer). %d is the occluded base",
					got, wantGray, corpus.FirstGray(corpus.StackedBaseSeed))
			}
		})
	}
}

// The headline correction of byb-b1.1. Byblos diverted 100% of a real ScanSnap
// iX500 corpus because classify keyed on TextOps, and 98.7% of the pages it
// diverted over a page-covering raster carried nothing but an invisible OCR
// layer. Each document here is one of the three shapes that layer takes.
func TestExtractPageRasterExtractsUnderAnInvisibleTextLayer(t *testing.T) {
	for _, name := range []string{
		"invisible-text",
		"invisible-text-in-form",
		"invisible-text-form-inherits",
		"invisible-text-bracketed",
	} {
		t.Run(name, func(t *testing.T) {
			pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, name)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v; want the page to extract", err)
			}
			if b := pr.Image.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
				t.Errorf("raster = %dx%d; want %dx%d",
					b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
			}
		})
	}
}

// byb-b1.2: 147 of the 159 off-axis placements measured on govdocs1 were
// sub-degree scanner deskew, and projection-variance on four of them confirmed
// the stored raster is the raw skewed scan with the correction in the placement
// matrix. Such a page is one page-covering raster and must extract — and what
// comes back is the raster as stored. Straightening it here would mean
// resampling, and those rasters are bilevel JBIG2: interpolation would destroy
// the lossless promise on exactly the pages Byblos most wants to serve.
func TestExtractPageRasterKeepsADeskewedRasterAsStored(t *testing.T) {
	pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-deskewed")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(scan-deskewed) error = %v", err)
	}
	got := pr.Image
	// scan and scan-deskewed hold the same raster object; only the placement
	// differs, so anything but pixel equality means the raster was rewritten.
	wantPR, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(scan) error = %v", err)
	}
	want := wantPR.Image
	if got.Bounds() != want.Bounds() {
		t.Fatalf("raster bounds = %v; want %v", got.Bounds(), want.Bounds())
	}
	b := want.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			gr, gg, gb, ga := got.At(x, y).RGBA()
			wr, wg, wb, wa := want.At(x, y).RGBA()
			if gr != wr || gg != wg || gb != wb || ga != wa {
				t.Fatalf("pixel (%d,%d) = %v; want %v — the deskewed raster was resampled, not returned as stored",
					x, y, got.At(x, y), want.At(x, y))
			}
		}
	}
}

// byb-b1.3: 132 pages across 17 files place the raster at its own resolution on
// a nominal page box rather than stretching it to fit. ia-DTIC_ADA383635.pdf p40
// is 2384x3321 pixels at 302 DPI, which is 568.37 x 791.76 points on a 612x792
// MediaBox — 91.74% of it, with a 43.6 point blank strip down the right.
//
// Its whole content stream is `/GS1 gs q 568.3708 0 0 791.7616 0 0 /Im40 Do Q`.
// Nothing can mark that strip, so the raster is the page and the page extracts.
// What the caller gets told is the geometry, not a divert.
func TestExtractPageRasterAcceptsANaturalDPIPlacement(t *testing.T) {
	pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-natural-dpi")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(scan-natural-dpi) error = %v; want the page to extract", err)
	}
	if pr.CoversPage() {
		t.Errorf("CoversPage() = true; placement %v does not fill page box %v", pr.Bounds, pr.Page)
	}
	// The gap the caller has to be able to see: 612 - 568 points down the right.
	if got := pr.Page.Dx() - pr.Bounds.Dx(); got < 40 || got > 48 {
		t.Errorf("horizontal shortfall = %d points; the measured page is 43.6", got)
	}
	if got := pr.Page.Dy() - pr.Bounds.Dy(); got != 0 {
		t.Errorf("vertical shortfall = %d points; the measured page is flush top and bottom", got)
	}
	// The raster itself is returned as stored, not padded out to the page box.
	if b := pr.Image.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
		t.Errorf("raster = %dx%d; want %dx%d as stored — Byblos does not pad to the MediaBox",
			b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
	}
}

func TestExtractPageRasterDiverts(t *testing.T) {
	for _, tc := range []struct{ doc, reason string }{
		{"born-digital", "no-image"},
		{"overlay-text", "has-text"},
		{"tiled", "multiple-images"},
		{"overlay-vector", "vector-paint"},
		{"stacked-smask", "transparent-overlay"},
		{"stacked-alpha", "transparent-overlay"},
		{"mrc", "mrc-layers"},
		{"mrc-inset-base", "mrc-layers"},
		// byb-b1.2 settles these two deliberately: a quarter turn and a mirror are
		// exactly correctable, and the affine is now recorded, but the returned
		// image.Image is consumed — OCR, thumbnails, human review — before anyone
		// reads provenance, and a sideways or mirrored page is wrong there in a way
		// a fraction of a degree is not. See ExtractPageRaster's doc comment.
		{"scan-quarter-turn", "rotated-placement"},
		{"scan-mirrored", "flipped-placement"},
	} {
		t.Run(tc.doc, func(t *testing.T) {
			data := corpusDoc(t, tc.doc)
			_, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if !errors.Is(err, ErrNotSingleRaster) {
				t.Fatalf("error = %v; want ErrNotSingleRaster", err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error = %q; want it to name the reason %q", err, tc.reason)
			}
		})
	}
}

// The trap ErrUnsupportedImageCodec exists for: pdfcpu returns a JBIG2 payload
// as opaque bytes with no error, so without this check the bytes would reach an
// image decoder and either fail obscurely or appear to work.
func TestExtractPageRasterRejectsJBIG2(t *testing.T) {
	data := corpusDoc(t, "jbig2")
	_, err := ExtractPageRaster(bytes.NewReader(data), 1)
	if !errors.Is(err, ErrUnsupportedImageCodec) {
		t.Fatalf("error = %v; want ErrUnsupportedImageCodec", err)
	}
	if errors.Is(err, ErrNotSingleRaster) {
		t.Error("a JBIG2 page-covering scan IS a single raster; it must not also report ErrNotSingleRaster")
	}
	// The message must be exactly the guard's, not merely one that mentions the
	// codec. Deleting the `case "jbig2", "jpx"` guard sends the payload on to
	// image.Decode, whose failure branch also wraps ErrUnsupportedImageCodec and
	// also interpolates the fileType — it just appends the decoder's own words
	// ("...: jbig2: image: unknown format"). A Contains("jbig2") assertion is
	// satisfied by both, so it pins nothing.
	if want := ErrUnsupportedImageCodec.Error() + ": jbig2"; err.Error() != want {
		t.Errorf("error = %q; want exactly %q (a longer message means the guard was bypassed)", err, want)
	}
}

// Page 2 of the mixed document is a clean scan even though page 1 is not.
// Classification must be per-page.
func TestExtractPageRasterIsPerPage(t *testing.T) {
	data := corpusDoc(t, "mixed")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); !errors.Is(err, ErrNotSingleRaster) {
		t.Errorf("page 1: error = %v; want ErrNotSingleRaster", err)
	}
	if _, err := ExtractPageRaster(bytes.NewReader(data), 2); err != nil {
		t.Errorf("page 2: error = %v; want success", err)
	}
}

// Extraction must be per-object too, not merely per-page. Both pages of
// dup-raster hold the same raster bytes as two distinct objects, which pdfcpu's
// optimize pass deduplicates.
func TestExtractPageRasterHandlesDeduplicatedRasters(t *testing.T) {
	data := corpusDoc(t, "dup-raster")
	for _, page := range []int{1, 2} {
		pr, err := ExtractPageRaster(bytes.NewReader(data), page)
		if err != nil {
			t.Fatalf("page %d: error = %v; want success", page, err)
		}
		if b := pr.Image.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
			t.Errorf("page %d: raster = %dx%d; want %dx%d",
				page, b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
		}
	}
}

func TestExtractPageRasterOutOfRange(t *testing.T) {
	data := corpusDoc(t, "scan")
	for _, n := range []int{0, 2, -1} {
		if _, err := ExtractPageRaster(bytes.NewReader(data), n); err == nil {
			t.Errorf("ExtractPageRaster(page %d): want an error, got nil", n)
		}
	}
}

func TestExtractPageRasterMalformed(t *testing.T) {
	data := corpusDoc(t, "malformed")
	if _, err := ExtractPageRaster(bytes.NewReader(data), 1); err == nil {
		t.Fatal("ExtractPageRaster(malformed): want an error, got nil")
	}
}

// Every reason classify can return, plus the codec reason, must map to a coarse
// class. PageProvenance.Diverted stores the coarse form and capabilityRules
// matches on it (upgrade.go), so an unmapped reason would silently become an
// upgrade blind spot.
func TestDivertClassCoversEveryReason(t *testing.T) {
	want := map[string]string{
		"no-image":           "not-single-raster",
		"has-text":           "not-single-raster",
		"multiple-images":    "not-single-raster",
		"inline-image":       "not-single-raster",
		"vector-paint":       "not-single-raster",
		"shading":            "not-single-raster",
		"unresolved-xobject": "not-single-raster",
		"rotated-placement":  "not-single-raster",
		"flipped-placement":  "not-single-raster",
		// byb-b1.3 stopped classify emitting this one: a lone raster is the page
		// at any coverage. It stays here because a provenance record written by
		// an earlier release carries it, and divertClass is what reads those.
		"not-page-covering": "not-single-raster",
		// byb-b1.12: a clip (or form /BBox) disjoint from the placement's own
		// extent narrows Box to zero area. Extracting there would return the
		// full raster against a raster_box nobody can trust, so classify
		// diverts instead.
		"clipped-away":        "not-single-raster",
		"transparent-overlay": "not-single-raster",
		"mrc-layers":          "not-single-raster",
		// The coarse legacy class. A record written before byb-z8j, or one
		// where the codec could not be determined (pdfdoc.ErrUnsupportedCodec:
		// pdfcpu returns a nil reader before naming a file type), still carries
		// exactly this string and must still match all three decode-* rules.
		"unsupported-codec": "unsupported-codec",
		// byb-z8j: the fine reason names the codec, and divertClass passes it
		// straight through so each decode-* rule can key on its own class.
		"unsupported-codec-jbig2": "unsupported-codec-jbig2",
		"unsupported-codec-jpx":   "unsupported-codec-jpx",
		// pdfcpu v0.13.0 names this file type "tif", never "tiff"
		// (writeImage.go renderDeviceCMYKToTIFF / renderIndexedCMYKToTIFF both
		// return "tif"), so "unsupported-codec-tif" is the reason extract.go
		// actually emits. divertClass normalizes it to "unsupported-codec-tiff"
		// -- the class name decode-tiff (upgrade.go) keys on -- so the rule
		// name stays readable even though pdfcpu's abbreviation is not.
		"unsupported-codec-tif": "unsupported-codec-tiff",
		// An unrecognised codec suffix is still a codec problem, not a
		// rendering one: falling back to "not-single-raster" here would
		// nominate a renderer for a page whose codec no decoder can help
		// either, exactly what byb-97q ruled out for the coarse class.
		"unsupported-codec-cmyk": "unsupported-codec",
	}
	for reason, class := range want {
		if got := divertClass(reason); got != class {
			t.Errorf("divertClass(%q) = %q; want %q", reason, got, class)
		}
	}
	if got := divertClass("something-new"); got != "not-single-raster" {
		t.Errorf("divertClass(unknown) = %q; want the conservative default", got)
	}
}

// The bug byb-b1.2 records: the old gate compared m[1] and m[2] — placement
// matrix entries, in points — against 1e-6. At the 560-point scale of a real
// page that is an exact-zero test, roughly 1e-7 degrees, so whether a rotation
// was tolerated depended on how large the raster was. An angle does not.
func TestSkewDegreesIsScaleInvariant(t *testing.T) {
	const deg = 0.5
	r := deg * math.Pi / 180
	for _, scale := range []float64{1, 72, 612, 5000} {
		m := content.Matrix{
			scale * math.Cos(r), scale * math.Sin(r),
			-scale * math.Sin(r), scale * math.Cos(r),
			0, 0,
		}
		if got := skewDegrees(m); math.Abs(got-deg) > 1e-9 {
			t.Errorf("skewDegrees at scale %v = %v; want %v", scale, got, deg)
		}
	}
}

// The page byb-b1.2 was measured on. Its whole content stream is
//
//	q
//	560.65283 -0.56462 0.76572 760.3374 14.97417 16.36581 cm
//	/Im0 Do
//	Q
//
// govdocs1/005393.pdf p91: no text, no paint, one image, and a deskew rotation
// two orders of magnitude larger than the old tolerance could see.
func TestSkewDegreesOnTheMeasuredPage(t *testing.T) {
	m := content.Matrix{560.65283, -0.56462, 0.76572, 760.3374, 14.97417, 16.36581}
	got := skewDegrees(m)
	if math.Abs(got-0.0577) > 0.0005 {
		t.Errorf("skewDegrees = %v degrees; the measured page is 0.0577", got)
	}
	if got > maxSkewDeg {
		t.Errorf("skewDegrees = %v exceeds maxSkewDeg %v; the measured page still diverts", got, maxSkewDeg)
	}
}

func TestClassify(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	full := contentBox(0, 0, 612, 792)
	half := contentBox(0, 0, 306, 792)
	rightHalf := contentBox(306, 0, 612, 792)
	patch := contentBox(12, 2, 365, 617)
	speck := contentBox(0, 0, 60, 60) // 0.7% of the page, under the MRC floor
	// The base as Google Books actually places it: short of the page box on
	// every edge, covering 94.8% of it. covers() rejects this at a whole 14.5
	// points of shortfall, which is why the guard measures area instead.
	insetBase := contentBox(14.5, 2, 597.5, 790)

	bitonal := pdfdoc.ImageInfo{BPC: 1}
	grey := pdfdoc.ImageInfo{BPC: 8}

	tests := []struct {
		name string
		scan *contentScan
		// info is the image-dictionary lookup; nil means every image is an
		// ordinary opaque 8-bit raster.
		info    func(int) (pdfdoc.ImageInfo, bool)
		wantIdx int
		want    string
	}{
		{name: "clean scan", scan: &contentScan{Images: onePlacement(full)}},
		{name: "no image at all", scan: &contentScan{}, want: "no-image"},
		{name: "inked text present", scan: &contentScan{Images: onePlacement(full), TextOps: 1, InkedTextOps: 1}, want: "has-text"},
		// The whole point of byb-b1.1: an OCR layer shows text operators and
		// deposits no ink, so it is not a reason to divert.
		{name: "invisible text only", scan: &contentScan{Images: onePlacement(full), TextOps: 4}, want: ""},
		{name: "inline image", scan: &contentScan{Images: onePlacement(full), InlineImgs: 1}, want: "inline-image"},
		// The raster is placed at Index 0, so a paint at Index 1 lands on top of
		// it and is visible content.
		{name: "painted path over the raster", scan: &contentScan{
			Images: onePlacement(full),
			Paints: []content.Paint{{Op: "f", Box: full, Index: 1}},
		}, want: "vector-paint"},
		{name: "shading", scan: &contentScan{Images: onePlacement(full), ShadingOps: 1}, want: "shading"},
		{name: "unresolved name", scan: &contentScan{Images: onePlacement(full), Unresolved: []string{"X"}}, want: "unresolved-xobject"},
		{name: "rotated placement", scan: &contentScan{Images: rotatedPlacement()}, want: "rotated-placement"},
		// byb-b1.2: of 159 off-axis placements measured on govdocs1, 147 were
		// scanner deskew, all but one under a degree, median 0.13. The raster is
		// the raw skewed scan and the placement matrix carries the correction, so
		// these pages are single page-covering rasters and must extract.
		{name: "median scanner deskew", scan: &contentScan{Images: deskewedPlacement(0.13)}},
		{name: "the widest deskew measured", scan: &contentScan{Images: deskewedPlacement(1.09)}},
		{name: "a rotation exactly at the tolerance", scan: &contentScan{Images: deskewedPlacement(maxSkewDeg)}},
		{name: "a rotation past the tolerance", scan: &contentScan{Images: deskewedPlacement(2.5)}, want: "rotated-placement"},
		// A shear is not a rotation: the x axis is square to the page and the y
		// axis is not. Checking only one axis would let it through as clean.
		{name: "sheared placement", scan: &contentScan{Images: placementOf(content.Matrix{612, 0, 80, 792, 0, 0})}, want: "rotated-placement"},
		// A negative scale term mirrors the raster without introducing any skew,
		// so the off-diagonal check cannot see it, and UnitSquareBox reports the
		// same page-covering box for all three. Only the sign of a and d tells
		// these apart from a clean placement.
		{name: "vertically flipped", scan: &contentScan{Images: placementOf(content.Matrix{612, 0, 0, -792, 0, 792})}, want: "flipped-placement"},
		{name: "horizontally flipped", scan: &contentScan{Images: placementOf(content.Matrix{-612, 0, 0, 792, 612, 0})}, want: "flipped-placement"},
		{name: "flipped on both axes", scan: &contentScan{Images: placementOf(content.Matrix{-612, 0, 0, -792, 612, 792})}, want: "flipped-placement"},
		// A degenerate zero-scale CTM (`0 0 0 0 0 0 cm`) collapses Box to a
		// single point with no clip in effect. The zero-area guard used to fire
		// unconditionally and report "clipped-away" for this, blaming a clip
		// that was never there; with no Clip set, classify must fall through
		// to placementReason and report the CTM's own cause.
		{name: "degenerate placement with no clip reports its own cause, not a clip",
			scan: &contentScan{Images: []content.Placement{{
				Name: "Im0", ID: 1,
				CTM: content.Matrix{0, 0, 0, 0, 0, 0},
				Box: contentBox(0, 0, 0, 0),
			}}},
			want: "flipped-placement"},
		// The same zero-area Box, but with Clip set: now a clip really did
		// narrow it away, and the placement's own CTM is unremarkable, so
		// "clipped-away" is the right cause.
		{name: "degenerate placement with a clip reports clipped-away",
			scan: &contentScan{Images: []content.Placement{{
				Name: "Im0", ID: 1,
				CTM:  content.Matrix{612, 0, 0, 792, 0, 0},
				Box:  contentBox(0, 0, 0, 0),
				Clip: &content.Box{LLX: 700, LLY: 800, URX: 700, URY: 800},
			}}},
			want: "clipped-away"},
		// A sub-point-tall clip: 0.4pt of height has positive float area
		// (612*0.4 = 244.8) but rounds to a zero-height Bounds (boxRect rounds
		// 100 and 100.4 to the same 100), which is the same incoherent state --
		// a non-empty raster against an empty raster_box -- the guard exists to
		// prevent. Testing float area alone let this slip through; testing the
		// rounded rectangle catches it.
		{name: "a sub-point clip sliver rounds to an empty Bounds and diverts",
			scan: &contentScan{Images: []content.Placement{{
				Name: "Im0", ID: 1,
				CTM:  content.Matrix{612, 0, 0, 792, 0, 0},
				Box:  contentBox(0, 100, 612, 100.4),
				Clip: &content.Box{LLX: 0, LLY: 100, URX: 612, URY: 100.4},
			}}},
			want: "clipped-away"},
		// A clip WAS in effect and still narrowed nothing: `0 0 612 792 re W n`
		// is a page-sized clip, the commonest clip in real documents, and the
		// zero-area Box here comes from the CTM alone. A bare Clip != nil test
		// passes both cases above and then blames this one on the clip too;
		// clipNarrowed separates them by asking whether Box actually fell below
		// CTM.UnitSquareBox(). Without this case that mutant escapes the suite.
		{name: "a clip that narrowed nothing does not steal the CTM's cause",
			scan: &contentScan{Images: []content.Placement{{
				Name: "Im0", ID: 1,
				CTM:  content.Matrix{0, 0, 0, 0, 0, 0},
				Box:  contentBox(0, 0, 0, 0),
				Clip: &content.Box{LLX: 0, LLY: 0, URX: 612, URY: 792},
			}}},
			want: "flipped-placement"},
		// byb-b1.3. A lone raster is the page whatever fraction of the box it
		// occupies: every other arm has already established that nothing in the
		// stream can mark the part it leaves bare. The measured shape is a
		// natural-DPI placement covering 91.74%; half a page is the same rule
		// pushed well past anything measured, and it still holds, because the
		// argument is about what can put ink there and not about how much is
		// left. PageRaster.CoversPage is where the caller learns the difference.
		{name: "a natural-DPI placement 43.6 points short still extracts",
			scan: &contentScan{Images: onePlacement(contentBox(0, 0, 568.3708, 791.7616))}},
		{name: "a lone raster over half the page is still the page",
			scan: &contentScan{Images: onePlacement(half)}},
		{name: "half a point of slack is tolerated", scan: &contentScan{Images: onePlacement(contentBox(0.5, 0.5, 611.5, 791.5))}},
		// A stack is a different question: an under-layer can reach past the top
		// one, so a layered page still has to be covered.
		{name: "a stacked pair that does not cover the page",
			scan:    &contentScan{Images: layers(placement(1, half, true), placement(2, half, true))},
			want:    "multiple-images",
			wantIdx: 0},

		// The measured Internet Archive shape.
		{
			name:    "stacked pair takes the top",
			scan:    &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			wantIdx: 1,
		},
		{
			name:    "three stacked layers take the last",
			scan:    &contentScan{Images: layers(placement(1, full, true), placement(2, full, true), placement(3, full, true))},
			wantIdx: 2,
		},
		// Genuine tiling, which the relaxation must not swallow.
		{
			name: "side-by-side rasters divert",
			scan: &contentScan{Images: layers(placement(1, half, true), placement(2, rightHalf, true))},
			want: "multiple-images",
		},
		{
			name: "a base the top does not contain diverts",
			scan: &contentScan{Images: layers(placement(1, contentBox(-20, -20, 632, 812), true), placement(2, full, true))},
			want: "multiple-images",
		},
		{
			name: "a stamp painted over the cover diverts",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, speck, true))},
			want: "multiple-images",
		},
		// Transparency: the occlusion argument needs the top layer to be opaque.
		{
			name: "a top painted under transparency cannot be assumed to occlude",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, false))},
			want: "transparent-overlay",
		},
		{
			name: "a top with an /SMask diverts",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: grey, 2: {BPC: 8, SMask: true}}),
			want: "transparent-overlay",
		},
		{
			name: "a top with a /Mask diverts",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: grey, 2: {BPC: 8, Mask: true}}),
			want: "transparent-overlay",
		},
		{
			// A stencil mask paints the fill colour through the mask, so what is
			// under it shows through the unset bits.
			name: "an image-mask top diverts",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: grey, 2: {ImageMask: true}}),
			want: "transparent-overlay",
		},
		{
			name: "an unreadable image dictionary is not assumed opaque",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			info: facts(nil),
			want: "transparent-overlay",
		},
		// The MRC guard. Google Books emits a bitonal page-covering base plus a
		// smaller non-bitonal patch, and on 153 measured pages the base is blank.
		{
			name: "a bitonal base with a non-bitonal patch diverts",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, patch, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: bitonal, 2: grey}),
			want: "mrc-layers",
		},
		{
			// The guard runs first on purpose: this page would otherwise reduce
			// to its top layer, and the base could be the only thing carrying
			// content.
			name: "the guard beats the take-the-top rule",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, full, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: bitonal, 2: grey}),
			want: "mrc-layers",
		},
		{
			// The measured geometry, not an idealised one. On every one of the
			// 153 pages the base is placed at its own resolution and falls short
			// of the page box, so a base test built on covers() never fires and
			// the guard is dead in the field it was written for.
			name: "a base placed short of the page box is still an MRC base",
			scan: &contentScan{Images: layers(placement(1, insetBase, true), placement(2, patch, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: bitonal, 2: grey}),
			want: "mrc-layers",
		},
		{
			name: "a bitonal page-covering raster alone is not an MRC page",
			scan: &contentScan{Images: onePlacement(full)},
			info: facts(map[int]pdfdoc.ImageInfo{1: bitonal}),
		},
		{
			name: "a non-bitonal speck is too small to be an MRC patch",
			scan: &contentScan{Images: layers(placement(1, full, true), placement(2, speck, true))},
			info: facts(map[int]pdfdoc.ImageInfo{1: bitonal, 2: grey}),
			want: "multiple-images",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := tc.info
			if info == nil {
				info = plainFacts
			}
			idx, got := classify(page, tc.scan, info)
			if got != tc.want {
				t.Fatalf("classify() reason = %q; want %q", got, tc.want)
			}
			if got == "" && idx != tc.wantIdx {
				t.Errorf("classify() index = %d; want %d", idx, tc.wantIdx)
			}
		})
	}
}

// byb-b1.5 measured 126 scan-shaped pages diverting as vector-paint. The
// trigger is a background wash painted before the raster and then covered by
// it: 117 of the 126 set an explicit background fill colour before the first
// Do, 90 white and 27 the PowerPoint slide colour.
//
// These cases go through the real walker rather than a hand-built Scan, because
// what is under test is the graphics state: painting order, the CTM at the time
// of the paint, and the path's device-space extent. A Scan literal would let a
// test assert an ordering the walker never actually produces.
// TestClassifyMRCPatchFloorBracket pins mrcPatchAreaFrac between the two real
// pages that bracket it, so the constant cannot drift in either direction
// without a measurement.
//
// Both cases are ia-municipaldocume00masgoog.pdf as walked: identical J2i0
// base and Wm watermark, only the JXi0 inset differs. p99's inset is the
// smallest patch that fires mrc-layers anywhere in the pinned sample; p101's
// is the largest that does not. Raising the floor past 0.020194 kills the
// first case; lowering it below 0.017130 kills the second.
func TestClassifyMRCPatchFloorBracket(t *testing.T) {
	page := pdfdocRect(0, 0, 420, 618.72)
	base := contentBox(9.96, 1.56, 410.04, 617.16)      // 0.947768 of the page
	watermark := contentBox(286, 10, 370.0001, 26.5726) // 0.005357, bitonal, on top
	p99Inset := contentBox(70.44, 88.2, 140.52, 163.08) // 0.020194
	p101Inset := contentBox(81.96, 448.2, 148.2, 515.4) // 0.017130
	info := facts(map[int]pdfdoc.ImageInfo{1: {BPC: 1}, 2: {BPC: 8}, 3: {BPC: 1}})

	tests := []struct {
		name  string
		inset content.Box
		want  string
	}{
		{"p99's 0.020194 inset is an MRC patch", p99Inset, "mrc-layers"},
		{"p101's 0.017130 inset is not", p101Inset, "multiple-images"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			scan := &contentScan{Images: layers(
				placement(1, base, true),
				placement(2, tc.inset, true),
				placement(3, watermark, true),
			)}
			if _, got := classify(page, scan, info); got != tc.want {
				t.Fatalf("classify() reason = %q; want %q", got, tc.want)
			}
		})
	}
}

func TestClassifyPaintOcclusion(t *testing.T) {
	page := pdfdocRect(0, 0, 612, 792)
	const fullPageDo = "q 612 0 0 792 0 0 cm /Im0 Do Q\n"

	for _, tc := range []struct {
		name, src, form, want string
	}{
		// The shape from govdocs1/005697.pdf, with the placement widened to the
		// full page. See backgroundWashContent in the corpus.
		{"background wash then the raster",
			"/Cs6 cs 1 1 1 scn\n0.029999 0.03009 611.94 791.94 re\nf\n0 0 0 scn\n" + fullPageDo,
			"", ""},

		// The overlay-vector corpus document. Same rectangle, same raster: only
		// the order differs, and the order is the whole decision.
		{"stroked border over the raster",
			fullPageDo + "q 0 0 0 RG 2 w 72 72 468 648 re S Q\n",
			"", "vector-paint"},

		// A wash the raster does not cover is visible content, whatever order it
		// was painted in. The coverage rule would also reject this page, but the
		// paint arm is reached first and must not wave it through.
		{"wash wider than the raster it precedes",
			"0 0 612 792 re f\nq 412 0 0 592 100 100 cm /Im0 Do Q\n",
			"", "vector-paint"},

		// Ink spreads half the line width to either side of the path, so a border
		// stroked along the raster's own edge lands outside it. Comparing the
		// path box alone would call this hidden.
		{"a stroke whose ink spreads past the raster edge",
			"20 w 0 0 612 792 re S\n" + fullPageDo,
			"", "vector-paint"},

		// The same stroke, thin enough that its ink stays inside the raster.
		{"a stroke whose ink stays inside the raster",
			"1 w 10 10 592 772 re S\n" + fullPageDo,
			"", ""},

		// W sets the clip from the current path and n ends it without painting.
		// Clipping alone has never counted as paint; this pins that it still does
		// not now that paths are tracked rather than only counted.
		{"clip only, no paint", "0 0 612 792 re W n\n" + fullPageDo, "", ""},

		// A wash inside a Form XObject is invisible to the page's own operators,
		// which is the reason the walker recurses at all. Its CTM and its place in
		// the painting order both have to survive the recursion.
		{"wash inside a form before the raster",
			"q 0.5 0 0 0.5 0 0 cm /Fm0 Do Q\n" + fullPageDo,
			"1 1 1 scn 0 0 1200 1500 re f\n", ""},

		// Same form, painted after the raster.
		{"wash inside a form after the raster",
			fullPageDo + "q 0.5 0 0 0.5 0 0 cm /Fm0 Do Q\n",
			"1 1 1 scn 0 0 1200 1500 re f\n", "vector-paint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := classify(page, walkPage(t, tc.src, tc.form), plainFacts); got != tc.want {
				t.Errorf("classify() = %q; want %q", got, tc.want)
			}
		})
	}
}

// A wash is only invisible while the thing over it is opaque. A stencil
// /ImageMask paints through its 1 bits and leaves the rest clear, so the wash
// beneath one is the page background — and of the 126 pages byb-b1.5 measured,
// 27 fill it in the PowerPoint slide colour rather than white. Same for an
// /SMask, a /Mask, and a lowered /ca in the graphics state.
func TestClassifyPaintUnderATransparentRasterStillDiverts(t *testing.T) {
	const src = "1 1 1 scn 0 0 612 792 re f\nq 612 0 0 792 0 0 cm /Im0 Do Q\n"
	for _, tc := range []struct {
		name string
		info pdfdoc.ImageInfo
	}{
		{"stencil image mask", pdfdoc.ImageInfo{ImageMask: true}},
		{"soft-masked raster", pdfdoc.ImageInfo{BPC: 8, SMask: true}},
		{"colour-keyed raster", pdfdoc.ImageInfo{BPC: 8, Mask: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := walkPage(t, src, "")
			if _, got := classify(pdfdocRect(0, 0, 612, 792), s, facts(map[int]pdfdoc.ImageInfo{1: tc.info})); got != "vector-paint" {
				t.Errorf("classify() = %q; want %q", got, "vector-paint")
			}
		})
	}

	// The graphics-state half of the same question: /GS0 lowers /ca, so the
	// raster is painted at reduced alpha and the wash shows through it.
	s := walkPage(t, "1 1 1 scn 0 0 612 792 re f\nq /GS0 gs 612 0 0 792 0 0 cm /Im0 Do Q\n", "")
	if _, got := classify(pdfdocRect(0, 0, 612, 792), s, plainFacts); got != "vector-paint" {
		t.Errorf("classify() with a non-opaque ExtGState = %q; want %q", got, "vector-paint")
	}
}

// A page-clip that narrows an under-layer's Box makes classify's contains()
// check on that under-layer EASIER, not harder, to satisfy -- narrowing a box
// can only shrink what it might stick out past, never grow it. B-review.json
// flagged this direction as the one this project is supposed to be careful
// about, and reproduced a divert -> extract flip from it. This pins that the
// flip is the CORRECT answer, not a regression the "narrower is always safer"
// framing would suggest byb-b1.12 should have avoided.
//
// The reasoning: before byb-b1.12, an under-layer's Box was its raw,
// unclipped placement -- which can overstate what is actually visible on the
// page. Painting a 700x900 image and then clipping to the 612x792 page means
// nothing outside the page is ever shown, but the pre-b1.12 Box (700x900)
// made contains(top.Box, under.Box) fail anyway, over content nobody can see.
// Post-b1.12, under.Box is the placement's REAL visible extent -- clipped to
// what the page clip actually lets through -- so contains() is testing the
// right question: is everything the under-layer actually shows hidden by the
// opaque top layer? Here it is (both layers reduce to the same clipped
// (0,0)-(612,792) box), so extracting is the accurate answer and the old
// divert was the artifact of an untracked clip, not a safety margin.
func TestExtractPageRasterNarrowerUnderLayerBoxFlipsDivertToExtractCorrectly(t *testing.T) {
	const src = "q 0 0 612 792 re W n\n" +
		"q 700 0 0 900 0 0 cm /Im0 Do Q\n" +
		"q 612 0 0 792 0 0 cm /Im0 Do Q\n" +
		"Q\n"
	s := walkPage(t, src, "")
	if len(s.Images) != 2 {
		t.Fatalf("Images = %d; want 2", len(s.Images))
	}
	wantClipped := contentBox(0, 0, 612, 792)
	if s.Images[0].Box != wantClipped {
		t.Fatalf("under-layer Box = %v; want %v (clipped to the page, not its raw 700x900 placement)", s.Images[0].Box, wantClipped)
	}
	if s.Images[1].Box != wantClipped {
		t.Fatalf("top-layer Box = %v; want %v", s.Images[1].Box, wantClipped)
	}
	idx, got := classify(pdfdocRect(0, 0, 612, 792), s, plainFacts)
	if got != "" {
		t.Fatalf("classify() = %q; want the page to extract now that both layers' real visible extent is the same clipped box", got)
	}
	if idx != 1 {
		t.Errorf("classify() index = %d; want 1 (the top layer)", idx)
	}
}

// The page byb-b1.5 was measured on, at its measured geometry. Its entire
// 132-byte content stream is identical on all 45 pages of govdocs1/005697.pdf:
//
//	/Cs6 cs 1 1 1 scn
//	/GS1 gs
//	0.029999 0.03009 610.5 791.94 re
//	f
//	0 0 0 scn
//	q
//	610.559937 0 0 792.000061 -0.000012 -0.000031 cm
//	/Im1 Do
//	Q
//
// This was byb-b1.5's honesty test, and byb-b1.3 finished the job. When b1.5
// landed the wash stopped diverting the page but the raster is 610.56 points
// wide on a 612 point MediaBox, so covers() rejected it and the page fell into
// b1.3's bucket instead — one of the 59 the re-scoring predicted would.
//
// Both arms are in now, so this page extracts: at its own geometry, with the
// two operators it actually carries. All 45 pages of govdocs1/005697.pdf are
// this stream byte for byte.
func TestClassifyOnTheMeasuredWashPage(t *testing.T) {
	const src = "/Cs6 cs 1 1 1 scn\n" +
		"/GS1 gs\n" +
		"0.029999 0.03009 610.5 791.94 re\n" +
		"f\n" +
		"0 0 0 scn\n" +
		"q\n" +
		"610.559937 0 0 792.000061 -0.000012 -0.000031 cm\n" +
		"/Im0 Do\n" +
		"Q\n"
	_, got := classify(pdfdocRect(0, 0, 612, 792), walkPage(t, src, ""), plainFacts)
	if got == "vector-paint" {
		t.Fatal("classify() = \"vector-paint\"; the wash is painted before the raster and inside its box (byb-b1.5)")
	}
	if got == "not-page-covering" {
		t.Fatal("classify() = \"not-page-covering\"; the raster is 610.56pt on a 612pt box and is still the whole page (byb-b1.3)")
	}
	if got != "" {
		t.Errorf("classify() = %q; want the page to extract", got)
	}
}

// pageEnv is the resource tree the classify tests walk against: /Im0 is an
// image, /Fm0 a form carrying the supplied content at scope 1, /GS1 an opaque
// graphics state and /GS0 a transparent one.
type pageEnv struct{ form string }

func (e pageEnv) XObject(scope int, name string) (content.XObject, bool) {
	switch name {
	case "Im0":
		return content.XObject{Image: true, ID: 1}, true
	case "Fm0":
		if e.form == "" {
			return content.XObject{}, false
		}
		return content.XObject{Content: []byte(e.form), Matrix: content.Identity, Scope: 1}, true
	}
	return content.XObject{}, false
}

func (e pageEnv) ExtGStateOpaque(scope int, name string) bool { return name == "GS1" }

func walkPage(t *testing.T, src, form string) *content.Scan {
	t.Helper()
	s, err := content.Walk([]byte(src), 0, pageEnv{form: form})
	if err != nil {
		t.Fatalf("content.Walk() error = %v", err)
	}
	return s
}

type contentScan = content.Scan

func pdfdocRect(llx, lly, urx, ury float64) pdfdoc.Rect {
	return pdfdoc.Rect{LLX: llx, LLY: lly, URX: urx, URY: ury}
}

func contentBox(llx, lly, urx, ury float64) content.Box {
	return content.Box{LLX: llx, LLY: lly, URX: urx, URY: ury}
}

// placement derives the CTM from the box the way the walker does, so a case
// cannot accidentally describe a geometry the two disagree about.
func placement(id int, b content.Box, opaque bool) content.Placement {
	return content.Placement{
		Name: fmt.Sprintf("Im%d", id), ID: id, Box: b, Opaque: opaque,
		CTM: content.Matrix{b.URX - b.LLX, 0, 0, b.URY - b.LLY, b.LLX, b.LLY},
	}
}

func onePlacement(b content.Box) []content.Placement {
	return layers(placement(1, b, true))
}

// layers spells out that the arguments are in paint order: the last one is on
// top.
func layers(p ...content.Placement) []content.Placement { return p }

// plainFacts reports every image as an ordinary opaque 8-bit greyscale raster,
// which is what the geometry cases are about.
func plainFacts(int) (pdfdoc.ImageInfo, bool) { return pdfdoc.ImageInfo{BPC: 8}, true }

// facts serves one dictionary per image id and reports anything else as
// unknown, which is itself a case worth testing.
func facts(m map[int]pdfdoc.ImageInfo) func(int) (pdfdoc.ImageInfo, bool) {
	return func(id int) (pdfdoc.ImageInfo, bool) {
		info, ok := m[id]
		return info, ok
	}
}

// placementOf builds a placement from m and derives its Box the way the walker
// does. Deriving rather than hardcoding is the point: it shows the Box really is
// page-covering, so classify cannot tell these cases apart from geometry alone.
func placementOf(m content.Matrix) []content.Placement {
	return []content.Placement{{Name: "Im0", ID: 1, CTM: m, Box: m.UnitSquareBox()}}
}

// deskewedPlacement is a page-covering raster rotated by deg degrees, the way a
// deskewing scanner writes one: the pixels are the raw scan and the rotation
// lives in the placement matrix. The raster is the size of the page, so the
// rotated box still covers it at the angles this tests.
func deskewedPlacement(deg float64) []content.Placement {
	r := deg * math.Pi / 180
	return placementOf(content.Matrix{
		612 * math.Cos(r), 612 * math.Sin(r),
		-792 * math.Sin(r), 792 * math.Cos(r),
		0, 0,
	})
}

func rotatedPlacement() []content.Placement {
	// A 90-degree rotation: a and d are zero, b and c are not.
	return []content.Placement{{
		Name: "Im0", ID: 1,
		CTM: content.Matrix{0, 792, -612, 0, 612, 0},
		Box: contentBox(0, 0, 612, 792),
	}}
}

// A page whose raster falls short of the box and carries a stamp in the strip
// extracts, and says what it could not include.
//
// This is byb-b1.11's decision made visible. classify is right to extract: the
// content stream proves nothing marks the strip, and it is the annotation --
// which is not in the content stream at all -- that puts ink there. Poppler
// renders that stamp as black where the un-annotated page is white, so the
// returned raster really is missing something a reader sees. The caller is told
// rather than refused, because the measurement found 6 such pages in 18,610.
func TestExtractPageRasterReportsAStampItCannotInclude(t *testing.T) {
	pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-stamped")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster error = %v; the page must still extract", err)
	}
	if pr.CoversPage() {
		t.Errorf("CoversPage() = true; the natural-DPI raster leaves a strip")
	}
	if pr.DroppedAnnots != 1 {
		t.Errorf("DroppedAnnots = %d; want 1", pr.DroppedAnnots)
	}
}

// The same placement without the annotation reports nothing, so the count
// tracks the stamp and not the geometry.
func TestExtractPageRasterReportsNoAnnotsWhenThereAreNone(t *testing.T) {
	for _, name := range []string{"scan-natural-dpi", "scan", "scan-deskewed"} {
		t.Run(name, func(t *testing.T) {
			pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, name)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster error = %v", err)
			}
			if pr.DroppedAnnots != 0 {
				t.Errorf("DroppedAnnots = %d; want 0", pr.DroppedAnnots)
			}
		})
	}
}

// The decoy in indirect-kids is hidden, zero-area and has no appearance. A
// viewer draws nothing for it, so neither does this count.
func TestExtractPageRasterIgnoresANonPaintingAnnotation(t *testing.T) {
	pr, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "indirect-kids")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster error = %v", err)
	}
	if pr.DroppedAnnots != 0 {
		t.Errorf("DroppedAnnots = %d for a hidden zero-area annotation with no /AP; want 0", pr.DroppedAnnots)
	}
}
