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
type PageInfo struct {
	Index     int
	Bounds    image.Rectangle
	Images    []ImageRef
	TextChars int
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
			return nil, err
		}
		out = append(out, *pi)
	}
	return out, nil
}

// inspectPage returns the page's PageInfo alongside the raw walk, which
// ExtractPageRaster needs for classification.
func inspectPage(ctx context.Context, d pdfdoc.Doc, n int) (*PageInfo, *content.Scan, error) {
	p, err := d.Page(n)
	if err != nil {
		return nil, nil, err
	}
	s, err := content.Walk(ctx, p.Content, p.Scope, d)
	if err != nil {
		return nil, nil, fmt.Errorf("byblos: page %d: %w", n, err)
	}
	pi := &PageInfo{
		Index:     n,
		Bounds:    rectOf(p.CropBox),
		TextChars: s.TextChars,
	}
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
	return pi, s, nil
}

func rectOf(r pdfdoc.Rect) image.Rectangle {
	return image.Rect(round(r.LLX), round(r.LLY), round(r.URX), round(r.URY))
}

func boxRect(b content.Box) image.Rectangle {
	return image.Rect(round(b.LLX), round(b.LLY), round(b.URX), round(b.URY))
}

func round(v float64) int { return int(math.Round(v)) }
