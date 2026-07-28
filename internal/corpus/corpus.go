// Package corpus builds the Byblos test corpus in memory.
//
// Every document here is produced by this code, deterministically. No binary
// PDF fixture of uncertain provenance enters the repository, and `make corpus`
// reproduces the exact bytes the committed poppler goldens were made from.
//
// The PDF writer below is deliberately minimal and hand-rolled rather than
// built on pdfcpu: the corpus must be able to express structures pdfcpu would
// never emit, including a truncated file.
package corpus

import (
	"bytes"
	"compress/zlib"
	"fmt"
)

// Geometry shared by every generated document. US Letter at 72 points/inch.
const (
	PageWidthPt  = 612
	PageHeightPt = 792

	ScanImageW, ScanImageH = 306, 396 // the full-page raster, 36 DPI
	TileImageW, TileImageH = 153, 396 // each half of the tiled page
)

// Seeds for the grey patterns of the stacked documents. The two layers must be
// distinguishable pixel by pixel, because "which one came back?" is the whole
// question those documents ask.
const (
	StackedBaseSeed = 4
	StackedTopSeed  = 5
)

// FirstGray is the first sample of the pattern grayPixels builds for a seed. A
// test that decodes a stacked document reads pixel (0,0) and compares it to
// this, which is cheaper than a second copy of the pattern and just as decisive.
func FirstGray(seed int) uint8 { return grayPixels(1, 1, seed)[0] }

// Text content, kept as constants so tests assert against a named value rather
// than a magic number.
const (
	bornDigitalContent = "BT /F1 12 Tf 1 0 0 1 72 720 Tm (Byblos born-digital page one.) Tj ET\n" +
		"BT /F1 12 Tf 1 0 0 1 72 700 Tm [ (Second) -250 (line) -250 (here.) ] TJ ET\n"
	overlayTextContent = "BT /F1 10 Tf 1 0 0 1 72 40 Tm (Scanned 2026-07-27) Tj ET\n"

	// BornDigitalTextChars is len("Byblos born-digital page one.") +
	// len("Second") + len("line") + len("here.") = 29 + 6 + 4 + 5.
	BornDigitalTextChars = 44
	// OverlayTextChars is len("Scanned 2026-07-27").
	OverlayTextChars = 18
)

const helveticaFont = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

// Doc is one generated test document.
type Doc struct {
	Name string
	Desc string
	Data []byte
}

// All returns the corpus, in a stable order.
func All() []Doc {
	return []Doc{
		{"born-digital", "text only, no images: must never be rasterized", bornDigital()},
		{"scan", "one page-covering image: the case the whole design targets", scan(0)},
		{"scan-rotated", "one page-covering image on a /Rotate 90 page", scan(90)},
		{"scan-in-form", "one page-covering image inside a Form XObject: must NOT divert", scanInForm()},
		{"tiled", "two half-page images: must divert", tiled()},
		{"overlay-text", "page-covering image plus text inside a Form XObject: must divert", overlayText()},
		{"overlay-vector", "page-covering image plus a stroked rectangle: must divert", overlayVector()},
		{"mixed", "two pages: born-digital then scan", mixed()},
		{"dup-raster", "two pages holding a byte-identical raster as two objects: both must extract", dupRaster()},
		{"jbig2", "one page-covering JBIG2 raster: 1 bpc, and a codec byblos cannot decode", jbig2()},
		{"stacked", "two page-covering images at the identical CTM: the second occludes the first", stackedPair(false, 0)},
		{"stacked-in-form", "the occluding image is painted inside a Form XObject: paint order must survive the recursion", stackedInForm()},
		{"stacked-smask", "the occluding image carries an /SMask, so it cannot be assumed to hide what is below: must divert", stackedPair(true, 0)},
		{"stacked-alpha", "the occluding image is painted under /ca 0.5: must divert", stackedPair(false, 0.5)},
		{"mrc", "bitonal page-covering base plus a smaller non-bitonal patch: must divert", mrc()},
		{"malformed", "the scan document truncated mid-body", malformed()},
	}
}

// ByName returns one document's bytes.
func ByName(name string) ([]byte, bool) {
	for _, d := range All() {
		if d.Name == name {
			return d.Data, true
		}
	}
	return nil, false
}

// --- the minimal PDF writer -------------------------------------------------

type writer struct {
	buf     bytes.Buffer
	offsets []int // offsets[i-1] is the byte offset of object i; -1 until filled
}

func newWriter() *writer {
	w := &writer{}
	// The binary comment line marks the file as containing binary data, per
	// ISO 32000-1 section 7.5.2.
	w.buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	return w
}

// reserve allocates an object number to be filled later, so parents can refer
// to children that have not been written yet.
func (w *writer) reserve() int {
	w.offsets = append(w.offsets, -1)
	return len(w.offsets)
}

func (w *writer) fill(n int, body string) {
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n%s\nendobj\n", n, body)
}

