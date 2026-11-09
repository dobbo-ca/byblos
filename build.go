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

	"github.com/dobbo-ca/byblos/internal/limits"
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
	// DPIY is the vertical resolution; zero means the same as DPI.
	DPIY float64
	// Rotate is the page's /Rotate in degrees: 0 (none written), 90, 180 or
	// 270, anything else refused. It turns the page on display and leaves the
	// MediaBox and the placement as they are, which is what img2pdf writes for
	// an EXIF orientation (see EXIFRotation).
	Rotate int
}

// BuildPDF writes a PDF whose page i paints pages[i].Image and nothing else.
//
// It supports FlateDecode (up to 16 bits per component), DCTDecode
// (DeviceGray, DeviceRGB, DeviceCMYK), JPXDecode (a JP2 file, see EmbedJP2) and
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
	return BuildPDFLimits(ctx, w, pages, Limits{})
}

// BuildPDFLimits is BuildPDFContext under lim. An image over
// lim.MaxEncodedImagePixels (width x height, and each axis) is refused as a
// *LimitError named encoded-image-pixels, before anything is written.
func BuildPDFLimits(ctx context.Context, w io.Writer, pages []BuildPage, lim Limits) error {
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
		built[i] = pdfbuild.Page{Image: p.Image, WidthPt: pw, HeightPt: ph, Rotate: p.Rotate}
	}
	if err := pdfbuild.Write(w, built, lim); err != nil {
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
		dy := p.DPIY
		if dy == 0 {
			dy = p.DPI
		}
		for _, d := range []float64{p.DPI, dy} {
			if d <= 0 || math.IsNaN(d) || math.IsInf(d, 0) {
				return 0, 0, fmt.Errorf("no page box given and DPI %v is not positive", d)
			}
		}
		return float64(p.Image.Width) * 72 / p.DPI, float64(p.Image.Height) * 72 / dy, nil
	}
	if math.IsNaN(w) || math.IsNaN(h) || math.IsInf(w, 0) || math.IsInf(h, 0) || w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("page box %gx%g is not a positive finite size", w, h)
	}
	return w, h, nil
}

// TranscodePNG turns a PNG that EmbedPNG cannot carry (alpha, tRNS, Adam7,
// 16-bit) into an EncodedImage for BuildPDF: Flate colour at the PNG's depth
// with a Flate /SMask for alpha or tRNS, read from r once and a row at a time.
// It also returns the DPI the pHYs chunk declares (0 if none). See
// internal/pdfbuild.TranscodePNG for the rules; in short:
//
// lim's pixel cap (MaxEncodedImagePixels) is checked against the IHDR before
// anything is allocated, then the row buffers are reserved from the memory
// budget. An interlaced file needs half the image at native sample size for
// the even rows (Adam7 passes 1 to 6 hold exactly those): scratch, if not nil,
// is called with that many bytes and supplies them, the caller reserving them
// from its own budget; if nil they come from byblos's budget. The compressed
// output grows under the budget and is released on return; the returned bytes
// are the caller's to account for.
//
// Every chunk's CRC is checked; files the standard library's image/png refuses
// are refused. ctx is checked between rows.
func TranscodePNG(ctx context.Context, r io.Reader, lim Limits, scratch func(n int64) ([]byte, error)) (EncodedImage, float64, error) {
	img, dpi, err := pdfbuild.TranscodePNG(ctx, r, lim, scratch)
	if err != nil {
		return EncodedImage{}, 0, fmt.Errorf("byblos: TranscodePNG: %w", err)
	}
	return img, dpi, nil
}

// BuildWriter writes a PDF one page at a time (BuildPDF over a slice holds
// every page's encoded image at once). AddPage puts a page on the io.Writer as
// it is added and keeps no more than its object numbers; Close writes the page
// tree and the xref. The page being added is the caller's, in memory for the
// call: TranscodePNG's output, say.
type BuildWriter struct {
	b   *pdfbuild.Builder
	lim Limits
}

// NewBuildWriter starts a PDF on w. lim.MaxPages (default 10 000) caps the
// pages, and lim.MaxEncodedImagePixels each image, as for BuildPDFLimits.
func NewBuildWriter(w io.Writer, lim Limits) *BuildWriter {
	return &BuildWriter{b: pdfbuild.NewBuilder(w, lim), lim: lim}
}

// AddPage writes one page, with the same page-box, image and rotation rules as
// BuildPDF. A page it refuses (including on a cancelled ctx or past the page
// cap) writes nothing and leaves the writer usable. ctx is checked on entry;
// the write itself is one pass over the page's bytes and cannot be cancelled.
func (bw *BuildWriter) AddPage(ctx context.Context, p BuildPage) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if err := limits.Check("pages", int64(bw.b.Pages()+1), bw.lim.Pages()); err != nil {
		return fmt.Errorf("byblos: BuildWriter: %w", err)
	}
	pw, ph, err := pageBox(p)
	if err != nil {
		return fmt.Errorf("byblos: BuildWriter: page %d: %w", bw.b.Pages()+1, err)
	}
	if err := bw.b.AddPage(pdfbuild.Page{Image: p.Image, WidthPt: pw, HeightPt: ph, Rotate: p.Rotate}); err != nil {
		return fmt.Errorf("byblos: BuildWriter: %w", err)
	}
	return nil
}

// Close writes the page tree and the xref. A document with no page is an error.
func (bw *BuildWriter) Close() error {
	if err := bw.b.Close(); err != nil {
		return fmt.Errorf("byblos: BuildWriter: %w", err)
	}
	return nil
}
