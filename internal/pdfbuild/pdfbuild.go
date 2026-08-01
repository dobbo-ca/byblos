// Package pdfbuild constructs a PDF from already-encoded page images.
//
// It exists for BuildPDF (byb-c3o, design spec goal G1's img2pdf gap): Kleio's
// input is sometimes a bare TIFF or a set of page images, not an existing PDF,
// and nothing upstream of this package can write one. The writer here is
// hand-rolled, the same idiom internal/corpus uses, deliberately NOT built on
// pdfcpu: arch_test.go permits pdfcpu only inside internal/pdfdoc, and this
// package's job — write a fully-formed page tree with pixels already encoded
// — is exactly what internal/corpus's writer already does for test fixtures.
//
// Unlike corpus, which buffers a whole document in memory because it only
// ever builds small fixtures, Write streams straight to its io.Writer: a
// several-hundred-page 300 DPI archive is hundreds of megabytes, and nothing
// here needs to rewind before the xref is written.
package pdfbuild

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// Page is one page to build: an encoded image and the page box, in points,
// to paint it on. The box must already be resolved to a positive, finite
// size — deriving it from DPI is BuildPDF's job (build.go), not this
// package's, because DPI-derivation needs no PDF vocabulary at all.
type Page struct {
	Image             pdfdoc.EncodedImage
	WidthPt, HeightPt float64
}

// Write emits a PDF whose page i paints pages[i].Image, fit-centered on its
// WidthPt x HeightPt box, and nothing else.
//
// Placement is "contain": the image is scaled uniformly so it fits entirely
// inside the box and centred there, never stretched and never cropped. When
// the image's aspect ratio does not match the box, the placed raster falls
// short of the box on two opposite edges — deliberate, because stretching
// would distort a scan and cropping would discard content.
//
// ISO 32000-1 section 8.9.5.2 maps image sample row 0 (the top row) to the
// TOP of the unit square, so the ordinary `w 0 0 h tx ty cm` placement below
// already paints the image top-down. No y-flip belongs here.
func Write(out io.Writer, pages []Page) error {
	if len(pages) == 0 {
		return fmt.Errorf("byblos/pdfbuild: no pages")
	}
	for i, p := range pages {
		if err := validatePage(p); err != nil {
			return fmt.Errorf("byblos/pdfbuild: page %d: %w", i+1, err)
		}
	}

	w := newWriter(out)
	catalog := w.reserve()
	pagesRoot := w.reserve()

	type objs struct{ page, content, image int }
	po := make([]objs, len(pages))
	kids := make([]string, len(pages))
	for i := range pages {
		po[i] = objs{page: w.reserve(), content: w.reserve(), image: w.reserve()}
		kids[i] = fmt.Sprintf("%d 0 R", po[i].page)
	}

	for i, p := range pages {
		img := p.Image
		scale := math.Min(p.WidthPt/float64(img.Width), p.HeightPt/float64(img.Height))
		dw := float64(img.Width) * scale
		dh := float64(img.Height) * scale
		if !inRange(dw) || !inRange(dh) {
			return fmt.Errorf("byblos/pdfbuild: page %d: scaled image size %gx%g is not representable", i+1, dw, dh)
		}
		tx := (p.WidthPt - dw) / 2
		ty := (p.HeightPt - dh) / 2

		content := fmt.Sprintf("q %s 0 0 %s %s %s cm /Im0 Do Q\n",
			formatNum(dw), formatNum(dh), formatNum(tx), formatNum(ty))
		w.fillStream(po[i].content, "", []byte(content))

		dict, err := imageDict(img)
		if err != nil {
			return fmt.Errorf("byblos/pdfbuild: page %d: %w", i+1, err)
		}
		w.fillStream(po[i].image, dict, img.Data)

		w.fill(po[i].page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %s %s]"+
			" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
			pagesRoot, formatNum(p.WidthPt), formatNum(p.HeightPt), po[i].image, po[i].content))
	}

	w.fill(pagesRoot, fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), len(pages)))
	w.fill(catalog, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pagesRoot))

	return w.finish(catalog)
}

// formatNum renders v as a PDF real: fixed notation only (ISO 32000-1 section
// 7.3.3 forbids exponential form, which is what %g/%v can produce), rounded to
// six decimal places so the output is stable and free of float noise.
func formatNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e6)/1e6, 'f', -1, 64)
}