// fillStream writes a Flate-compressed stream object. PDF /FlateDecode is the
// zlib format of RFC 1950, which is what compress/zlib produces.
func (w *writer) fillStream(n int, dict string, payload []byte) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d >>\nstream\n", n, dict, z.Len())
	w.buf.Write(z.Bytes())
	w.buf.WriteString("\nendstream\nendobj\n")
}

// fillRawStream writes a stream object whose payload is stored verbatim. The
// dictionary declares whatever /Filter the caller wants; nothing is applied
// here. This exists so the corpus can carry a codec Go has no encoder for —
// see jbig2() below.
func (w *writer) fillRawStream(n int, dict string, payload []byte) {
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(payload))
	w.buf.Write(payload)
	w.buf.WriteString("\nendstream\nendobj\n")
}

// finish writes the cross-reference table and trailer. Each xref entry is
// exactly 20 bytes, as ISO 32000-1 section 7.5.4 requires.
func (w *writer) finish(root int) []byte {
	start := w.buf.Len()
	fmt.Fprintf(&w.buf, "xref\n0 %d\n0000000000 65535 f \n", len(w.offsets)+1)
	for i, off := range w.offsets {
		if off < 0 {
			panic(fmt.Sprintf("corpus: object %d was reserved but never filled", i+1))
		}
		fmt.Fprintf(&w.buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&w.buf,
		"trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(w.offsets)+1, root, start)
	return w.buf.Bytes()
}

func imageDict(w, h int) string {
	return fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 8", w, h)
}

// grayPixels returns deterministic 8-bit grey samples. The pattern is
// arbitrary but must be stable and must compress imperfectly, so that a
// truncation is genuinely damaging.
func grayPixels(w, h, seed int) []byte {
	px := make([]byte, w*h)
	for i := range px {
		px[i] = byte((i*7 + seed*31) % 251)
	}
	return px
}

// --- the documents ----------------------------------------------------------

func bornDigital() []byte {
	w := newWriter()
	cat, pages, page, cont, font := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, cont))
	w.fillStream(cont, "", []byte(bornDigitalContent))
	w.fill(font, helveticaFont)
	return w.finish(cat)
}

func scan(rotate int) []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	rot := ""
	if rotate != 0 {
		rot = fmt.Sprintf(" /Rotate %d", rotate)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]%s"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, rot, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func scanInForm() []byte {
	w := newWriter()
	cat, pages, page, cont, form, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, form, cont))
	w.fillStream(cont, "", []byte("q 1 0 0 1 0 0 cm /Fm0 Do Q\n"))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
		" /Matrix [1 0 0 1 0 0] /Resources << /XObject << /Im0 %d 0 R >> >>",
		PageWidthPt, PageHeightPt, img),
		[]byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func tiled() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	img0, img1 := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R /Im1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img0, img1, cont))
	half := PageWidthPt / 2
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq %d 0 0 %d %d 0 cm /Im1 Do Q\n",
		half, PageHeightPt, half, PageHeightPt, half)))
	w.fillStream(img0, imageDict(TileImageW, TileImageH), grayPixels(TileImageW, TileImageH, 2))
	w.fillStream(img1, imageDict(TileImageW, TileImageH), grayPixels(TileImageW, TileImageH, 3))
	return w.finish(cat)
}

// overlayText hides its text inside a Form XObject. pdfcpu's Images() still
// reports exactly one image for this page, which is why classification has to
// walk the content stream and recurse into forms.
func overlayText() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	form, font, img := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, form, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq /Fm0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >>", PageWidthPt, PageHeightPt, font),
		[]byte(overlayTextContent))
	w.fill(font, helveticaFont)
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func overlayVector() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq 0 0 0 RG 2 w 72 72 468 648 re S Q\n",
		PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

func mixed() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, font := w.reserve(), w.reserve(), w.reserve()
	p2, c2, img := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, c1))
	w.fillStream(c1, "", []byte(bornDigitalContent))
	w.fill(font, helveticaFont)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c2))
	w.fillStream(c2, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// dupRaster gives two pages the same raster bytes as two distinct objects.
// pdfcpu deduplicates byte-identical image XObjects in its optimize pass, so
// an extraction path that asks pdfcpu "which image objects are on page 2?"
// gets page 1's object number back and then cannot find it in the page's own
// resource dictionary. Task 8 resolves the image through the page's resources
// instead; this document is the regression guard for that decision, and the
// duplex-scanner blank-back-page case in its own right.
func dupRaster() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, i1 := w.reserve(), w.reserve(), w.reserve()
	p2, c2, i2 := w.reserve(), w.reserve(), w.reserve()
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)
	px := grayPixels(ScanImageW, ScanImageH, 1)
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, i1, c1))
	w.fillStream(c1, "", []byte(body))
	w.fillStream(i1, imageDict(ScanImageW, ScanImageH), px)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, i2, c2))
	w.fillStream(c2, "", []byte(body))
	w.fillStream(i2, imageDict(ScanImageW, ScanImageH), px)
	return w.finish(cat)
}

