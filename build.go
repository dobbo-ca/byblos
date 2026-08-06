package byblos

// BuildPDF is the missing half of design spec goal G1: Kleio's input is
// sometimes a bare TIFF or a set of page images rather than an existing PDF,
// and nothing else in this package's API can produce one — Inspect,
// ExtractPageRaster, StampTextLayer and Optimize all read an existing PDF.
// This is Byblos' Go replacement for the img2pdf binary on Kleio's list.

import (
	"context"
	"fmt"
	"io"
	"math"

	"github.com/dobbo-ca/byblos/internal/pdfbuild"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// EncodedImage, ColorSpace and DecodeParms are the vocabulary the write seam
// already defines (internal/pdfdoc/write.go). Aliased here because internal/
// packages are unreachable from Kleio, and a parallel type would be a second
// thing to keep in step with ReplaceImage's.
type EncodedImage = pdfdoc.EncodedImage
type ColorSpace = pdfdoc.ColorSpace
type DecodeParms = pdfdoc.DecodeParms

// BuildPage is one page of a document built from images: one encoded raster
// and the page box to paint it on.
//
// WidthPt/HeightPt are the MediaBox in points, PDF default user space (origin
// lower-left, y increasing upward — the same convention as PageInfo.Bounds).
// Both zero means derive the box from the image's pixel dimensions and DPI,
// which is what a scan wants.
//
// DPI is the raster's resolution, used only when the box is not given. Zero
// DPI with no box is an error, not a default: guessing 300 on a 600 DPI plate
// makes a page twice its true size and nothing downstream can tell.
//
// Placement within the box is fit-centered (contain): the image is scaled
// uniformly to fit inside the box and centred there. When the box's aspect
// ratio does not match the image's, PageRaster.CoversPage will report false
// for that page — correct and expected, not a defect.
type BuildPage struct {
	Image             EncodedImage
	WidthPt, HeightPt float64
	DPI               float64
}

// BuildPDF writes a PDF whose page i paints pages[i].Image and nothing else.
//
// It supports FlateDecode, DCTDecode (DeviceGray/DeviceRGB only) and
// JBIG2Decode (carried verbatim); any other filter, or an unsupported
// BPC/colour-space combination for one of those, is rejected rather than
// written as a file no reader can open.
//
// It cannot be cancelled. Use BuildPDFContext when the caller has a deadline.
func BuildPDF(w io.Writer, pages []BuildPage) error {
	return BuildPDFContext(context.Background(), w, pages)
}

// BuildPDFContext is BuildPDF, cancellable at each page boundary (byb-xyn).
//
// CANCELLATION LATENCY: EFFECTIVELY THE WHOLE CALL. The page loop is checked,
// but it only resolves each page's box -- arithmetic -- while the
// pdfbuild.Write that follows is a single uninterruptible pass over every
// page's encoded bytes, and that is where all the time goes. Measured over 120
// pages, the longest stretch between two context checks was 94% of the call.
// The per-page boundary here is real but nearly worthless; budget for the
// whole write. A cancelled call writes nothing to w. See context.go.
func BuildPDFContext(ctx context.Context, w io.Writer, pages []BuildPage) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("byblos: BuildPDF: no pages")
	}
	built := make([]pdfbuild.Page, len(pages))
	for i, p := range pages {
		if err := checkContext(ctx); err != nil {
			return err
		}
		pw, ph, err := pageBox(p)
		if err != nil {
			return fmt.Errorf("byblos: BuildPDF: page %d: %w", i+1, err)
		}
		built[i] = pdfbuild.Page{Image: p.Image, WidthPt: pw, HeightPt: ph}
	}
	if err := pdfbuild.Write(w, built); err != nil {
		return fmt.Errorf("byblos: BuildPDF: %w", err)
	}
	return nil
}

// pageBox resolves p's MediaBox: the explicit WidthPt/HeightPt if given, else
// derived from the image's pixel dimensions and DPI (points = pixels * 72 /
// DPI — not pixels * 25.4, which would produce a page in millimetres).
func pageBox(p BuildPage) (float64, float64, error) {
	w, h := p.WidthPt, p.HeightPt
	if w == 0 && h == 0 {
		if p.DPI <= 0 || math.IsNaN(p.DPI) || math.IsInf(p.DPI, 0) {
			return 0, 0, fmt.Errorf("no page box given and DPI %v is not positive", p.DPI)
		}
		return float64(p.Image.Width) * 72 / p.DPI, float64(p.Image.Height) * 72 / p.DPI, nil
	}
	if math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("page box %gx%g is not a positive finite size", w, h)
	}
	return w, h, nil
}
