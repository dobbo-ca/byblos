package byblos

import (
	"context"
	"fmt"
	"image"
	"io"
	"math"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ImageRef is one painting of an image on a page.
//
// Bounds is where the image is actually VISIBLE, in PDF default user space:
// points, origin lower-left, y increasing upward. image.Rectangle is used
// only as a convenient integer rectangle — do not read it as screen
// coordinates.
//
// Placement is the matrix the image was painted with, in PDF matrix order
// [a b c d e f] (ISO 32000-1 section 8.3.3), mapping the image's unit square
// into user space. Before byb-b1.12, Bounds was exactly that mapped unit
// square's axis-aligned bounding box -- the same rectangle for a clean
// placement and for one a scanner deskewed by a fraction of a degree, with
// the rotation visible only in Placement. Since byb-b1.12, Bounds is that
// same bounding box intersected with any W/W* clip path or form /BBox in
// effect at the Do: a placement clipped to a corner of the page reports the
// corner, not the raster's own oversized extent, while Placement keeps
// describing the full unclipped matrix regardless. A caller deriving a scale
// or DPI from Bounds against the stored raster's pixel dimensions (Width,
// Height) must account for this — a clipped Bounds does not mean the raster
// was stored at a different resolution.
//
// Bounds is never EMPTY for a placement that can paint (byb-62t). A placement
// narrower than a point on an axis — `q .4 0 0 .4 10 10 cm /Im0 Do Q`, which
// poppler renders — is reported as the smallest integer rectangle containing
// it, so it overstates the extent by up to a point per edge rather than
// vanishing. That is a further reason to read a scale off Placement and not off
// Bounds.
//
// Filter is the codec the stored raster declares: "JBIG2Decode",
// "CCITTFaxDecode", "DCTDecode", "FlateDecode", and so on; "" when the stream
// declares no /Filter or declares one this does not recognize as a name. It is
// the codec, not the transport wrapper — a /Filter [/ASCII85Decode
// /JBIG2Decode] chain reports JBIG2Decode. Read it beside Bitonal to triage
// work without decoding anything: Bitonal with Filter "JBIG2Decode" is already
// what Byblos would produce, Bitonal with any other filter needs only a
// re-encode, and not Bitonal needs a binarizer first.
//
// ObjNr identifies the image XObject itself, and is the handle ReplaceImages
// takes. It is per-OBJECT, not per-painting: one XObject can be painted on
// several pages, or twice on one page, and every such ImageRef reports the
// same ObjNr — which is exactly the signal a caller needs to re-encode a
// shared raster once rather than once per page. It is negative for an image
// stream that is a direct object, which ISO 32000-1 section 7.3.8.1 forbids
// and which ReplaceImages therefore refuses; such a stream has no
// cross-reference entry to write a substitution back to.
type ImageRef struct {
	Bounds        image.Rectangle
	Placement     [6]float64
	Width, Height int    // pixel dimensions of the stored raster
	Bitonal       bool   // 1 bit per component, or an image mask
	Filter        string // the stored raster's declared codec; "" when it declares none
	ObjNr         int    // the image XObject's PDF object number
}

// PageInfo describes one page.
//
// Bounds is the page's CropBox, or its MediaBox when it declares no CropBox,
// in the same user-space convention as ImageRef.Bounds.
//
// TextChars counts the bytes shown by the page's text operators, including
// text reached through Form XObjects. It is a born-digital signal, not a text
// extractor: it counts stored code units, not Unicode code points, and it does
// not decode fonts. Byblos never recognizes text (design spec section 3).
//
// Diagnostics holds what byblos had to work around on this page, and is empty
// for the overwhelming majority of pages. A page carrying one was still read.
type PageInfo struct {
	Index       int
	Bounds      image.Rectangle
	Images      []ImageRef
	TextChars   int
	Diagnostics []Diagnostic
}

// Severity says how much of a PageInfo to believe when byblos had to work
// around something on the page.
//
// It is poppler's distinction, taken from its Error.h: errSyntaxWarning is "PDF
// syntax error which can be worked around; output will probably be correct",
// and errSyntaxError is the same sentence ending "output will probably be
// incorrect". Note what BOTH halves say -- a syntax problem is always worked
// around. Neither category removes a page, and neither ends a document.
type Severity uint8

const (
	// SeverityWarning: byblos worked around the problem and the page's numbers
	// are probably right.
	SeverityWarning Severity = iota
	// SeverityError: byblos worked around the problem and the page's numbers
	// are probably WRONG, and wrong LOW. A content stream that stops early
	// paints fewer images and shows less text than the page really holds, so a
	// scanned page can look like an empty born-digital one -- which is the
	// exact classification byblos exists to get right. A caller must not read
	// TextChars or Images off such a page without accounting for this.
	SeverityError
)

func (s Severity) String() string {
	if s == SeverityError {
		return "error"
	}
	return "warning"
}

// Diagnostic is one problem byblos worked around while reading a page.
//
// byb-3jq: byblos used to refuse the WHOLE document for any of these. Measured
// over govdocs1 that cost 176 readable pages to 7 bad ones, 135 of them in one
// document over three pages. Poppler reads all four of those documents and
// reports the problem on stderr rather than withholding the file, which is the
// behaviour this mirrors.
//
// Message is the underlying error's text. Poppler's callback also carries a
// machine-readable byte offset (Goffset pos); byblos's offsets are inside the
// message text for now, because the lexer formats them rather than returning a
// typed error.
type Diagnostic struct {
	Severity Severity
	Message  string
}

// Inspect reports what every page of r contains. It does not render anything.
//
// It cannot be cancelled. Use InspectContext when the caller has a deadline.
func Inspect(r io.ReadSeeker) ([]PageInfo, error) {
	return InspectContext(context.Background(), r)
}

// InspectContext is Inspect, cancellable at each page boundary (byb-xyn).
//
// CANCELLATION LATENCY: MOST OF THE CALL, despite the per-page check, and the
// per-page check is not the reason. pdfdoc.Open runs before the loop and is a
// single uninterruptible pdfcpu parse of the whole document; the walk that
// follows is comparatively cheap. Measured over 120 pages of 300-dpi scans,
// the longest stretch between two context checks was 69% of the call, and all
// of it was the open. So the loop check bounds the WALK by one page, which is
// worth having, but it does not bound the CALL: a caller must still budget for
// a full pdfcpu parse. See context.go.
func InspectContext(ctx context.Context, r io.ReadSeeker) ([]PageInfo, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		return nil, err
	}
	out := make([]PageInfo, 0, d.PageCount())
	for n := 1; n <= d.PageCount(); n++ {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		pi, _, err := inspectPage(ctx, d, n)
		if err != nil {
			// Cancellation is not a defect in the document. Reporting it as a
			// page diagnostic would hand back a document that was never read.
			if cerr := checkContext(ctx); cerr != nil {
				return nil, cerr
			}
			// Everything else is a page byblos worked around. inspectPage
			// returns whatever it did establish, which is nothing at all only
			// when the page dictionary itself would not resolve.
			if pi == nil {
				pi = &PageInfo{Index: n}
			}
			pi.Diagnostics = append(pi.Diagnostics, Diagnostic{
				Severity: SeverityError,
				Message:  err.Error(),
			})
		}
		out = append(out, *pi)
	}
	return out, nil
}