// jbig2Payload is 64 bytes of deterministic filler. It is NOT a valid JBIG2
// segment stream and does not need to be: nothing in Byblos or in poppler
// decodes it. What matters is that the /Filter says JBIG2Decode, because that
// is what drives pdfcpu to hand back opaque bytes instead of an error.
func jbig2Payload() []byte {
	p := make([]byte, 64)
	for i := range p {
		p[i] = byte((i*13 + 7) % 251)
	}
	return p
}

// jbig2 is a page-covering 1-bpc raster stored with /Filter /JBIG2Decode. It
// is the corpus's only bitonal image (ImageRef.Bitonal true) and its only
// undecodable codec (ErrUnsupportedImageCodec).
//
// Known gap: no corpus document sets /ImageMask true, the other disjunct of
// ImageRef.Bitonal. A stencil mask is not extractable at all — pdfcpu's
// ExtractImage rejects it with "invalid components/bpc 0/1" — so covering that
// disjunct would mean adding a document that can only ever produce a failure.
// It belongs with the real-world-scans follow-up, not here.
func jbig2() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillRawStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode", ScanImageW, ScanImageH),
		jbig2Payload())
	return w.finish(cat)
}

// stackedPair builds the shape 16,241 measured Internet Archive pages have:
// two page-covering placements at the identical CTM, the second painted over
// the first. The content stream is modelled on ia-06043926.cn.pdf page 1,
// including the leading `cm` that paints nothing — that is what MuPDF emits.
//
// topSMask gives the upper image an /SMask, and topFillAlpha, when non-zero,
// paints it under an /ExtGState with that /ca. Either one makes the upper image
// something other than an opaque cover, so the occlusion argument stops holding
// and the page has to divert.
func stackedPair(topSMask bool, topFillAlpha float64) []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	img0, img1 := w.reserve(), w.reserve()

	topDict := imageDict(ScanImageW, ScanImageH)
	smask := 0
	if topSMask {
		smask = w.reserve()
		topDict += fmt.Sprintf(" /SMask %d 0 R", smask)
	}
	res := fmt.Sprintf("/XObject << /Im0 %d 0 R /Im1 %d 0 R >>", img0, img1)
	gsOp, gsObj := "", 0
	if topFillAlpha > 0 {
		gsObj = w.reserve()
		res += fmt.Sprintf(" /ExtGState << /GS0 %d 0 R >>", gsObj)
		gsOp = "/GS0 gs "
	}

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << %s >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, res, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm Q\nq %d 0 0 %d 0 0 cm /Im0 Do Q\nq %s%d 0 0 %d 0 0 cm /Im1 Do Q\n",
		PageWidthPt, PageHeightPt,
		PageWidthPt, PageHeightPt,
		gsOp, PageWidthPt, PageHeightPt)))
	w.fillStream(img0, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, StackedBaseSeed))
	w.fillStream(img1, topDict, grayPixels(ScanImageW, ScanImageH, StackedTopSeed))
	if topSMask {
		w.fillStream(smask, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 6))
	}
	if gsObj != 0 {
		w.fill(gsObj, fmt.Sprintf("<< /Type /ExtGState /ca %v >>", topFillAlpha))
	}
	return w.finish(cat)
}

// stackedInForm puts the occluding placement inside a Form XObject. Paint order
// is only usable if it survives the recursion, and a form is the one place a
// walk could plausibly lose it.
func stackedInForm() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	img0, form, img1 := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img0, form, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /Im0 Do Q\nq /Fm0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img0, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, StackedBaseSeed))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
		" /Matrix [1 0 0 1 0 0] /Resources << /XObject << /Im1 %d 0 R >> >>",
		PageWidthPt, PageHeightPt, img1),
		[]byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im1 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img1, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, StackedTopSeed))
	return w.finish(cat)
}

// mrc is the two-tier Google Books shape: a bitonal page-covering base plus a
// smaller non-bitonal patch. The resource names are the ones that file uses.
//
// The measured trap is that the base can be blank while the patch carries every
// word on the page, and nothing in the file says which. Byblos cannot tell those
// apart without decoding pixels, so the shape itself diverts.
func mrc() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	base, patch := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /J2i0 %d 0 R /JXi0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, base, patch, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q %d 0 0 %d 0 0 cm /J2i0 Do Q\nq 353 0 0 615 12 2 cm /JXi0 Do Q\n",
		PageWidthPt, PageHeightPt)))
	w.fillRawStream(base, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1 /Filter /JBIG2Decode", ScanImageW, ScanImageH),
		jbig2Payload())
	w.fillStream(patch, imageDict(TileImageW, TileImageH), grayPixels(TileImageW, TileImageH, 7))
	return w.finish(cat)
}

// malformed truncates the scan document mid-body, which is what a partial
// upload or a truncated S3 object looks like: a plausible header, a broken
// stream, and no cross-reference table.
func malformed() []byte {
	full := scan(0)
	return full[:len(full)*6/10]
}