// minCoord and maxCoord bound every length pdfbuild emits into a MediaBox or
// a placement matrix. formatNum rounds to six decimal places, so a value
// below minCoord is indistinguishable from zero once rendered — an
// astronomically large DPI or an extreme image aspect ratio can shrink a
// perfectly finite, positive scaled dimension past that floor and produce a
// singular `cm` matrix. maxCoord keeps the rendered token away from the
// float64 range where 'f' notation degenerates to "+Inf" (a token no reader
// accepts as a PDF number) and stays far below sizes observed to crash real
// readers (pdftoppm aborts allocating memory for a 1e38pt MediaBox).
const (
	minCoord = 1e-3
	maxCoord = 1e6
)

// inRange reports whether v is a finite length pdfbuild can safely render.
func inRange(v float64) bool {
	return !math.IsNaN(v) && v >= minCoord && v <= maxCoord
}

// colorSpaceName returns the /ColorSpace name for cs, or an error for
// anything this package does not support. Indexed and any other space are
// rejected outright: nothing in Byblos produces them for a built page, and
// supporting them here would be untested code.
func colorSpaceName(cs pdfdoc.ColorSpace) (string, error) {
	switch cs.Name {
	case "DeviceGray", "DeviceRGB", "DeviceCMYK":
		return cs.Name, nil
	default:
		return "", fmt.Errorf("colour space %q is not supported", cs.Name)
	}
}

// validatePage rejects anything this writer cannot safely turn into a PDF a
// reader can open: a non-finite or non-positive page box, or an image whose
// filter/BPC/colour-space combination this writer does not know how to
// declare correctly.
//
// The per-filter allowlist exists because emitting a /Filter name alone is
// not enough for a reader to trust the bytes behind it:
//   - CCITTFaxDecode needs /K, /Rows, /BlackIs1 and /EncodedByteAlign, none of
//     which pdfdoc.DecodeParms can express, so it is rejected rather than
//     emitted wrong.
//   - JPXDecode, LZWDecode, RunLengthDecode and the ASCII filters have no
//     Byblos producer; supporting them here would be untested code.
//   - DCTDecode is restricted to DeviceGray/DeviceRGB at 8 bits: byblos has no
//     reader for CMYK JPEG's Adobe-inverted convention either, so writing one
//     would produce a file this library's own read side cannot round-trip.
//   - JBIG2Decode carries 1-bit DeviceGray data verbatim (ISO 32000-1 section
//     7.4.7); any other BPC or colour space could not have come from a real
//     JBIG2 region.
//   - FlateDecode excludes 16-bit-per-component: byblos has no producer for
//     it either, and internal/pdfdoc's reader panics on a 16-bpc DeviceGray
//     image (indexes past the end of the decoded row), so writing one would
//     make a file this library's own read side crashes on, even though other
//     readers accept it.
func validatePage(p Page) error {
	if !inRange(p.WidthPt) || !inRange(p.HeightPt) {
		return fmt.Errorf("page box %gx%g is not in the representable range [%g, %g] points", p.WidthPt, p.HeightPt, minCoord, maxCoord)
	}
	img := p.Image
	if img.Width <= 0 || img.Height <= 0 {
		return fmt.Errorf("image dimensions %dx%d are not positive", img.Width, img.Height)
	}
	if len(img.Data) == 0 {
		return fmt.Errorf("image has no data")
	}
	switch img.Filter {
	case "FlateDecode":
		switch img.BPC {
		case 1, 2, 4, 8:
		default:
			return fmt.Errorf("FlateDecode: bits per component %d is not one of 1, 2, 4, 8", img.BPC)
		}
		if _, err := colorSpaceName(img.ColorSpace); err != nil {
			return err
		}
	case "DCTDecode":
		if img.BPC != 8 {
			return fmt.Errorf("DCTDecode: bits per component must be 8, got %d", img.BPC)
		}
		if img.ColorSpace.Name != "DeviceGray" && img.ColorSpace.Name != "DeviceRGB" {
			return fmt.Errorf("DCTDecode: colour space %q is not supported (only DeviceGray/DeviceRGB)", img.ColorSpace.Name)
		}
	case "JBIG2Decode":
		if img.BPC != 1 {
			return fmt.Errorf("JBIG2Decode: bits per component must be 1, got %d", img.BPC)
		}
		if img.ColorSpace.Name != "DeviceGray" {
			return fmt.Errorf("JBIG2Decode: colour space must be DeviceGray, got %q", img.ColorSpace.Name)
		}
	default:
		return fmt.Errorf("filter %q is not supported", img.Filter)
	}
	return nil
}

