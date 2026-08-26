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
	"bytes"
	"compress/zlib"
	"encoding/hex"
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

// maxImagePixels bounds an EncodedImage's Width*Height, restated (not
// imported -- see internal/render's maxRasterPixels, which restates the same
// number for the same reason) from this module's design point:
// internal/jbig2's MaxPagePixels and internal/render's maxRasterPixels are
// both 1<<26 = 67,108,864, arrived at independently as 256 MB of RGBA,
// roughly twice a 600 DPI A4. A census over the pinned sample (5,671
// documents, 169,376 pages) found the largest single decoded font program at
// 1,043,560 B and the largest per-page retained font bytes at 2,821,308 B --
// this module's other stated budget, 16<<20 (16 MiB), sits 16x and ~4.5x
// above those respectively, and 1<<26 pixels is the same order of margin
// applied to image dimensions instead of bytes. It refuses byb-37h's hostile
// inputs (a declared 2147483647x1 or 1x999000000 image) many orders of
// magnitude below the point where validatePage or Write would otherwise
// allocate or loop over them.
const maxImagePixels = 1 << 26

// colorSpaceObject renders cs as the object /ColorSpace takes: a name for a
// device space, or the [/Indexed base hival <lookup>] array of ISO 32000-1
// section 8.6.6.3.
//
// Indexed is here because Byblos produces it: QuantizeIndexed (byb-96p) emits
// exactly this shape, and BuildPDF is the only exported consumer of an
// EncodedImage, so refusing it left that encoder with no route into a PDF at
// all (byb-5jy). img2pdf, the binary BuildPDF replaces, accepts indexed PNGs
// for the same reason. Any other space still has no Byblos producer and is
// rejected rather than emitted as untested output.
//
// The palette checks mirror internal/pdfdoc's ColorSpace.object, which the
// write seam applies to this same struct: a lookup table that disagrees with
// its own declared base and hival is a colour space no reader can resolve.
func colorSpaceObject(cs pdfdoc.ColorSpace) (string, error) {
	if cs.Name == "Indexed" {
		n, ok := deviceComponents(cs.Base)
		if !ok {
			return "", fmt.Errorf("indexed base %q is not a device colour space", cs.Base)
		}
		if cs.HiVal < 0 || cs.HiVal > 255 {
			return "", fmt.Errorf("indexed hival %d is outside 0..255", cs.HiVal)
		}
		if want := (cs.HiVal + 1) * n; len(cs.Lookup) != want {
			return "", fmt.Errorf("indexed lookup is %d bytes, want %d for hival %d over %s",
				len(cs.Lookup), want, cs.HiVal, cs.Base)
		}
		return fmt.Sprintf("[/Indexed /%s %d <%s>]", cs.Base, cs.HiVal, hex.EncodeToString(cs.Lookup)), nil
	}
	if _, ok := deviceComponents(cs.Name); !ok {
		return "", fmt.Errorf("colour space %q is not supported", cs.Name)
	}
	return "/" + cs.Name, nil
}

