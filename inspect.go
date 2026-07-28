package byblos

import (
	"fmt"
	"image"
	"io"
	"math"

	"github.com/dobbo-ca/byblos/internal/content"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// ImageRef is one painting of an image on a page.
//
// Bounds is where the image lands, in PDF default user space: points, origin
// lower-left, y increasing upward. image.Rectangle is used only as a convenient
// integer rectangle — do not read it as screen coordinates.
//
// Placement is the matrix the image was painted with, in PDF matrix order
// [a b c d e f] (ISO 32000-1 section 8.3.3), mapping the image's unit square
// into user space. Bounds is its axis-aligned bounding box and reports the same
// rectangle for a clean placement and for one a scanner deskewed by a fraction
// of a degree; this is where that rotation is visible.
type ImageRef struct {
	Bounds        image.Rectangle
	Placement     [6]float64
	Width, Height int  // pixel dimensions of the stored raster
	Bitonal       bool // 1 bit per component, or an image mask
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
func Inspect(r io.ReadSeeker) ([]PageInfo, error) {
	d, err := pdfdoc.Open(r)
	if err != nil {
		return nil, err
	}
	out := make([]PageInfo, 0, d.PageCount())
	for n := 1; n <= d.PageCount(); n++ {
		pi, _, err := inspectPage(d, n)
		if err != nil {
			return nil, err
		}
		out = append(out, *pi)
	}
	return out, nil
}

// inspectPage returns the page's PageInfo alongside the raw walk, which
// ExtractPageRaster needs for classification.
func inspectPage(d pdfdoc.Doc, n int) (*PageInfo, *content.Scan, error) {
	p, err := d.Page(n)
	if err != nil {
		return nil, nil, err
	}
	s, err := content.Walk(p.Content, p.Scope, d)
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