// inspectPage returns the page's PageInfo alongside the raw walk, which
// ExtractPageRaster needs for classification.
//
// On a walk failure it returns BOTH the error and what the walk established
// before it, because a content stream that stops early still describes the part
// of the page it reached -- that is what poppler paints for these pages, and
// what byblos threw away before byb-3jq. Callers that cannot use a partial page
// simply check the error first, which every one of them does; only
// InspectContext looks at the PageInfo as well.
//
// A page whose dictionary will not resolve is different: nothing is known about
// it, not even a page box, so that returns a nil PageInfo.
func inspectPage(ctx context.Context, d pdfdoc.Doc, n int) (*PageInfo, *content.Scan, error) {
	p, err := d.Page(n)
	if err != nil {
		return nil, nil, err
	}
	s, walkErr := content.Walk(ctx, p.Content, p.Scope, d)
	pi := &PageInfo{Index: n, Bounds: rectOf(p.CropBox)}
	if s == nil {
		// Defensive: Walk returns its partial scan even on failure. A nil one
		// would mean a page with no numbers at all rather than a panic.
		s = &content.Scan{}
	}
	pi.TextChars = s.TextChars
	for _, pl := range s.Images {
		ref := ImageRef{Bounds: boxRect(pl.Box), Placement: [6]float64(pl.CTM)}
		if info, ok := d.ImageInfo(pl.ID); ok {
			ref.Width = info.Width
			ref.Height = info.Height
			ref.Bitonal = info.BPC == 1 || info.ImageMask
			ref.Filter = info.Filter
			ref.ObjNr = info.ObjNr
		}
		pi.Images = append(pi.Images, ref)
	}
	if walkErr != nil {
		return pi, s, fmt.Errorf("byblos: page %d: %w", n, walkErr)
	}
	return pi, s, nil
}

