package byblos

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"math"

	_ "image/jpeg"
	_ "image/png"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ErrNotSingleRaster reports a page that is not one page-covering image:
// tiled rasters, vector content, or image-plus-overlay. The wrapped message
// names the specific reason. Callers divert such documents for review; design
// spec section 2 explains why detecting rather than rendering is the whole
// reason this project is tractable.
var ErrNotSingleRaster = errors.New("byblos: page is not a single page-covering raster")

// ErrUnsupportedImageCodec reports a page raster stored in a codec Byblos
// cannot decode: JBIG2, JPEG 2000, or CMYK images pdfcpu re-renders as TIFF.
//
// This error exists because of a specific correctness trap: pdfcpu does not
// error on JBIG2Decode or JPXDecode, it returns the raw opaque bytes. Handing
// those to an image decoder would either fail obscurely or, worse, appear to
// work. Byblos names the case instead.
var ErrUnsupportedImageCodec = errors.New("byblos: page raster uses an image codec byblos cannot decode")

// coverTolerancePt is how far a placement may fall short of the page box and
// still count as page-covering. One point at 300 DPI is about four pixels.
//
// This constant is an engineering choice made against a synthetic corpus. If
// the divert-rate instrumentation shows "not-page-covering" is a common reason
// on real scans, revisit it here before revisiting the design.
const coverTolerancePt = 1.0

// skewTolerance is how far a placement matrix's off-diagonal terms may stray
// from zero before the image is treated as rotated or sheared.
const skewTolerance = 1e-6

// ExtractPageRaster returns the single page-covering raster of the given
// 1-based page.
//
// It returns an error wrapping ErrNotSingleRaster when the page is anything
// else, and ErrUnsupportedImageCodec when the raster's codec is not decodable.
//
// Note on rotation: a page's /Rotate is a display attribute and does not affect
// content space, so a rotated page still extracts cleanly. The returned image
// is the raster as stored; applying /Rotate is the caller's business.
//
// Orientation within content space is a different matter and is not the
// caller's business, because the caller has no way to recover it: a placement
// that rotates, skews or mirrors the raster diverts rather than returning an
// image the caller would have no signal to correct.
func ExtractPageRaster(r io.ReadSeeker, page int) (image.Image, error) {
	countAttempt()

	d, err := pdfdoc.Open(r)
	if err != nil {
		countFailure()
		return nil, err
	}
	p, err := d.Page(page)
	if err != nil {
		countFailure()
		return nil, err
	}
	_, scan, err := inspectPage(d, page)
	if err != nil {
		countFailure()
		return nil, err
	}

	if reason := classify(p.CropBox, scan); reason != "" {
		countDivert(reason)
		return nil, fmt.Errorf("%w: %s", ErrNotSingleRaster, reason)
	}

	id := scan.Images[0].ID
	data, fileType, err := d.RawImage(id)
	if err != nil {
		// pdfcpu declines to render some filters by returning a nil reader;
		// pdfdoc turns that into ErrUnsupportedCodec. That is a divert (the page
		// is understood, its codec is not), never a read failure.
		if errors.Is(err, pdfdoc.ErrUnsupportedCodec) {
			countDivert("unsupported-codec")
			return nil, fmt.Errorf("%w: %v", ErrUnsupportedImageCodec, err)
		}
		countFailure()
		return nil, err
	}
	switch fileType {
	case "jbig2", "jpx":
		// pdfcpu returns these as opaque bytes rather than erroring, so the
		// check has to happen here or the bytes look like a valid image.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedImageCodec, fileType)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// TIFF is what pdfcpu emits for CMYK rasters; golang.org/x/image/tiff
		// support arrives with B3.
		countDivert("unsupported-codec")
		return nil, fmt.Errorf("%w: %s: %v", ErrUnsupportedImageCodec, fileType, err)
	}
	countExtracted()
	return img, nil
}

// classify returns the reason a page cannot be treated as a single
// page-covering raster, or "" when it can.
//
// The order is deliberate: the first matching reason is the one reported, and
// it should be the most informative. A born-digital page has both no image and
// text; "no-image" says more.
//
// The returned strings are the keys of the divert counters, so changing one
// changes an operational metric. Do not rename them casually.
func classify(page pdfdoc.Rect, s *content.Scan) string {
	switch {
	case len(s.Images) == 0:
		return "no-image"
	case s.TextOps > 0:
		return "has-text"
	case len(s.Images) > 1:
		return "multiple-images"
	case s.InlineImgs > 0:
		return "inline-image"
	case s.PaintOps > 0:
		return "vector-paint"
	case s.ShadingOps > 0:
		return "shading"
	case len(s.Unresolved) > 0:
		return "unresolved-xobject"
	}
	m := s.Images[0].CTM
	if math.Abs(m[1]) > skewTolerance || math.Abs(m[2]) > skewTolerance {
		return "rotated-placement"
	}
	// The off-diagonal terms do not pin orientation. A negative scale mirrors
	// the raster without any skew, and Box cannot see it either: UnitSquareBox
	// takes min/max over the four mapped corners, so `612 0 0 -792 0 792` still
	// reports {0 0 612 792} and covers() still says yes. Without this check a
	// mirrored or upside-down page extracts silently as if it were clean.
	// Requiring both scales strictly positive also rules out a degenerate
	// zero-scale placement, leaving a > 0 and d > 0 for everything downstream.
	if m[0] <= 0 || m[3] <= 0 {
		return "flipped-placement"
	}
	if !covers(s.Images[0].Box, page) {
		return "not-page-covering"
	}
	return ""
}

// covers reports whether box contains the page box, within tolerance. An image
// larger than the page is fine: it is simply cropped on display.
func covers(b content.Box, page pdfdoc.Rect) bool {
	return b.LLX <= page.LLX+coverTolerancePt &&
		b.LLY <= page.LLY+coverTolerancePt &&
		b.URX >= page.URX-coverTolerancePt &&
		b.URY >= page.URY-coverTolerancePt
}

// divertClass maps a fine-grained classify reason to the coarse class stored in
// PageProvenance.Diverted.
//
// Two vocabularies exist on purpose. The counters want detail, because their
// whole job is to say *why* the divert rate is what it is. The stored record
// wants only enough to answer "would re-processing help?", which is what
// capabilityRules in upgrade.go matches on — and a record written today has to
// stay meaningful when a later release renames a counter key.
//
// B5 writes the record; this is the single place that decides the mapping. An
// unrecognised reason falls back to the class that makes a renderer a candidate,
// because reporting a wasted re-run is cheaper than hiding a real upgrade —
// the same bias UpgradeCandidates takes for a capability with no rule.
func divertClass(reason string) string {
	if reason == "unsupported-codec" {
		return "unsupported-codec"
	}
	return "not-single-raster"
}

// Replaced by the real counters in stats.go (Task 11).
func countAttempt()             {}
func countExtracted()           {}
func countFailure()             {}
func countDivert(reason string) { _ = reason }
