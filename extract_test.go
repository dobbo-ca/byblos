package byblos

import (
	"bytes"
	"errors"
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

func TestExtractPageRasterDiverts(t *testing.T) {
	for _, tc := range []struct{ doc, reason string }{
		{"born-digital", "no-image"},
		{"overlay-text", "has-text"},
		{"tiled", "multiple-images"},
		{"overlay-vector", "vector-paint"},
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
		"no-image":           "not-single-raster",
		"has-text":           "not-single-raster",
		"multiple-images":    "not-single-raster",
		"inline-image":       "not-single-raster",
		"vector-paint":       "not-single-raster",
		"shading":            "not-single-raster",
		"unresolved-xobject": "not-single-raster",
		"rotated-placement":  "not-single-raster",
		"flipped-placement":  "not-single-raster",
		"not-page-covering":  "not-single-raster",
		"unsupported-codec":  "unsupported-codec",
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

	tests := []struct {
		name string
		scan *contentScan
		want string
	}{
		{"clean scan", &contentScan{Images: onePlacement(full)}, ""},
		{"no image at all", &contentScan{}, "no-image"},
		{"text present", &contentScan{Images: onePlacement(full), TextOps: 1}, "has-text"},
		{"two images", &contentScan{Images: twoPlacements(full)}, "multiple-images"},
		{"inline image", &contentScan{Images: onePlacement(full), InlineImgs: 1}, "inline-image"},
		{"painted path", &contentScan{Images: onePlacement(full), PaintOps: 1}, "vector-paint"},
		{"shading", &contentScan{Images: onePlacement(full), ShadingOps: 1}, "shading"},
		{"unresolved name", &contentScan{Images: onePlacement(full), Unresolved: []string{"X"}}, "unresolved-xobject"},
		{"rotated placement", &contentScan{Images: rotatedPlacement()}, "rotated-placement"},
		// A negative scale term mirrors the raster without introducing any skew,
		// so the off-diagonal check cannot see it, and UnitSquareBox reports the
		// same page-covering box for all three. Only the sign of a and d tells
		// these apart from a clean placement.
		{"vertically flipped", &contentScan{Images: flippedPlacement(content.Matrix{612, 0, 0, -792, 0, 792})}, "flipped-placement"},
		{"horizontally flipped", &contentScan{Images: flippedPlacement(content.Matrix{-612, 0, 0, 792, 612, 0})}, "flipped-placement"},
		{"flipped on both axes", &contentScan{Images: flippedPlacement(content.Matrix{-612, 0, 0, -792, 612, 792})}, "flipped-placement"},
		{"image covers only half the page", &contentScan{Images: onePlacement(contentBox(0, 0, 306, 792))}, "not-page-covering"},
		{"half a point of slack is tolerated", &contentScan{Images: onePlacement(contentBox(0.5, 0.5, 611.5, 791.5))}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(page, tc.scan); got != tc.want {
				t.Errorf("classify() = %q; want %q", got, tc.want)
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

func onePlacement(b content.Box) []content.Placement {
	return []content.Placement{{
		Name: "Im0", ID: 1, Box: b,
		CTM: content.Matrix{b.URX - b.LLX, 0, 0, b.URY - b.LLY, b.LLX, b.LLY},
	}}
}

func twoPlacements(b content.Box) []content.Placement {
	return append(onePlacement(b), onePlacement(b)...)
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
