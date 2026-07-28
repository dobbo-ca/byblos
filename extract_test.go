package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

func TestExtractPageRasterSucceeds(t *testing.T) {
	for _, name := range []string{"scan", "scan-rotated", "scan-in-form"} {
		t.Run(name, func(t *testing.T) {
			data := corpusDoc(t, name)
			img, err := ExtractPageRaster(bytes.NewReader(data), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v", err)
			}
			b := img.Bounds()
			if b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
				t.Errorf("raster = %dx%d; want %dx%d",
					b.Dx(), b.Dy(), corpus.ScanImageW, corpus.ScanImageH)
			}
		})
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
			img, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, name)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v", err)
			}
			r, _, _, _ := img.At(0, 0).RGBA()
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
			img, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, name)), 1)
			if err != nil {
				t.Fatalf("ExtractPageRaster() error = %v; want the page to extract", err)
			}
			if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
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
	got, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan-deskewed")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(scan-deskewed) error = %v", err)
	}
	// scan and scan-deskewed hold the same raster object; only the placement
	// differs, so anything but pixel equality means the raster was rewritten.
	want, err := ExtractPageRaster(bytes.NewReader(corpusDoc(t, "scan")), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster(scan) error = %v", err)
	}
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
		img, err := ExtractPageRaster(bytes.NewReader(data), page)
		if err != nil {
			t.Fatalf("page %d: error = %v; want success", page, err)
		}
		if b := img.Bounds(); b.Dx() != corpus.ScanImageW || b.Dy() != corpus.ScanImageH {
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
		"no-image":            "not-single-raster",
		"has-text":            "not-single-raster",
		"multiple-images":     "not-single-raster",
		"inline-image":        "not-single-raster",
		"vector-paint":        "not-single-raster",
		"shading":             "not-single-raster",
		"unresolved-xobject":  "not-single-raster",
		"rotated-placement":   "not-single-raster",
		"flipped-placement":   "not-single-raster",
		"not-page-covering":   "not-single-raster",
		"transparent-overlay": "not-single-raster",
		"mrc-layers":          "not-single-raster",
		"unsupported-codec":   "unsupported-codec",
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
		{name: "painted path", scan: &contentScan{Images: onePlacement(full), PaintOps: 1}, want: "vector-paint"},
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
		{name: "image covers only half the page", scan: &contentScan{Images: onePlacement(half)}, want: "not-page-covering"},
		{name: "half a point of slack is tolerated", scan: &contentScan{Images: onePlacement(contentBox(0.5, 0.5, 611.5, 791.5))}},

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
