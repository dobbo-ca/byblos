package byblos

import (
	"bytes"
	"errors"
	"fmt"
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
		{name: "text present", scan: &contentScan{Images: onePlacement(full), TextOps: 1}, want: "has-text"},
		{name: "inline image", scan: &contentScan{Images: onePlacement(full), InlineImgs: 1}, want: "inline-image"},
		{name: "painted path", scan: &contentScan{Images: onePlacement(full), PaintOps: 1}, want: "vector-paint"},
		{name: "shading", scan: &contentScan{Images: onePlacement(full), ShadingOps: 1}, want: "shading"},
		{name: "unresolved name", scan: &contentScan{Images: onePlacement(full), Unresolved: []string{"X"}}, want: "unresolved-xobject"},
		{name: "rotated placement", scan: &contentScan{Images: rotatedPlacement()}, want: "rotated-placement"},
		// A negative scale term mirrors the raster without introducing any skew,
		// so the off-diagonal check cannot see it, and UnitSquareBox reports the
		// same page-covering box for all three. Only the sign of a and d tells
		// these apart from a clean placement.
		{name: "vertically flipped", scan: &contentScan{Images: flippedPlacement(content.Matrix{612, 0, 0, -792, 0, 792})}, want: "flipped-placement"},
		{name: "horizontally flipped", scan: &contentScan{Images: flippedPlacement(content.Matrix{-612, 0, 0, 792, 612, 0})}, want: "flipped-placement"},
		{name: "flipped on both axes", scan: &contentScan{Images: flippedPlacement(content.Matrix{-612, 0, 0, -792, 612, 792})}, want: "flipped-placement"},
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

// flippedPlacement builds a placement from m and derives its Box the way the
// walker does. Deriving rather than hardcoding is the point: it shows the Box
// really is page-covering, so classify cannot detect the mirror from geometry.
func flippedPlacement(m content.Matrix) []content.Placement {
	return []content.Placement{{Name: "Im0", ID: 1, CTM: m, Box: m.UnitSquareBox()}}
}

func rotatedPlacement() []content.Placement {
	// A 90-degree rotation: a and d are zero, b and c are not.
	return []content.Placement{{
		Name: "Im0", ID: 1,
		CTM: content.Matrix{0, 792, -612, 0, 612, 0},
		Box: contentBox(0, 0, 612, 792),
	}}
}
