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

// mrcPatchAreaFrac is how much of the page a non-bitonal placement must cover
// before it counts as an MRC patch rather than a stamp or a logo. Google Books
// patches cover about 84% of the page, so the floor only has to be clear of
// noise; it is not a tuned number.
const mrcPatchAreaFrac = 0.02

// ExtractPageRaster returns the single page-covering raster of the given
// 1-based page.
//
// A page may reach that through more than one placement. Scanner pipelines
// routinely paint two page-covering images at the same matrix, the second
// hiding the first; that page has one visible raster and this returns it. See
// classify for the conditions, which are stricter than they look — a layer that
// is not provably an opaque cover diverts.
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

	idx, reason := classify(p.CropBox, scan, d.ImageInfo)
	if reason != "" {
		countDivert(reason)
		return nil, fmt.Errorf("%w: %s", ErrNotSingleRaster, reason)
	}

	id := scan.Images[idx].ID
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

// classify returns the index of the placement that is the page's raster, or the
// reason the page cannot be treated as one. imageInfo resolves an image's
// dictionary facts by placement ID; pdfdoc.Doc's ImageInfo method is the real
// one.
//
// The order is deliberate: the first matching reason is the one reported, and
// it should be the most informative. A born-digital page has both no image and
// text; "no-image" says more.
//
// The returned strings are the keys of the divert counters, so changing one
// changes an operational metric. Do not rename them casually.
func classify(page pdfdoc.Rect, s *content.Scan, imageInfo func(int) (pdfdoc.ImageInfo, bool)) (int, string) {
	switch {
	case len(s.Images) == 0:
		return 0, "no-image"
	// Only text that deposits ink is a reason to divert. Rendering mode 3 paints
	// no glyphs, and an invisible OCR layer is what nearly every scan pipeline
	// ships; keying on TextOps diverted 100% of a real ScanSnap corpus, and
	// 98.7% of the pages it diverted over a page-covering raster carried no ink
	// at all.
	//
	// Known simplification: this ignores z-order. Text painted BEFORE an opaque
	// page-covering image is hidden by it and contributes nothing either, which
	// was 36 pages of the measurement against 210 for the rendering-mode cases.
	// Those pages divert, which is the safe direction; fixing them means
	// tracking paint order, not pretending operator order does not matter.
	case s.InkedTextOps > 0:
		return 0, "has-text"
	case s.InlineImgs > 0:
		return 0, "inline-image"
	case s.PaintOps > 0:
		return 0, "vector-paint"
	case s.ShadingOps > 0:
		return 0, "shading"
	case len(s.Unresolved) > 0:
		return 0, "unresolved-xobject"
	}
	if reason := mrcLayers(page, s.Images, imageInfo); reason != "" {
		return 0, reason
	}

	// Scan.Images is in paint order, so the last placement is the one on top,
	// and it is the only candidate: an earlier raster is not what the page
	// shows, and a raster painted after the candidate would be lost by
	// returning it.
	top := len(s.Images) - 1
	if reason := placementReason(s.Images[top], page); reason != "" {
		// On a layered page the informative fact is that the layers did not
		// reduce to one raster, not which geometry test the top layer failed.
		// This is also what keeps genuine tiling reported as tiling.
		if top > 0 {
			return 0, "multiple-images"
		}
		return 0, reason
	}
	if top > 0 {
		// 16,241 measured Internet Archive pages paint two page-covering images
		// at the identical CTM, the second hiding the first. That reduces to one
		// raster only while the top layer really is an opaque cover over
		// everything below it.
		if !opaqueCover(s.Images[top], imageInfo) {
			return 0, "transparent-overlay"
		}
		for _, under := range s.Images[:top] {
			if !contains(s.Images[top].Box, under.Box) {
				return 0, "multiple-images"
			}
		}
	}
	return top, ""
}

// placementReason reports why a placement cannot stand as the page's raster, or
// "" when it can.
func placementReason(p content.Placement, page pdfdoc.Rect) string {
	m := p.CTM
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
	if !covers(p.Box, page) {
		return "not-page-covering"
	}
	return ""
}

// opaqueCover reports whether p can be said to hide whatever is painted under
// it. Three of the four ways it cannot are dictionary facts — /ImageMask,
// /SMask, /Mask — and the fourth is the graphics state at the moment of
// painting, which the walk recorded.
//
// An image whose dictionary could not be read is not opaque. Guessing the other
// way is the one error here that returns a wrong page instead of a divert.
func opaqueCover(p content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) bool {
	if !p.Opaque {
		return false
	}
	info, ok := imageInfo(p.ID)
	return ok && !info.ImageMask && !info.SMask && !info.Mask
}

// mrcLayers reports the two-tier MRC shape Google Books emits: a bitonal
// page-covering base plus a smaller non-bitonal patch.
//
// It runs before the take-the-top rule and overrides it. On 153 measured pages
// of one Internet Archive file the bitonal base is BLANK and the patch carries
// every word on the page, and only decoding the pixels tells those apart from
// the pages where the base carries the text. Either layer can be the document,
// so neither is returned.
//
// Compositing the layers is a renderer's job, and byblos-divert established it
// is not worth building for 0.78% of one corpus.
func mrcLayers(page pdfdoc.Rect, imgs []content.Placement, imageInfo func(int) (pdfdoc.ImageInfo, bool)) string {
	pageArea := area(content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
	if pageArea <= 0 {
		return ""
	}
	var base, patch bool
	for i, p := range imgs {
		info, ok := imageInfo(p.ID)
		if !ok {
			continue
		}
		if info.BPC == 1 || info.ImageMask {
			// A base is painted under something. A bitonal layer on top is a
			// stencil over the raster below, which is a transparency question,
			// not this one.
			base = base || (i < len(imgs)-1 && covers(p.Box, page))
			continue
		}
		patch = patch || area(p.Box)/pageArea > mrcPatchAreaFrac
	}
	if base && patch {
		return "mrc-layers"
	}
	return ""
}

// contains reports whether outer covers inner, within tolerance.
func contains(outer, inner content.Box) bool {
	return outer.LLX <= inner.LLX+coverTolerancePt &&
		outer.LLY <= inner.LLY+coverTolerancePt &&
		outer.URX >= inner.URX-coverTolerancePt &&
		outer.URY >= inner.URY-coverTolerancePt
}

// covers reports whether box contains the page box, within tolerance. An image
// larger than the page is fine: it is simply cropped on display.
func covers(b content.Box, page pdfdoc.Rect) bool {
	return contains(b, content.Box{LLX: page.LLX, LLY: page.LLY, URX: page.URX, URY: page.URY})
}

func area(b content.Box) float64 { return (b.URX - b.LLX) * (b.URY - b.LLY) }

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