// rectOf projects a page box onto the integer rectangle PageInfo.Bounds and
// PageRaster.Page report. It is boxRect's rule applied to the other box, and
// byb-67j is why they now agree: byb-62t stopped the RASTER rectangle collapsing
// and left this half alone, which made the asymmetry visible rather than fixing
// it — a raster covering 100% of a 1.2 x 0.48 pt page reported a non-empty
// Bounds that was not contained in an empty Page.
//
// The rule is re-derived here rather than copied, because a page box is not a
// placement box. Poppler 26.06.0 reports a sub-point /MediaBox VERBATIM through
// pdfinfo and renders every one measured to at least one inked pixel, down to
// 0.1 x 0.1 pt. It applies no minimum of its own — worth stating, since ISO
// 32000-1 names 3 units as the smallest page and poppler DOES substitute a
// 612x792 default for a missing /MediaBox (byb-8ly), so it has opinions here and
// this is not one of them. Collapsing the box says "no page" about a page
// poppler draws, and byb-3jq settled that trade.
//
// A box with no extent still projects to an empty rectangle, and that is
// load-bearing: CoversPage's Page.Empty() guard exists for the zero PageRaster
// every error return hands back, and it keeps working only while a genuinely
// zero box stays empty.
func rectOf(r pdfdoc.Rect) image.Rectangle {
	llx, urx := roundExtent(r.LLX, r.URX)
	lly, ury := roundExtent(r.LLY, r.URY)
	return image.Rect(llx, lly, urx, ury)
}

// boxRect projects a float box onto the integer rectangle a caller reads as
// Bounds. PageRaster.Bounds and ImageRef.Bounds are both this, of the same
// placement box, and they must not disagree.
//
// Rounding each edge to nearest is the presentation answer, and for any box
// wider than a point it is the whole answer. What it must never do is report an
// EMPTY rectangle beside raster bytes: nothing tells that record apart from a
// page carrying no marks, while PageGeometry's raster_box states a real extent
// alongside it (byb-62t). So on an axis whose two edges round together, the
// rectangle widens outward instead, to the smallest integer interval that
// contains the box.
//
// A sub-point extent is content, not a rounding ghost, which is why widening
// rather than refusing is the answer. poppler 26.06.0 renders `q .4 0 0 792 10
// 0 cm /Im0 Do Q` as a 0.4pt-wide full-height stripe and inks 792 pixels of it
// at 72 DPI.
//
// Whether anything is visible AT ALL is a different question, it is a float one,
// and it is not asked here: a box with no extent on an axis still projects to an
// empty rectangle. classify (extract.go) decides visibility with marks() before
// any of this is reached.
func boxRect(b content.Box) image.Rectangle {
	llx, urx := roundExtent(b.LLX, b.URX)
	lly, ury := roundExtent(b.LLY, b.URY)
	return image.Rect(llx, lly, urx, ury)
}

// roundExtent projects one axis of a box, widening outward rather than letting a
// positive extent collapse to nothing. See boxRect.
//
// Both edges move when they collapse, not just the far one: 10.6 and 10.9 round
// to the same 11 and ceil(10.9) is 11 as well, so widening the far edge alone
// would leave the interval empty.
func roundExtent(lo, hi float64) (int, int) {
	if l, h := round(lo), round(hi); l != h || hi <= lo {
		return l, h
	}
	return int(math.Floor(lo)), int(math.Ceil(hi))
}

func round(v float64) int { return int(math.Round(v)) }