// deviceComponents returns the sample count of a device colour space. It
// mirrors internal/pdfdoc's componentsOf, which is unexported; both list the
// three device spaces ISO 32000-1 section 8.6.4 defines.
func deviceComponents(space string) (int, bool) {
	switch space {
	case "DeviceGray":
		return 1, true
	case "DeviceRGB":
		return 3, true
	case "DeviceCMYK":
		return 4, true
	}
	return 0, false
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
//
// FlateDecode additionally has its Data checked against the filter it names:
// EncodedImage.Data is []byte and so is QuantizePNG's return, so a caller can
// hand a whole PNG FILE to the most obvious "make a PDF" entry point and be
// told nothing (byb-vq6 -- measured: BuildPDF returned nil, and the written
// page failed to read back with "zlib: invalid header"). Parsing the zlib
// header is the cheapest check that catches that entire class. For a
// non-Indexed image it is the only one: it does not catch a well-formed
// stream of the wrong dimensions, and deliberately does not decompress,
// because a page image is megabytes and inflating every one of them to verify
// a length would cost more than a build.
//
// An /Indexed image is the exception and does get inflated, because for it
// the sample values are not just pixels, they are offsets into a table: a
// sample larger than hival indexes past the end of the lookup, and pdfcpu
// -- byblos's own reader -- does not bounds check it (writeImage.go's
// renderIndexedRGBToPNG panics with "index out of range"). Having inflated
// it, the check also notices an /Indexed stream that runs out before the rows
// it declares, which is the same panic from the other side; that is a side
// effect for /Indexed only, not a dimension check the other colour spaces get.
// See maxSampleValue for what it costs.
func validatePage(p Page) error {
	if !inRange(p.WidthPt) || !inRange(p.HeightPt) {
		return fmt.Errorf("page box %gx%g is not in the representable range [%g, %g] points", p.WidthPt, p.HeightPt, minCoord, maxCoord)
	}
	img := p.Image
	if img.Width <= 0 || img.Height <= 0 {
		return fmt.Errorf("image dimensions %dx%d are not positive", img.Width, img.Height)
	}
	// byb-37h: each axis is bounded by maxImagePixels BEFORE the product is
	// taken, before either dimension reaches maxSampleValue or the scaling
	// arithmetic in Write. Width and Height are declared ints and can arrive
	// from parsed file headers (EmbedPNG's IHDR is a uint32 each way) or from
	// a caller-built Page, so either can be large enough that Width*Height
	// overflows uint64 itself -- a uint64 product alone is not enough, it
	// just moves the wrap from 2^63 to 2^64 (e.g. Width=1<<61, Height=8
	// still computes to 0 and passes). Bounding each axis to maxImagePixels
	// first makes the subsequent product at most (1<<26)*(1<<26) = 1<<52,
	// nowhere near uint64's range, so it cannot wrap. This also subsumes
	// byb-jo9's old `Width > 1<<31` bound: maxImagePixels is 1<<26, so any
	// Width admitted here is already far under 1<<31, which is what made
	// rowLen := (img.Width*img.BPC+7)/8 wrap in the first place.
	if img.Width > maxImagePixels || img.Height > maxImagePixels {
		return fmt.Errorf("image is %dx%d, which exceeds the maximum of %d pixels", img.Width, img.Height, maxImagePixels)
	}
	if uint64(img.Width)*uint64(img.Height) > maxImagePixels {
		return fmt.Errorf("image is %dx%d (%d pixels), which exceeds the maximum of %d pixels", img.Width, img.Height, uint64(img.Width)*uint64(img.Height), maxImagePixels)
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
		if _, err := colorSpaceObject(img.ColorSpace); err != nil {
			return err
		}
		if _, err := zlib.NewReader(bytes.NewReader(img.Data)); err != nil {
			return fmt.Errorf("FlateDecode: data does not decode under the filter it declares: %w", err)
		}
		if img.ColorSpace.Name == "Indexed" {
			max, err := maxSampleValue(img)
			if err != nil {
				return fmt.Errorf("FlateDecode: /Indexed data cannot be checked against its palette: %w", err)
			}
			if max > img.ColorSpace.HiVal {
				return fmt.Errorf("FlateDecode: /Indexed data holds index %d, past the palette's hival %d", max, img.ColorSpace.HiVal)
			}
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

// maxSampleValue returns the largest sample in img's FlateDecode data,
// decoding it the way a reader does: inflate, undo the /DecodeParms
// predictor, then unpack Width samples of BPC bits from each of Height rows.
//
// This is not a free ride on the zlib check above. That check parses the
// two-byte header and stops; measured on a 2550x3300 4-bit indexed page (US
// Letter at 300 DPI, the shape QuantizeIndexed produces, 18 KB compressed and
// 4.2 MB inflated) it costs 0.0017 ms against 1.5 ms to inflate the same
// stream in full. The lane that started emitting /Indexed declined this check
// partly on the belief that the stream was already being inflated; it was
// not.
//
// What it does cost, on that page: 6.8 ms and 45 KB, against 0.0036 ms for
// the surrounding Write, which only copies an already-compressed stream
// through. So this is very nearly the entire cost of writing an indexed page.
// In context it is still small: deflating that same page at the compression
// QuantizeIndexed uses takes 37.5 ms, five times more, and the caller has
// already paid it before BuildPDF is ever reached. A 500-page archive spends
// about 3.4 s here.
//
// It streams. One row is held plus the row before it (the PNG predictors
// reference the row above), so the memory cost is a row, not a page, however
// large the image.
func maxSampleValue(img pdfdoc.EncodedImage) (int, error) {
	zr, err := zlib.NewReader(bytes.NewReader(img.Data))
	if err != nil {
		return 0, err
	}
	defer func() { _ = zr.Close() }()

	predicted, err := pngPredicted(img)
	if err != nil {
		return 0, err
	}
	// ISO 32000-1 section 8.9.5.1: each image row begins on a byte boundary.
	rowLen := (img.Width*img.BPC + 7) / 8

	largest := 0
	var cur, prev []byte
	ft := make([]byte, 1)
	for y := 0; y < img.Height; y++ {
		if predicted {
			if _, err := io.ReadFull(zr, ft); err != nil {
				return 0, fmt.Errorf("row %d: reading the predictor's filter type: %w", y+1, err)
			}
		}
		if cur == nil {
			// The first row is read into a buffer that grows as the bytes
			// arrive, so a Width x Height declaration far larger than the
			// data behind it is reported as a short stream rather than
			// allocated for. Once one row has proven the stream really is
			// that wide, prev can be allocated outright.
			var err error
			if cur, err = readRow(zr, rowLen); err != nil {
				return 0, fmt.Errorf("row %d: %w", y+1, err)
			}
			// PNG treats the row above row 1 as all zeroes.
			prev = make([]byte, rowLen)
		} else if _, err := io.ReadFull(zr, cur); err != nil {
			return 0, fmt.Errorf("row %d: %w", y+1, err)
		}
		if predicted {
			if err := unfilterPNGRow(ft[0], cur, prev); err != nil {
				return 0, fmt.Errorf("row %d: %w", y+1, err)
			}
		}
		if v := maxInRow(cur, img.BPC, img.Width); v > largest {
			largest = v
		}
		// The reconstructed row becomes the row above; its predecessor is
		// the buffer the next row is read into.
		cur, prev = prev, cur
	}
	return largest, nil
}

// readRow reads exactly n bytes, growing its buffer as they arrive rather
// than allocating n up front.
func readRow(r io.Reader, n int) ([]byte, error) {
	row, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return nil, err
	}
	if len(row) != n {
		return nil, fmt.Errorf("stream holds %d of the %d bytes the row needs", len(row), n)
	}
	return row, nil
}

// pngPredicted reports whether img's data carries the PNG predictors' per-row
// filter type byte -- ISO 32000-1 table 10 gives /Predictor >= 10 to them,
// and 1 or absent to "no prediction" -- and refuses parameters whose layout
// maxSampleValue cannot reconstruct rather than guessing at one.
func pngPredicted(img pdfdoc.EncodedImage) (bool, error) {
	p := img.DecodeParms
	if p == nil || p.Predictor <= 1 {
		return false, nil
	}
	if p.Predictor < 10 {
		return false, fmt.Errorf("/DecodeParms /Predictor %d (TIFF prediction) has no byblos producer", p.Predictor)
	}
	// ISO 32000-1 table 10 defaults: 1 colour, 8 bits, 1 column.
	colors, bpc, columns := p.Colors, p.BitsPerComponent, p.Columns
	if colors == 0 {
		colors = 1
	}
	if bpc == 0 {
		bpc = 8
	}
	if columns == 0 {
		columns = 1
	}
	if colors != 1 || bpc != img.BPC || columns != img.Width {
		return false, fmt.Errorf("/DecodeParms (%d colours, %d bits, %d columns) describes different rows than the image (1 colour, %d bits, %d columns)",
			colors, bpc, columns, img.BPC, img.Width)
	}
	return true, nil
}

// unfilterPNGRow reconstructs one PNG-predicted row in place, given the
// reconstructed row above it (ISO/IEC 15948 section 9.2, which ISO 32000-1
// table 10 adopts wholesale for /Predictor >= 10).
//
// PNG's bpp -- the byte distance to the pixel on the left -- is 1 for every
// row that reaches here: pngPredicted has already required /Colors 1, and BPC
// is at most 8, so a "pixel" rounds up to a single byte.
func unfilterPNGRow(ft byte, row, prev []byte) error {
	switch ft {
	case 0: // None
	case 1: // Sub
		for i := 1; i < len(row); i++ {
			row[i] += row[i-1]
		}
	case 2: // Up
		for i := range row {
			row[i] += prev[i]
		}
	case 3: // Average
		for i := range row {
			var left byte
			if i > 0 {
				left = row[i-1]
			}
			row[i] += byte((int(left) + int(prev[i])) / 2)
		}
	case 4: // Paeth
		for i := range row {
			var left, upLeft byte
			if i > 0 {
				left, upLeft = row[i-1], prev[i-1]
			}
			row[i] += paethPredictor(left, prev[i], upLeft)
		}
	default:
		return fmt.Errorf("predictor filter type %d is not one of PNG's 0..4", ft)
	}
	return nil
}

// paethPredictor is ISO/IEC 15948 section 9.4's PaethPredictor: of the byte
// to the left (a), the byte above (b) and the byte above-left (c), the one
// closest to a+b-c.
func paethPredictor(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := absInt(p-int(a)), absInt(p-int(b)), absInt(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// maxInRow returns the largest of the first width samples of bpc bits packed
// into row, high-order bits first (ISO 32000-1 section 8.9.5.1). Bits past
// the last sample are row padding, which a reader ignores, so they are not
// considered here either.
//
// It walks bytes, not samples, because a sample-indexed loop needs a divide
// and a modulo per sample to find the byte and the shift, and there are
// Width x Height of them: at 8.4 megapixels that division was measured at
// most of this check's whole cost.
func maxInRow(row []byte, bpc, width int) int {
	if bpc == 8 {
		largest := 0
		for _, b := range row[:width] {
			if int(b) > largest {
				largest = int(b)
			}
		}
		return largest
	}
	perByte := 8 / bpc
	full := width / perByte
	largest := 0
	for _, b := range row[:full] {
		if v := maxInByte(b, bpc, perByte); v > largest {
			largest = v
		}
	}
	// The last byte of a row whose width is not a whole number of bytes is
	// only partly samples; the rest is padding.
	if rem := width % perByte; rem > 0 {
		if v := maxInByte(row[full], bpc, rem); v > largest {
			largest = v
		}
	}
	return largest
}

// maxInByte returns the largest of the first n samples of bpc bits in b,
// high-order bits first.
func maxInByte(b byte, bpc, n int) int {
	mask := byte(1<<bpc - 1)
	largest := 0
	for i := 0; i < n; i++ {
		if v := int(b >> (8 - bpc*(i+1)) & mask); v > largest {
			largest = v
		}
	}
	return largest
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
	cs, err := colorSpaceObject(img.ColorSpace)
	if err != nil {
		return "", err
	}
	d := fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d /BitsPerComponent %d /ColorSpace %s /Filter /%s",
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