// decodeParmsDict renders /DecodeParms, or "" when there is nothing to say.
// Callers must only invoke this for FlateDecode: its keys are the PNG
// predictor parameters (ISO 32000-1 Table 12), which DCTDecode and
// JBIG2Decode do not accept.
func decodeParmsDict(p *pdfdoc.DecodeParms) string {
	if p == nil {
		return ""
	}
	var parts []string
	if p.Predictor != 0 {
		parts = append(parts, fmt.Sprintf("/Predictor %d", p.Predictor))
	}
	if p.Colors != 0 {
		parts = append(parts, fmt.Sprintf("/Colors %d", p.Colors))
	}
	if p.BitsPerComponent != 0 {
		parts = append(parts, fmt.Sprintf("/BitsPerComponent %d", p.BitsPerComponent))
	}
	if p.Columns != 0 {
		parts = append(parts, fmt.Sprintf("/Columns %d", p.Columns))
	}
	if len(parts) == 0 {
		return ""
	}
	return "<< " + strings.Join(parts, " ") + " >>"
}

// imageDict renders the image XObject dictionary contents (without the
// enclosing << >>, which fillStream adds) for img.
func imageDict(img pdfdoc.EncodedImage) (string, error) {
	cs, err := colorSpaceName(img.ColorSpace)
	if err != nil {
		return "", err
	}
	d := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent %d /ColorSpace /%s /Filter /%s",
		img.Width, img.Height, img.BPC, cs, img.Filter)
	// /DecodeParms only has a defined meaning for FlateDecode here (see
	// decodeParmsDict); DCTDecode and JBIG2Decode take none of these keys.
	if img.Filter == "FlateDecode" {
		if parms := decodeParmsDict(img.DecodeParms); parms != "" {
			d += " /DecodeParms " + parms
		}
	}
	return d, nil
}

// --- the minimal streaming PDF writer ---------------------------------------

// writer emits PDF objects directly to an io.Writer, tracking each object's
// byte offset so the xref table can be written last without rewinding.
type writer struct {
	w       io.Writer
	n       int   // bytes written so far
	offsets []int // offsets[i-1] is the byte offset of object i; -1 until filled
	err     error // first write error, sticky
}

func newWriter(out io.Writer) *writer {
	w := &writer{w: out}
	// The binary comment line marks the file as containing binary data, per
	// ISO 32000-1 section 7.5.2.
	w.writeString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	return w
}

func (w *writer) writeString(s string) {
	if w.err != nil {
		return
	}
	n, err := io.WriteString(w.w, s)
	w.n += n
	if err != nil {
		w.err = err
	}
}

func (w *writer) write(b []byte) {
	if w.err != nil {
		return
	}
	n, err := w.w.Write(b)
	w.n += n
	if err != nil {
		w.err = err
	}
}

// reserve allocates an object number to be filled later, so parents can refer
// to children that have not been written yet.
func (w *writer) reserve() int {
	w.offsets = append(w.offsets, -1)
	return len(w.offsets)
}

func (w *writer) fill(n int, body string) {
	w.offsets[n-1] = w.n
	w.writeString(fmt.Sprintf("%d 0 obj\n%s\nendobj\n", n, body))
}

// fillStream writes a stream object whose payload is stored verbatim: nothing
// here compresses or otherwise touches it, because by the time a Page reaches
// this writer its image bytes are already in whatever /Filter names, and
// content streams are small enough that a /Filter buys nothing.
func (w *writer) fillStream(n int, dict string, payload []byte) {
	w.offsets[n-1] = w.n
	sep := ""
	if dict != "" {
		sep = " "
	}
	w.writeString(fmt.Sprintf("%d 0 obj\n<< %s%s/Length %d >>\nstream\n", n, dict, sep, len(payload)))
	w.write(payload)
	w.writeString("\nendstream\nendobj\n")
}

// finish writes the cross-reference table and trailer. Each xref entry is
// exactly 20 bytes, as ISO 32000-1 section 7.5.4 requires.
func (w *writer) finish(root int) error {
	if w.err != nil {
		return w.err
	}
	start := w.n
	w.writeString(fmt.Sprintf("xref\n0 %d\n0000000000 65535 f \n", len(w.offsets)+1))
	for i, off := range w.offsets {
		if off < 0 {
			return fmt.Errorf("byblos/pdfbuild: object %d was reserved but never filled", i+1)
		}
		w.writeString(fmt.Sprintf("%010d 00000 n \n", off))
	}
	w.writeString(fmt.Sprintf("trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(w.offsets)+1, root, start))
	return w.err
}
