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
	"encoding/ascii85"
	"fmt"
	"strconv"
	"strings"
)

// Count is how many documents All() returns; ReadableCount is how many of them
// a PDF reader can open, which is every one except "malformed", the scan
// truncated mid-body.
//
// THESE ARE THE SINGLE SOURCE OF THE TWO NUMBERS. Both are quoted in prose all
// over the tree -- the design spec's section 8 acceptance row, and doc comments
// in optimize.go, optimize_test.go, stamp_test.go and linearize_test.go -- and
// every one of those said 27 through three successive documentation
// reconciliations while the corpus grew, because nothing connected the prose to
// the code (byb-a20).
//
// Adding a document is therefore two edits and no more: put it in All(), and
// bump the constant here. TestAllMatchesTheDeclaredCount (this package) and
// TestCorpusReadableCountIsWhatTheCorpusDeclares plus
// TestCorpusCountClaimsMatchTheCorpus (root package, designspec_pin_test.go)
// then fail until every quoted figure in the tree agrees.
const (
	Count         = 37
	ReadableCount = 36
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

	// ocrTextContent is the invisible OCR layer every scan pipeline ships,
	// transcribed from ia-DTIC_ADA134285.pdf p20. Rendering mode 3 paints no
	// glyphs, so this deposits no ink anywhere on the page.
	ocrTextContent = "BT\n3 Tr /F1 1 Tf\n11.4 0 0 12 119 703.2 Tm (References)Tj\nET\n"
	// ocrTextBracketed restores mode 0 after the showing operator, the idiom
	// measured on govdocs1/004513.pdf p1. The text is still invisible: what the
	// stream mentions is not what was in force when the glyphs were shown.
	ocrTextBracketed = "BT\n3 Tr /F1 1 Tf\n11.4 0 0 12 119 703.2 Tm (References)Tj\n0 Tr\nET\n"
	// ocrTextInheritedMode sets no mode of its own and shows text in whatever
	// mode it inherits from the stream that invoked it.
	ocrTextInheritedMode = "BT\n/F1 1 Tf\n11.4 0 0 12 119 703.2 Tm (References)Tj\nET\n"

	// BornDigitalTextChars is len("Byblos born-digital page one.") +
	// len("Second") + len("line") + len("here.") = 29 + 6 + 4 + 5.
	BornDigitalTextChars = 44
	// OverlayTextChars is len("Scanned 2026-07-27").
	OverlayTextChars = 18
	// InvisibleTextChars is len("References"). TextChars counts an invisible
	// layer like any other text; only classification treats it differently.
	InvisibleTextChars = 10
)

const helveticaFont = "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"

// The placement matrices the three off-axis scan documents use, in PDF matrix
// order [a b c d e f]. They are exported so a test asserts against a named
// value rather than repeating six magic numbers.
//
// The shapes come from a measurement over govdocs1 (byb-b1.2): of 159 pages
// whose page-covering raster was not placed axis-aligned, 147 were sub-degree
// scanner deskew, 8 were a vertical mirror, and 4 were a true quarter turn.
var (
	// DeskewPlacement rotates the raster by 0.13 degrees clockwise — the median
	// deskew of those 147 pages, in the same sign convention as the measured
	// stream (b negative, c positive). govdocs1/005393.pdf p91 carries the whole
	// content stream `q 560.65283 -0.56462 0.76572 760.3374 14.97417 16.36581 cm
	// /Im0 Do Q`; the numbers here are that rotation applied to a raster
	// oversized by six points, so the placement still covers the page and the
	// document exercises the rotation rather than the coverage rule (byb-b1.3).
	DeskewPlacement = [6]float64{617.99841, -1.40220, 1.81061, 797.99795, -3.9, -3}

	// MirrorPlacement flips the raster vertically: b and c are zero, so no
	// rotation test can see it, and UnitSquareBox still reports a page-covering
	// box. Only the sign of d gives it away.
	MirrorPlacement = [6]float64{PageWidthPt, 0, 0, -PageHeightPt, 0, PageHeightPt}

	// QuarterTurnPlacement is an exact 90-degree rotation: a and d are zero.
	QuarterTurnPlacement = [6]float64{0, PageHeightPt, -PageWidthPt, 0, PageWidthPt, 0}

	// NaturalDPIPlacement is ia-DTIC_ADA383635.pdf p40's, the shape byb-b1.3
	// measured on 132 pages across 17 files. The raster is placed at its own
	// resolution rather than stretched to the page: 2384x3321 pixels at 302 DPI
	// is 568.37 x 791.76 points on a nominal 612x792 MediaBox, which covers
	// 91.74% of it and leaves a 43.6 point blank strip down the right-hand side.
	// The page's entire content stream is `/GS1 gs q 568.3708 0 0 791.7616 0 0
	// /Im40 Do Q` — nothing else can mark that strip.
	//
	// Measured placement DPI was a round number in every dominant file: DTIC
	// 302/303, CIA 299.3, dc-1238360 400.0, govdocs1 200/300/300.3.
	NaturalDPIPlacement = [6]float64{568.3708, 0, 0, 791.7616, 0, 0}

	// SubPointPlacement squeezes the raster into 0.4 point on BOTH axes. Every
	// edge of the placed box rounds to the same integer as its opposite edge, so
	// a projection that rounds to nearest reports an empty rectangle for a
	// placement that paints (byb-62t).
	//
	// It is content, not a rounding ghost. poppler 26.06.0 renders the page: a
	// 4x4 grey image under this matrix inks 2 pixels at 72 DPI and 9 at 300, and
	// pdfimages -list reports the image present at 720 ppi, which is what 4
	// pixels across 0.4 point is.
	SubPointPlacement = [6]float64{0.4, 0, 0, 0.4, 10, 10}

	// SubPointStripePlacement is the same squeeze on ONE axis only: 0.4 point
	// wide and the full height of the page. It separates a per-axis projection
	// from a whole-box one, and it is the harder case to call invisible --
	// poppler inks 792 pixels of it at 72 DPI and 9,900 at 300, a black stripe
	// running the length of the page.
	SubPointStripePlacement = [6]float64{0.4, 0, 0, PageHeightPt, 10, 0}
)

// cmOperands renders a placement matrix as the six operands of a `cm` operator.
// FormatFloat with precision -1 is the shortest decimal that round-trips, so the
// generated bytes stay stable and exactly reproduce the matrix above.
func cmOperands(m [6]float64) string {
	s := make([]string, len(m))
	for i, v := range m {
		s[i] = strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strings.Join(s, " ")
}

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
		{"scan-clipped-corner", "a full-page image clipped by `re W n` to a 100x100pt corner: must extract with Bounds the corner, not the raster placement, and CoversPage false (byb-b1.12)",
			scanClippedCorner()},
		{"scan-cropped-by-form-bbox", "a full-page image inside a Form XObject whose /BBox actually crops it to a 100x100pt corner: must extract with Bounds the corner, and CoversPage false (byb-b1.12; the only form fixture whose /BBox narrows anything)",
			scanCroppedByFormBBox()},
		{"scan-clip-narrower-than-raster-box", "a page clip that narrows the placement on Y only, so recorded ClipBox and RasterBox differ: must extract with ClipBox the actual clip, not RasterBox (byb-b1.12)",
			scanClipNarrowerThanTheRasterBox()},
		{"scan-clipped-away", "a page-covering image clipped by a `re W n` rectangle entirely off the page: must divert, not extract the full raster against an empty raster_box (byb-b1.12)",
			scanClippedAway()},
		{"scan-deskewed", "page-covering image placed with a 0.13 degree scanner deskew: must NOT divert",
			scanPlaced(cmOperands(DeskewPlacement), 0)},
		{"scan-natural-dpi", "raster placed at its own 302 DPI on a nominal Letter box, 43.6 points short: must NOT divert",
			scanPlaced(cmOperands(NaturalDPIPlacement), 0)},
		{"scan-stamped", "natural-DPI raster plus a /Stamp with an appearance stream in the uncovered strip: must extract, and report the stamp it cannot include",
			stampedScan()},
		{"scan-bilevel", "page-covering raster declared /BitsPerComponent 1, stored uncompressed so it extracts: the corpus's only bitonal image that reaches ExtractPageRaster (byb-xcx)",
			scanBilevel()},
		{"scan-reversed-cropbox", "page-covering image on a /CropBox whose corners are named UR-then-LL: must NOT record an inverted page_box",
			scanReversedCropBox()},
		{"scan-mirrored", "page-covering image placed with a vertical mirror: must divert",
			scanPlaced(cmOperands(MirrorPlacement), 0)},
		{"scan-quarter-turn", "page-covering image placed with a true 90 degree rotation: must divert",
			scanPlaced(cmOperands(QuarterTurnPlacement), 0)},
		{"tiled", "two half-page images: must divert", tiled()},
		{"overlay-text", "page-covering image plus text inside a Form XObject: must divert", overlayText()},
		{"overlay-vector", "page-covering image plus a stroked rectangle: must divert", overlayVector()},
		{"background-wash", "page-covering image over a background wash painted first: must NOT divert", backgroundWash()},
		{"invisible-text", "page-covering image under an invisible OCR layer (3 Tr): must NOT divert", ocrScan(ocrTextContent, "")},
		{"invisible-text-in-form", "the OCR layer, and its 3 Tr, inside a Form XObject: must NOT divert", ocrScan("q /Fm0 Do Q\n", ocrTextContent)},
		{"invisible-text-form-inherits", "3 Tr set by the page, the OCR layer shown inside a form: must NOT divert", ocrScan("q 3 Tr /Fm0 Do Q\n", ocrTextInheritedMode)},
		{"invisible-text-bracketed", "the `3 Tr ... Tj ... 0 Tr` idiom: must NOT divert", ocrScan(ocrTextBracketed, "")},
		{"mixed", "two pages: born-digital then scan", mixed()},
		{"dup-raster", "two pages holding a byte-identical raster as two objects: both must extract", dupRaster()},
		{"jbig2", "one page-covering JBIG2 raster: 1 bpc, and a codec byblos cannot decode", jbig2()},
		{"jpx", "one page-covering JPXDecode raster: 8 bpc DeviceRGB, and the codec byblos unconditionally diverts (byb-ybu)", jpx()},
		{"stacked", "two page-covering images at the identical CTM: the second occludes the first", stackedPair(false, 0)},
		{"stacked-in-form", "the occluding image is painted inside a Form XObject: paint order must survive the recursion", stackedInForm()},
		{"stacked-smask", "the occluding image carries an /SMask, so it cannot be assumed to hide what is below: must divert", stackedPair(true, 0)},
		{"stacked-alpha", "the occluding image is painted under /ca 0.5: must divert", stackedPair(false, 0.5)},
		{"mrc", "bitonal page-covering base plus a smaller non-bitonal patch: must divert", mrc()},
		{"mrc-inset-base", "the MRC shape with the placements Google Books really emits: the base falls short of the page box on every edge", mrcInsetBase()},
		{"indirect-kids", "page tree whose /Kids is an indirect reference: both pages must still read", indirectKids()},
		{"blank-page", "two pages: a scan, then a page whose /Contents decodes to zero bytes -- one blank page must not fail the document", blankPage()},
		{"booklet", "an eight-page OCR'd booklet: pages 2-8 share one font page 1 does not use, an outline tree spans the document, page 5 states its stream /Length indirectly, and every page carries a different amount of text (byb-woy)",
			booklet()},
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

// fillStreamIndirectLength is fillStream with the stream dictionary's /Length
// stated as a reference to lenObj rather than as a direct integer. ISO 32000-1
// section 7.3.8.2 explicitly allows it, and real producers do it when they
// cannot know the length until the stream is finished -- byblos's own writer
// never emits one, which is exactly why nothing in the corpus carried the
// shape until booklet() did (byb-woy).
func (w *writer) fillStreamIndirectLength(n int, dict string, payload []byte, lenObj int) {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(payload); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	w.offsets[n-1] = w.buf.Len()
	fmt.Fprintf(&w.buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d 0 R >>\nstream\n",
		n, dict, lenObj)
	w.buf.Write(z.Bytes())
	w.buf.WriteString("\nendstream\nendobj\n")
	w.fill(lenObj, strconv.Itoa(z.Len()))
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
	return w.finishWithInfo(root, 0)
}

// finishWithInfo is finish, plus a trailer /Info entry when info is nonzero.
func (w *writer) finishWithInfo(root, info int) []byte {
	start := w.buf.Len()
	fmt.Fprintf(&w.buf, "xref\n0 %d\n0000000000 65535 f \n", len(w.offsets)+1)
	for i, off := range w.offsets {
		if off < 0 {
			panic(fmt.Sprintf("corpus: object %d was reserved but never filled", i+1))
		}
		fmt.Fprintf(&w.buf, "%010d 00000 n \n", off)
	}
	infoEntry := ""
	if info != 0 {
		infoEntry = fmt.Sprintf(" /Info %d 0 R", info)
	}
	fmt.Fprintf(&w.buf,
		"trailer\n<< /Size %d /Root %d 0 R%s >>\nstartxref\n%d\n%%%%EOF\n",
		len(w.offsets)+1, root, infoEntry, start)
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

// noMediaBox is a one-page document that declares no /MediaBox ANYWHERE: not on
// the page, not on the /Pages node, nowhere in the inheritance chain.
//
// ISO 32000-1 7.7.3.3 makes /MediaBox a required inheritable page attribute, so
// this document is malformed. It is also real: byb-8ly measured 9 of 4,840
// govdocs1 files exactly like it -- all PDF 1.0, none using object streams,
// with the string "MediaBox" absent from the file's bytes altogether. Byblos
// refused all 9 while poppler read every one and reported 612x792, which is not
// a measurement of the document but the universal reader default.
//
// The raster is page-covering at that default size, so a reader that defaults
// as poppler does sees a page-covering scan.
// NoMediaBox is exported and deliberately NOT in All(): every write-path test
// iterates the corpus, and pdfcpu refuses to WRITE a page dict with no
// /MediaBox, so registering it would fail 12 optimize and linearize tests that
// have nothing to do with byb-8ly. Read-path tests reach for it by name.
func NoMediaBox() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	// No /MediaBox on the page, and none on the /Pages node to inherit.
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n",
		PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 3))
	return w.finish(cat)
}

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

// InlineImageScan is a one-page document whose only raster is a BI ... ID ...
// EI inline image (ISO 32000-1 8.9.7), page-covering and with no image
// XObject anywhere in the page's /Resources. byb-js5.6: 1,235 of the pinned
// sample's 169,376 pages are shaped exactly like this -- an inline image with
// no XObject image alongside it -- which byblos.Inspect could not see before
// that bead. It is not registered in All() because every write-path test
// iterates the corpus and this document carries no XObject for those tests to
// exercise; read-path tests reach for it by name, as NoMediaBox does.
func InlineImageScan() []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Contents %d 0 R >>", pages, PageWidthPt, PageHeightPt, cont))
	pixel := grayPixels(1, 1, 5)
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm BI /W 1 /H 1 /CS /G /BPC 8 ID ",
		PageWidthPt, PageHeightPt) + string(pixel) + "EI Q\n"
	w.fillStream(cont, "", []byte(content))
	return w.finish(cat)
}

func scan(rotate int) []byte {
	return scanPlaced(fmt.Sprintf("%d 0 0 %d 0 0", PageWidthPt, PageHeightPt), rotate)
}

// scanPlaced is the one-page scan document with an arbitrary placement matrix,
// so the corpus can carry placements measured on real scans rather than only the
// axis-aligned ideal.
func scanPlaced(cm string, rotate int) []byte {
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
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %s cm /Im0 Do Q\n", cm)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// ScanPlacedAt is the one-page scan document under a caller-chosen placement
// matrix, for a geometry no named corpus document carries.
//
// Exported and deliberately NOT in All(), for the reason NoMediaBox gives: All()
// is iterated by every write-path test and its length is a measured figure in
// five files (byb-3y8), so a document added there costs nine edits that have
// nothing to do with the bead adding it. A test that needs one placement reaches
// for this by name.
func ScanPlacedAt(m [6]float64) []byte { return scanPlaced(cmOperands(m), 0) }

// scanBilevel is scan(0) with the raster declared /BitsPerComponent 1 and
// stored uncompressed, so it extracts rather than diverting.
//
// It exists because byb-xcx could not otherwise be tested end to end. Before
// this document the corpus had exactly one bitonal image -- jbig2() -- and that
// one diverts as an undecodable codec, so every raster reaching
// ExtractPageRaster was 8 bpc and no test could tell a dropped bit depth from a
// correct one. A unit test on the predicate alone would not cover the seam this
// bead is about, which is the extraction path carrying the fact.
//
// Uncompressed on purpose: a real bitonal codec (CCITT, JBIG2) diverts as
// unsupported-codec whatever the declared depth is, which is the same masking
// problem mrcInsetBase's comment describes.
func scanBilevel() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1", ScanImageW, ScanImageH),
		bilevelPixels(ScanImageW, ScanImageH))
	return w.finish(cat)
}

// bilevelPixels returns a 1-bit-per-component raster of horizontal bars. Rows
// are padded to a byte boundary (ISO 32000-1 section 8.9.5.1) and a set bit in
// DeviceGray is white, as whitePixels records. Bars rather than all-white so
// the oracle's pixel hash has something to compare.
func bilevelPixels(w, h int) []byte {
	stride := (w + 7) / 8
	out := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		v := byte(0xFF)
		if y%8 < 4 {
			v = 0x00
		}
		for x := 0; x < stride; x++ {
			out[y*stride+x] = v
		}
	}
	return out
}

// scanReversedCropBox is scan(0) with an explicit /CropBox whose corners are
// named in the diagonally opposite order from the usual [llx lly urx ury] --
// legal under ISO 32000-1 7.9.5, which requires a consumer to normalize. It
// exists to catch a writer that stores a page's box verbatim instead of
// normalizing it: [612 792 0 0] is the same rectangle as [0 0 612 792], but a
// naive reader of the raw numbers sees an inverted, degenerate one.
func scanReversedCropBox() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /CropBox [%d %d 0 0]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// ScanFractionalCropBox is scan(0) with an explicit /CropBox offset by frac
// points from the MediaBox on every edge -- byb-2mt's review found
// PageGeometry.CoversPage and PageRaster.CoversPage can disagree on a
// fractional CropBox once RasterQuad is in play (PageRaster rounds the page
// box to integers before testing the quad against it; PageGeometry does
// not), and this is what reproduces that.
func ScanFractionalCropBox(frac float64) []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /CropBox [%g %g %g %g]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt,
		frac, frac, float64(PageWidthPt)-frac, float64(PageHeightPt)-frac,
		img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// stampedScan is the natural-DPI placement carrying a /Stamp in the 43.6 point
// strip the raster does not reach.
//
// This is the byb-b1.11 shape. classify sees a lone raster and no content
// operator anywhere outside it, so the page extracts — correctly, on the
// content stream's own evidence — and the stamp a reader would see is not in
// the returned image. It is the page that must change behaviour if the
// decision on that bead is ever revisited from "report it" to "divert on it".
//
// The /AP dictionary is DIRECT, which is the common form in real documents and
// the one pdfcpu's own Annotation helper cannot see, because that reads /AP
// with IndirectRefEntry. Only the appearance stream itself is indirect.
func stampedScan() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	annot, ap := w.reserve(), w.reserve()

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R /Annots [%d 0 R] >>",
		pages, PageWidthPt, PageHeightPt, img, cont, annot))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %s cm /Im0 Do Q\n", cmOperands(NaturalDPIPlacement))))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	// Rect sits beyond the raster's 568.3708 right edge and inside the 612 point
	// page: the blank strip, which nothing in the content stream marks.
	w.fill(annot, fmt.Sprintf("<< /Type /Annot /Subtype /Stamp /Rect [575 100 605 200]"+
		" /F 4 /AP << /N %d 0 R >> >>", ap))
	w.fillStream(ap, "/Type /XObject /Subtype /Form /BBox [0 0 30 100]",
		[]byte("0 0 0 rg 0 0 30 100 re f\n"))
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

// scanClippedCorner is a full-page raster placement narrowed by a `re W n`
// clip path to a 100x100pt corner of the page. The image XObject itself is
// still placed page-covering (`612 0 0 792 0 0 cm`); only the clip makes the
// visible mark a corner. It exists for byb-b1.12's acceptance criterion,
// verbatim: "A placement clipped by a form /BBox or a clip path reports the
// visible box, and a clipped page-covering image no longer reports CoversPage
// true."
func scanClippedCorner() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q 0 0 100 100 re W n %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// scanClippedAway is a page-covering image clipped by a `re W n` rectangle
// entirely off the page and disjoint from the image, so the placement's Box
// narrows to a zero-area rectangle outside page_box. It exists to pin
// classify's "clipped-away" reason: extracting here would return the full
// raster bytes against a raster_box nobody can trust (B-review.json's second
// major finding).
func scanClippedAway() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf(
		"q 700 800 50 50 re W n %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// scanCroppedByFormBBox is a full-page image placed inside a Form XObject
// whose /BBox actually crops it to a 100x100pt corner -- unlike scanInForm's
// (and every other form fixture's) page-sized /BBox, which narrows nothing.
// It exists because internal/pdfdoc's /BBox read (the only wire between a
// real PDF and content.XObject.BBox) had zero end-to-end coverage without it:
// that read can be deleted and the suite does not notice when every form
// fixture's /BBox is a no-op (B-mutate.json PROBE 12).
func scanCroppedByFormBBox() []byte {
	w := newWriter()
	cat, pages, page, cont, form, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Fm0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, form, cont))
	w.fillStream(cont, "", []byte("q 1 0 0 1 0 0 cm /Fm0 Do Q\n"))
	w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 100 100]"+
		" /Matrix [1 0 0 1 0 0] /Resources << /XObject << /Im0 %d 0 R >> >>",
		img),
		[]byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// scanClipNarrowerThanTheRasterBox is a page clip that narrows the placement
// on only one axis, so the recorded ClipBox and RasterBox are NOT the same
// rectangle: the clip is wider than the image's own extent on X (so X is
// bounded by the image, not the clip) and narrower on Y (so Y is bounded by
// the clip). It exists because extract.go's ClipBox can be sourced from
// placement.Box (the already-clipped RasterBox) instead of placement.Clip
// (the actual clip rectangle) with 0 FAIL on every other fixture, since every
// other clipped fixture happens to have the clip and the narrowed box come
// out identical (B-mutate.json PROBE 3).
func scanClipNarrowerThanTheRasterBox() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	// Clip: (-50,-50)-(200,200). Image: 100 0 0 300 0 0, i.e. Box (0,0)-(100,300)
	// before clipping. X: the clip (-50..200) is wider than the image (0..100),
	// so the image's own edge bounds X. Y: the clip (-50..200) is narrower than
	// the image (0..300), so the clip bounds Y. Box after clipping is therefore
	// (0,0)-(100,200) -- neither the clip rectangle nor the unclipped image box.
	w.fillStream(cont, "", []byte(
		"q -50 -50 250 250 re W n 100 0 0 300 0 0 cm /Im0 Do Q\n"))
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

// ocrScan builds a page-covering scan carrying an invisible OCR text layer.
//
// pageText is appended to the page's own content stream, after the raster.
// formText, when not empty, becomes a Form XObject the page can invoke as
// /Fm0 — the shape 14 files and 207 pages of the DocumentCloud sample take,
// where Tesseract's /GlyphLessFont text and its `3 Tr` are both out of sight of
// anything that reads only the page stream.
func ocrScan(pageText, formText string) []byte {
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	font, img := w.reserve(), w.reserve()
	xobjects := fmt.Sprintf("/Im0 %d 0 R", img)
	form := 0
	if formText != "" {
		form = w.reserve()
		xobjects += fmt.Sprintf(" /Fm0 %d 0 R", form)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> /XObject << %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, xobjects, cont))
	// The raster is painted first and the text over it, so these documents say
	// nothing about z-order: they are about the rendering mode alone.
	w.fillStream(cont, "", []byte(
		fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)+pageText))
	w.fill(font, helveticaFont)
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	if formText != "" {
		w.fillStream(form, fmt.Sprintf("/Type /XObject /Subtype /Form /BBox [0 0 %d %d]"+
			" /Resources << /Font << /F1 %d 0 R >> >>", PageWidthPt, PageHeightPt, font),
			[]byte(formText))
	}
	return w.finish(cat)
}

// backgroundWashContent is the shape byb-b1.5 measured: a full-page rectangle
// filled in an explicit background colour, and then a page-covering raster
// painted over it. 117 of the 126 pages in that bucket set a background fill
// before the first Do — 90 white, 27 the PowerPoint slide colour.
//
// The operators are govdocs1/005697.pdf's entire 132-byte content stream, which
// is identical on all 45 pages of that file:
//
//	/Cs6 cs 1 1 1 scn
//	/GS1 gs
//	0.029999 0.03009 610.5 791.94 re
//	f
//	0 0 0 scn
//	q
//	610.559937 0 0 792.000061 -0.000012 -0.000031 cm
//	/Im1 Do
//	Q
//
// Two numbers differ here. The wash is widened to 611.94 and the placement to
// the full page, because the measured raster is 610.56 points wide on a 612
// point MediaBox and so falls foul of covers() — that page is in byb-b1.3's
// bucket, not this one, and a fixture that diverted for the other reason would
// prove nothing about this one. The measured geometry is asserted directly in
// TestClassifyOnTheMeasuredWashPage instead.
const backgroundWashContent = "/Cs6 cs 1 1 1 scn\n" +
	"/GS1 gs\n" +
	"0.029999 0.03009 611.94 791.94 re\n" +
	"f\n" +
	"0 0 0 scn\n" +
	"q\n" +
	"612 0 0 792 0 0 cm\n" +
	"/Im0 Do\n" +
	"Q\n"

func backgroundWash() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	// /Cs6 and /GS1 are declared so the stream references live resources, as the
	// measured document does. Byblos resolves neither — it records the colour
	// space by name and never reads an ExtGState — but poppler generates the
	// differential golden from these same bytes, and an undefined resource is a
	// difference in the fixture rather than in what is under test.
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> /ColorSpace << /Cs6 /DeviceRGB >>"+
		" /ExtGState << /GS1 << /Type /ExtGState >> >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(backgroundWashContent))
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

// mixedPageTwoUnreadable is mixed() with page 2's content stream declared
// /FlateDecode and filled with bytes that are not flate. The document opens
// cleanly -- pdfdoc.Open runs no validator -- and Page(2) resolves, but
// decoding its content fails, so a caller walking every page hits a genuine
// per-page read failure on a page after the first, as opposed to malformed()'s
// failure inside pdfdoc.Open itself before any page is ever reached.
//
// IT USED TO WORK BY REMOVING PAGE 2'S /MediaBox, and byb-8ly took that
// mechanism away: byblos now defaults a missing /MediaBox to US Letter rather
// than refusing the page, because 9 of 4,840 govdocs1 documents declare none
// and poppler reads every one. The MixedPageTwoUnreadable comment below had
// already recorded that poppler defaults rather than errors here -- so this
// fixture was built on the one behaviour that turned out to be the defect.
func mixedPageTwoUnreadable() []byte {
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
	// Declared flate, and not flate. fillRawStream writes the payload through
	// untouched, which fillStream would not.
	w.fillRawStream(c2, "/Filter /FlateDecode", []byte("not a flate stream at all"))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	return w.finish(cat)
}

// MixedPageTwoUnreadable exposes mixedPageTwoUnreadable directly rather than
// through ByName/All: poppler defaults a missing /MediaBox rather than
// erroring, so this document would fail TestInspectAgreesWithPoppler and
// TestExtractedRasterMatchesPdfimages for disagreeing with poppler on exactly
// the page it exists to make pdfdoc.Doc.Page reject -- that divergence is the
// point of the fixture, not a bug to reconcile against the oracle.
func MixedPageTwoUnreadable() []byte { return mixedPageTwoUnreadable() }

// TruncatedContentStreamText is page 2's content in PageTwoStopsMidStream: two
// legal text-showing operators, then a literal string that is never closed.
//
// The two Tj operands are 5 and 6 bytes, so a walk that gets as far as the
// damage has counted exactly TruncatedContentStreamChars, and one that throws
// its work away on the error counts zero. That difference is the whole point of
// the fixture.
const TruncatedContentStreamText = "BT /F1 12 Tf 72 720 Td (hello) Tj 0 -14 Td (world!) Tj ET\n" +
	"BT /F1 12 Tf 72 690 Td (never closed"

// TruncatedContentStreamChars is what a walk of TruncatedContentStreamText
// counts before the unterminated string: len("hello") + len("world!").
const TruncatedContentStreamChars = 11

// PageTwoStopsMidStream is a two-page document whose second page decodes
// perfectly and then fails to LEX partway through, which is a different failure
// from mixedPageTwoUnreadable's: there the stream never decodes at all and the
// page yields nothing, and here the page yields real content up to the damage.
//
// That is the case poppler handles by painting what it reached -- on
// 050734.pdf page 8 it renders 182 characters out of a content stream that
// stops after 1,156 bytes -- and the case byblos discarded entirely before
// byb-3jq.
//
// Exported directly rather than through All(), like MixedPageTwoUnreadable: a
// document that exists to be half-broken has no business in the sweeps that
// compare every corpus document against poppler page for page.
func PageTwoStopsMidStream() []byte { return pageTwoStopsMidStream() }

func pageTwoStopsMidStream() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, font := w.reserve(), w.reserve(), w.reserve()
	p2, c2 := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, c1))
	w.fillStream(c1, "", []byte(bornDigitalContent))
	w.fill(font, helveticaFont)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, font, c2))
	// A real flate stream, so pdfdoc decodes it and hands the lexer every byte.
	w.fillStream(c2, "", []byte(TruncatedContentStreamText))
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

// DupRasterWithInfo is dupRaster with a single Info-dictionary entry set
// directly in the trailer, as a PDF literal string. It exists so a test can
// build a document that already carries an Info entry (e.g. a
// byblos-provenance record) WITHOUT that entry ever having passed through a
// pdfcpu optimize pass: writing it via the library's own WriteProvenance
// always runs one (it is itself a full pdfcpu read-validate-optimize-write
// pass), which leaves no headroom to observe a SECOND pdfcpu rewrite
// actually shrinking the document any further. Hand-rolling the Info entry
// here is what makes that second rewrite observable.
func DupRasterWithInfo(key, value string) []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, i1 := w.reserve(), w.reserve(), w.reserve()
	p2, c2, i2 := w.reserve(), w.reserve(), w.reserve()
	info := w.reserve()
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
	w.fill(info, fmt.Sprintf("<< /%s (%s) >>", key, escapePDFLiteral(value)))
	return w.finishWithInfo(cat, info)
}

// escapePDFLiteral backslash-escapes the three bytes a PDF literal string
// (...) treats specially (ISO 32000-1 section 7.3.4.2), so an arbitrary
// value can be embedded without unbalancing the string.
func escapePDFLiteral(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\\' || r == '(' || r == ')' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
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
// is the corpus's only bitonal image (ImageRef.Bitonal true) and one of its
// undecodable codecs (ErrUnsupportedImageCodec) -- the other is jpx, below.
//
// Known gap, now half closed: no document in All() sets /ImageMask true, the
// other disjunct of ImageRef.Bitonal. A stencil mask is not extractable at all —
// pdfcpu's ExtractImage rejects it with "invalid components/bpc 0/1" — so
// putting one in the corpus every extraction test walks would add a document
// that can only ever produce a failure. ScanImageMask (images.go) is that
// document as a STANDALONE fixture instead, for the tests that need the disjunct
// without extracting it (byb-js5.2).
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

// jpx is a page-covering raster stored with /Filter /JPXDecode, /ColorSpace
// /DeviceRGB and 8 bpc -- the shape a real JPEG 2000 scan declares. It is the
// corpus's only unconditional codec divert: unlike jbig2(), byblos has no
// JPEG 2000 decoder in either direction, so every page reaching this codec
// diverts with ErrUnsupportedImageCodec regardless of what the bytes hold
// (extract.go's jpx case).
//
// The survey behind byb-ybu found jpx the codec that matters most on scan-shaped
// pages -- 84.0% on ia and 54.8% on commons, against 7.4% and 12.4% for jbig2 --
// yet no corpus document exercised the divert arm that guards it.
func jpx() []byte {
	w := newWriter()
	cat, pages, page, cont, img := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, cont))
	w.fillStream(cont, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillRawStream(img, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /JPXDecode", ScanImageW, ScanImageH),
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

// mrcInsetBase is the same two-tier shape as mrc, with the placements the
// measured file really uses. The content stream is verbatim from p105 of
// ia-municipaldocume00masgoog.pdf, one of the 153 pages whose bitonal base is
// blank while the JPEG 2000 patch carries every word:
//
//	q /GS1 gs 400.080017 0 0 615.600037 9.959999 1.559990 cm /J2i0 Do Q
//	q /GS1 gs 354.239990 0 0 615.359985 11.879999 1.799990 cm /JXi0 Do Q
//
// The difference from mrc, and the whole reason this document exists: the base
// is placed at its own resolution and falls about 10 points short of the page
// box on every edge. It covers 94.7% of the page and the patch 83.8%, matching
// the measurement, but a page-covering test built on covers() rejects it. A
// guard that recognises only mrc's idealised full-page base is dead code on the
// file it was written for.
//
// The page box is 420x619, the size at which those placements reproduce the
// measured coverage. The rasters are Flate rather than JBIG2 and JPEG 2000 on
// purpose: real codecs divert as unsupported-codec whatever classify decided,
// and that would mask a classification regression rather than expose it. The
// base is blank, as p105's is.
func mrcInsetBase() []byte {
	const (
		pageW, pageH   = 420, 619
		baseW, baseH   = 400, 616 // 1 bpc, blank; the real one is 3334x5130
		patchW, patchH = 369, 641 // 8 bpc; the real one is 738x1282
	)
	w := newWriter()
	cat, pages, page, cont := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	base, patch := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	w.fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /ExtGState << /GS1 << /Type /ExtGState /CA 1 /ca 1 >> >>"+
		" /XObject << /J2i0 %d 0 R /JXi0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, pageW, pageH, base, patch, cont))
	w.fillStream(cont, "", []byte(
		"q /GS1 gs 400.080017 0 0 615.600037 9.959999 1.559990 cm /J2i0 Do Q\n"+
			"q /GS1 gs 354.239990 0 0 615.359985 11.879999 1.799990 cm /JXi0 Do Q\n"))
	w.fillStream(base, fmt.Sprintf("/Type /XObject /Subtype /Image /Width %d /Height %d"+
		" /ColorSpace /DeviceGray /BitsPerComponent 1", baseW, baseH), whitePixels(baseW, baseH))
	w.fillStream(patch, imageDict(patchW, patchH), grayPixels(patchW, patchH, 9))
	return w.finish(cat)
}

// whitePixels returns an all-white 1-bit-per-component raster: the blank base.
// Rows are padded to a byte boundary (ISO 32000-1 section 8.9.5.1), and a set
// bit in DeviceGray is white.
func whitePixels(w, h int) []byte {
	return bytes.Repeat([]byte{0xFF}, (w+7)/8*h)
}

// indirectKids is the page tree Google Books PDF Converter (rel 1 21/8/06)
// emits, reduced to two pages. Three things about it are load-bearing, and all
// three were measured on ia-revistadasocied03portgoog.pdf (byb-5kk):
//
//   - The /Pages node's /Kids is an indirect reference to the array object
//     rather than the array itself. ISO 32000-1 section 7.3.10 permits any
//     object that is not a stream to be indirect, so this is legal, and poppler
//     reads these files without complaint.
//   - Every page carries its own /MediaBox and an indirect /Resources.
//   - The page tree is flat: one /Pages node with every page as a direct kid.
//
// The first of those is the regression this document guards. pdfcpu's page tree
// walk reads /Kids with types.Dict.ArrayEntry, which returns nil rather than
// dereferencing, so an unrepaired /Pages node looks like a childless leaf and
// PageDict hands that node back as the dictionary of every page in the file.
func indirectKids() []byte {
	w := newWriter()
	cat, pages, kids := w.reserve(), w.reserve(), w.reserve()
	p1, res1, cont1, img1, annot := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	p2, res2, cont2, img2 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)

	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids %d 0 R /Count 2 /Rotate 0 >>", kids))
	w.fill(kids, fmt.Sprintf("[ %d 0 R %d 0 R ]", p1, p2))

	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources %d 0 R /Contents %d 0 R"+
		" /MediaBox [ 0 0 %d %d ] /Annots [%d 0 R] >>",
		pages, res1, cont1, PageWidthPt, PageHeightPt, annot))
	w.fill(res1, fmt.Sprintf("<< /XObject << /Im0 %d 0 R >> >>", img1))
	w.fillStream(cont1, "", []byte(body))
	w.fillStream(img1, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	w.fill(annot, "<< /Type /Annot /Subtype /Square /Rect [ 0 0 0 0 ] /F 2 >>")

	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /Resources %d 0 R /Contents %d 0 R"+
		" /MediaBox [ 0 0 %d %d ] >>",
		pages, res2, cont2, PageWidthPt, PageHeightPt))
	w.fill(res2, fmt.Sprintf("<< /XObject << /Im0 %d 0 R >> >>", img2))
	w.fillStream(cont2, "", []byte(body))
	w.fillStream(img2, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 6))
	return w.finish(cat)
}

// rotateInheritance is a two-page document whose /Pages node declares
// /Rotate 90: page 1 has no /Rotate of its own and must report the inherited
// 90, and page 2 declares its own /Rotate 180, which must override it. Both
// pages otherwise carry nothing -- an empty content stream and no resources --
// because the fixture exists only to exercise inheritance resolution, not
// content.
func rotateInheritance() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1 := w.reserve(), w.reserve()
	p2, c2 := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 /Rotate 90 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, c1))
	w.fillStream(c1, "", nil)
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d] /Rotate 180"+
		" /Resources << >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, c2))
	w.fillStream(c2, "", nil)
	return w.finish(cat)
}

// RotateInheritance exposes rotateInheritance directly rather than through
// ByName/All, for the reason NoMediaBox gives: All() is iterated by every
// write-path test and its length is a measured figure in five files
// (byb-3y8), so a document added there costs edits (Count, wantNames) that
// have nothing to do with the bead adding it. This fixture exists for one
// test, byb-yul.2's proof that PageInfo.Rotate resolves /Rotate through
// page-tree inheritance rather than reading only the leaf dict.
func RotateInheritance() []byte { return rotateInheritance() }

// blankPage is a two-page document whose second page is blank in the way six
// of 200 govdocs1 files are blank (byb-uxb; byb-cqs). Page 2's /Contents is a
// perfectly well-formed /FlateDecode stream whose payload decodes to ZERO
// bytes -- the shape dug out of govdocs1/750088.pdf p2, where object 3 is
// << /Filter /FlateDecode /Length 9 >> and those nine bytes inflate to
// nothing. Nothing on the page, nothing wrong with the page: a duplex
// scanner's back side, a section separator, a deliberately blank form page.
// poppler reads all six of those files without complaint.
//
// Page 1 carries the ordinary page-covering scan so the document measures the
// real cost. pdfcpu's PageContent reports a zero-byte content stream with
// model.ErrNoContent, so a reader that takes that for a failure loses page 1
// as well -- the whole document, over a page that contains nothing at all.
func blankPage() []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, img := w.reserve(), w.reserve(), w.reserve()
	p2, c2 := w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c1))
	w.fillStream(c1, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, c2))
	// Flate-compressed emptiness: a real stream, a real /Length, no bytes out.
	w.fillStream(c2, "", nil)
	return w.finish(cat)
}

// CorruptContentStreamPayload is the nine bytes blankPage's empty content
// stream would carry if it had been damaged in transit: a valid zlib header
// (0x78 0x9c, exactly what compress/zlib writes) followed by a deflate block
// whose type bits are the reserved value 11. zlib.NewReader accepts the
// header, and the inflate then fails with "flate: corrupt input before offset
// N".
//
// That prefix is load-bearing. pdfcpu's decodeContentStream swallows precisely
// that error under the relaxed validation mode pdfdoc.Open uses -- it logs
// "skipped" and returns nil -- which leaves the stream's content empty, and
// PageContent then reports the page with the SAME model.ErrNoContent it uses
// for a legitimately blank one. A shredded page and an empty page arrive at
// pdfdoc as the identical error.
var CorruptContentStreamPayload = []byte{0x78, 0x9c, 0x07, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}

// CorruptContentStream is blankPage with page 2's zero-byte content stream
// replaced by CorruptContentStreamPayload: the same /FlateDecode dictionary,
// the same zero bytes of usable content, and a stream that did not decode.
//
// CorruptContentStreamInArray is the same damage with page 2's /Contents
// written as the array form ISO 32000-1 table 30 also permits -- an intact
// empty stream first, the corrupt one second. Real producers write /Contents
// as an array routinely (every incremental append does), and a reader that
// checks only the first entry, or only handles the single-stream form, gets
// the identical zero bytes and the identical model.ErrNoContent from pdfcpu
// while being wrong about the page.
//
// Neither is in All(). poppler reads both files as two pages and simply
// renders nothing for page 2, so a document that must make Byblos report a
// damaged page cannot also satisfy TestInspectAgreesWithPoppler -- the
// divergence is the point of the fixture. Same reasoning, same treatment as
// MixedPageTwoUnreadable.
func CorruptContentStream() []byte { return corruptContentStream(false) }

// CorruptContentStreamInArray is CorruptContentStream with page 2's /Contents
// written as an array. See CorruptContentStream.
func CorruptContentStreamInArray() []byte { return corruptContentStream(true) }

func corruptContentStream(inArray bool) []byte {
	w := newWriter()
	cat, pages := w.reserve(), w.reserve()
	p1, c1, img := w.reserve(), w.reserve(), w.reserve()
	p2, c2 := w.reserve(), w.reserve()
	contents := fmt.Sprintf("%d 0 R", c2)
	empty := 0
	if inArray {
		empty = w.reserve()
		contents = fmt.Sprintf("[%d 0 R %d 0 R]", empty, c2)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R %d 0 R] /Count 2 >>", p1, p2))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, img, c1))
	w.fillStream(c1, "", []byte(fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)))
	w.fillStream(img, imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 1))
	w.fill(p2, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents %s >>",
		pages, PageWidthPt, PageHeightPt, contents))
	w.fillRawStream(c2, "/Filter /FlateDecode", CorruptContentStreamPayload)
	if empty != 0 {
		w.fillStream(empty, "", nil)
	}
	return w.finish(cat)
}

// BadAdler32ContentStream returns a one-page document whose content stream is a
// valid FlateDecode stream with a deliberately corrupted Adler-32 checksum.
// The compressed data decodes perfectly until checksum validation, yielding
// a recoverable prefix. This proves pdfcpu v0.15's stricter checksum handling
// can still recover when only the trailing checksum is corrupt. See byb-3iw.
//
// NOT in All(). It exists to test recovery only.
func BadAdler32ContentStream() []byte {
	content := []byte("BT /F1 12 Tf 50 750 Td (Recoverable text.) Tj ET")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(content); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	compressed := z.Bytes()
	if len(compressed) < 6 {
		panic("compressed stream too short")
	}
	compressed[len(compressed)-1] ^= 0xFF

	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	w.fillRawStream(c1, "/Filter /FlateDecode", compressed)
	return w.finish(cat)
}

// CleanContentsArray returns a one-page document whose /Contents is a
// three-element array of ordinary, undamaged FlateDecode streams.
//
// Nothing about it is corrupt, and that is the point. Byblos decodes /Contents
// itself (see pdfdoc.pageContents, byb-3iw), so the SEPARATOR between array
// elements is now byblos's decision, and the corpus had no fixture that could
// observe it: joining the elements with a newline changed clean pages by one
// byte per element and the whole suite stayed green. ISO 32000-1 table 30 says
// the elements are concatenated as a single stream with no byte inserted, which
// is what pdfcpu and every other reader do.
//
// CleanContentsArrayExpected is the concatenation byblos must produce.
func CleanContentsArray() []byte {
	w := newWriter()
	cat, pages, p1 := w.reserve(), w.reserve(), w.reserve()
	ids := []int{w.reserve(), w.reserve(), w.reserve()}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents [%d 0 R %d 0 R %d 0 R] >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, ids[0], ids[1], ids[2]))
	for i, id := range ids {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write([]byte(cleanArrayElements[i])); err != nil {
			panic(err)
		}
		if err := zw.Close(); err != nil {
			panic(err)
		}
		w.fillRawStream(id, "/Filter /FlateDecode", z.Bytes())
	}
	return w.finish(cat)
}

// cleanArrayElements are CleanContentsArray's three element payloads. Each ends
// WITHOUT a trailing newline so that a separator inserted between them would
// change the concatenation and be visible.
var cleanArrayElements = [3]string{
	"BT /F1 12 Tf 50 750 Td (one) Tj ET",
	"BT /F1 12 Tf 50 730 Td (two) Tj ET",
	"BT /F1 12 Tf 50 710 Td (three) Tj ET",
}

// CleanContentsArrayExpected is CleanContentsArray's decoded page content: the
// three elements concatenated with no separator.
var CleanContentsArrayExpected = cleanArrayElements[0] + cleanArrayElements[1] + cleanArrayElements[2]

// BadAdler32InArray returns a two-element /Contents array where the first
// stream has a bad Adler-32 and the second is good. This is 150277 p25's
// class: one element fails, a sibling survives, and pdfcpu v0.13 returned
// 89 bytes while round 1 returned 40. See byb-3iw.
func BadAdler32InArray() []byte {
	good := []byte("BT /F1 12 Tf 50 750 Td (Good sibling.) Tj ET")
	bad := []byte("BT /F1 12 Tf 50 730 Td (Bad Adler.) Tj ET")

	var zGood, zBad bytes.Buffer
	zwGood := zlib.NewWriter(&zGood)
	if _, err := zwGood.Write(good); err != nil {
		panic(err)
	}
	if err := zwGood.Close(); err != nil {
		panic(err)
	}
	zwBad := zlib.NewWriter(&zBad)
	if _, err := zwBad.Write(bad); err != nil {
		panic(err)
	}
	if err := zwBad.Close(); err != nil {
		panic(err)
	}
	compBad := zBad.Bytes()
	compBad[len(compBad)-1] ^= 0xFF

	w := newWriter()
	cat, pages, p1, cGood, cBad := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents [%d 0 R %d 0 R] >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, cBad, cGood))
	w.fillRawStream(cBad, "/Filter /FlateDecode", compBad)
	w.fillRawStream(cGood, "/Filter /FlateDecode", zGood.Bytes())
	return w.finish(cat)
}

// MidBlockTruncation returns a FlateDecode stream truncated mid-deflate-block,
// where a real PREFIX is recovered but bytes are lost. This is 150277 p25's
// damage class. See byb-3iw.
func MidBlockTruncation() []byte {
	content := []byte("BT /F1 12 Tf 50 750 Td (This text will be cut short mid-stream.) Tj ET")
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(content); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	compressed := z.Bytes()
	// Truncate halfway through the deflate payload.
	truncated := compressed[:len(compressed)/2]

	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	w.fillRawStream(c1, "/Filter /FlateDecode", truncated)
	return w.finish(cat)
}

// Predictor12BadAdler returns a FlateDecode stream with /Predictor 12 and a
// bad Adler-32. Round 1 recovered it without applying the predictor, handing
// callers PNG-filtered bytes as PDF operators. Must refuse. See byb-3iw.
// The payload is genuinely PNG-row-filtered before it is deflated, which is what
// makes this fixture reach the checksum at all. An unfiltered payload is refused
// earlier, by pdfcpu's own "unexpected PNG predictor" check on the first byte of
// the first row, so it never exercises byblos's predictor gate.
func Predictor12BadAdler() []byte {
	content := []byte("BT /F1 12 Tf 50 750 Td (Text.) Tj ET")
	// /Columns 8 /Colors 1 /BitsPerComponent 8: rows of 8 bytes, each preceded
	// by a filter-type byte. 0 is PNG filter type None, so the row bytes are the
	// content bytes and the un-filtered result is the content itself.
	const columns = 8
	for len(content)%columns != 0 {
		content = append(content, ' ')
	}
	var filtered []byte
	for i := 0; i < len(content); i += columns {
		filtered = append(filtered, 0x00)
		filtered = append(filtered, content[i:i+columns]...)
	}

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(filtered); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	compressed := z.Bytes()
	compressed[len(compressed)-1] ^= 0xFF

	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	dict := "/Filter /FlateDecode /DecodeParms << /Predictor 12 /Colors 1 /BitsPerComponent 8 /Columns 8 >>"
	w.fillRawStream(c1, dict, compressed)
	return w.finish(cat)
}

// FilterChainBadAdler returns a filter chain [/FlateDecode /ASCII85Decode]
// with a bad Adler-32. Round 1 returned ASCII85 ciphertext as PDF operators.
// Must refuse. See byb-3iw.
// The chain is /Filter [/ASCII85Decode /FlateDecode], which is the order real
// producers emit and the order a reader applies left to right: un-ASCII85 first,
// then inflate. The payload is therefore genuinely ASCII85-encoded deflate data,
// with the corruption applied to the Adler-32 INSIDE the flate layer before the
// ASCII85 encoding wraps it.
//
// Getting that order backwards makes an invalid document rather than a chain: a
// [/FlateDecode /ASCII85Decode] stream holding plain deflate data asks the reader
// to ASCII85-decode ordinary text, which fails for reasons that have nothing to
// do with the checksum.
//
// FilterChainBadAdlerExpected is the content a reader must recover. pdfcpu v0.13
// recovered it in full, because it nilled the checksum error before running the
// remaining filters, so anything less is a regression.
func FilterChainBadAdler() []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write([]byte(FilterChainBadAdlerExpected)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	compressed := z.Bytes()
	compressed[len(compressed)-1] ^= 0xFF

	var a85 bytes.Buffer
	enc := ascii85.NewEncoder(&a85)
	if _, err := enc.Write(compressed); err != nil {
		panic(err)
	}
	if err := enc.Close(); err != nil {
		panic(err)
	}
	a85.WriteString("~>") // the EOD marker pdfcpu's ASCII85Decode expects

	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	w.fillRawStream(c1, "/Filter [/ASCII85Decode /FlateDecode]", a85.Bytes())
	return w.finish(cat)
}

// FilterChainBadAdlerExpected is FilterChainBadAdler's decoded content.
const FilterChainBadAdlerExpected = "BT /F1 12 Tf 50 750 Td (Chain.) Tj ET"

// BadAdlerWithTrailingByte returns a one-page document whose FlateDecode content
// stream has a bad Adler-32 AND one extra byte after the zlib stream, because its
// /Length swallowed the EOL before `endstream`.
//
// That producer bug is routine, and it is the shape that refuted an earlier
// version of byb-3iw's fix. pdfcpu's relaxed mode repairs a SHORT /Length but
// leaves a long one's extra bytes in Raw, so a recovery that assumes the Adler-32
// is the last four bytes patches the wrong offsets. Measured: v0.13 returned the
// complete content, that version refused the page.
//
// InArray is the same stream beside a healthy sibling, which is the worse half:
// there the loss was SILENT, half the page's bytes gone with no error -- byb-3iw's
// own 150277 p25 shape reproduced by the fix for it.
//
// BadAdlerTrailingExpected is the damaged element's content.
func BadAdlerWithTrailingByte() []byte        { return badAdlerTrailing(false) }
func BadAdlerWithTrailingByteInArray() []byte { return badAdlerTrailing(true) }

// BadAdlerTrailingExpected and BadAdlerTrailingSiblingExpected are the two
// elements' contents in BadAdlerWithTrailingByteInArray.
const (
	BadAdlerTrailingExpected        = "BT /F1 12 Tf 50 750 Td (Trailing byte.) Tj ET"
	BadAdlerTrailingSiblingExpected = "BT /F1 12 Tf 50 700 Td (Sibling.) Tj ET"
)

func badAdlerTrailing(inArray bool) []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write([]byte(BadAdlerTrailingExpected)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	bad := z.Bytes()
	bad[len(bad)-1] ^= 0xFF
	// The extra byte an over-declared /Length swallows. fillRawStream writes
	// /Length as the byte count it is given, so appending it here is exactly the
	// producer bug: the declared length includes a byte the zlib stream does not.
	bad = append(bad, '\n')

	var good bytes.Buffer
	gw := zlib.NewWriter(&good)
	if _, err := gw.Write([]byte(BadAdlerTrailingSiblingExpected)); err != nil {
		panic(err)
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}

	w := newWriter()
	cat, pages, p1, cBad := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	contents := fmt.Sprintf("%d 0 R", cBad)
	cGood := 0
	if inArray {
		cGood = w.reserve()
		contents = fmt.Sprintf("[%d 0 R %d 0 R]", cBad, cGood)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %s >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, contents))
	w.fillRawStream(cBad, "/Filter /FlateDecode", bad)
	if cGood != 0 {
		w.fillRawStream(cGood, "/Filter /FlateDecode", good.Bytes())
	}
	return w.finish(cat)
}

// DoubleFlateBadInner returns a one-page document whose content stream is flate
// compressed TWICE -- /Filter [/FlateDecode /FlateDecode] -- with the Adler-32 of
// the INNER layer corrupted and the outer layer intact.
//
// The damaged flate layer is therefore not the first one in the pipeline. A
// recovery that repairs the first flate layer it finds rewrites a trailer that was
// already correct, re-decodes the same broken pipeline, and drops the element:
// measured, v0.13 read 372 bytes and that version refused the page, while the
// array form returned 19 of 391 bytes silently (byb-3iw).
//
// InArray pairs it with a healthy sibling, which is the silent half.
//
// DoubleFlateExpected is the damaged element's content, sized past one deflate
// window so the inner stream spans more than a single block.
func DoubleFlateBadInner() []byte        { return doubleFlate(false, false) }
func DoubleFlateBadInnerInArray() []byte { return doubleFlate(true, false) }

// DoubleFlateBothBad is DoubleFlateBadInner with BOTH flate layers' Adler-32
// trailers corrupted, and InArray pairs it with a healthy sibling.
//
// Two damaged layers is the shape that refuted a recovery which repaired only ONE
// of them: every single-layer repair is defeated by the other layer's damage, so
// the element was dropped -- v0.13 read the content in full and byblos refused the
// page, or returned half the array's bytes silently. pdfcpu v0.13 recovered both
// because it nilled the checksum error on EVERY filter application, which is why
// byblos now decodes one filter at a time (byb-3iw).
func DoubleFlateBothBad() []byte        { return doubleFlate(false, true) }
func DoubleFlateBothBadInArray() []byte { return doubleFlate(true, true) }

// DoubleFlateExpected and DoubleFlateSiblingExpected are the two elements'
// contents in DoubleFlateBadInnerInArray.
var (
	DoubleFlateExpected = strings.Repeat("BT /F1 12 Tf 50 750 Td (Double flate.) Tj ET\n", 8)
	// Deliberately short, so a reader that returns only this is obviously
	// returning the sibling rather than the whole page.
	DoubleFlateSiblingExpected = "BT ET 0 0 m\n"
)

func doubleFlate(inArray, bothBad bool) []byte {
	// Inner: zlib the payload, then corrupt its Adler-32.
	var inner bytes.Buffer
	iw := zlib.NewWriter(&inner)
	if _, err := iw.Write([]byte(DoubleFlateExpected)); err != nil {
		panic(err)
	}
	if err := iw.Close(); err != nil {
		panic(err)
	}
	bad := inner.Bytes()
	bad[len(bad)-1] ^= 0xFF

	// Outer: zlib the corrupted inner stream. This layer is intact, so a reader
	// that stops at the first flate layer sees nothing wrong with it.
	var outer bytes.Buffer
	ow := zlib.NewWriter(&outer)
	if _, err := ow.Write(bad); err != nil {
		panic(err)
	}
	if err := ow.Close(); err != nil {
		panic(err)
	}
	outerBytes := outer.Bytes()
	if bothBad {
		// Corrupt the OUTER trailer as well, so neither layer's checksum is right.
		outerBytes[len(outerBytes)-1] ^= 0xFF
	}

	var good bytes.Buffer
	gw := zlib.NewWriter(&good)
	if _, err := gw.Write([]byte(DoubleFlateSiblingExpected)); err != nil {
		panic(err)
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}

	w := newWriter()
	cat, pages, p1, cBad := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	contents := fmt.Sprintf("%d 0 R", cBad)
	cGood := 0
	if inArray {
		cGood = w.reserve()
		contents = fmt.Sprintf("[%d 0 R %d 0 R]", cBad, cGood)
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %s >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, contents))
	w.fillRawStream(cBad, "/Filter [/FlateDecode /FlateDecode]", outerBytes)
	if cGood != 0 {
		w.fillRawStream(cGood, "/Filter /FlateDecode", good.Bytes())
	}
	return w.finish(cat)
}

// OnePageRawStream returns a one-page document whose /Contents is a single stream
// with the given stream-dictionary entries and raw, already-encoded bytes.
//
// It exists so a test can build a damaged stream whose bytes it computes itself,
// without another named fixture. Everything here is a fixture constructor rather
// than a document in All(), so nothing generic existed for that.
func OnePageRawStream(dictEntries string, raw []byte) []byte {
	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	w.fillRawStream(c1, dictEntries, raw)
	return w.finish(cat)
}

// PartialPredictorRowCutTrailer returns a one-page /Contents array whose first
// element is a /Predictor 12 stream with a PARTIAL final row and an Adler-32
// trailer cut to one byte, beside a healthy sibling.
//
// It is the shape that reaches the trailer-length guard in pdfdoc.repairTrailer,
// which a comment once called unreachable. pdfcpu swallows a truncated trailer on
// its passThru path, but WITH a predictor it uses decodePostProcessRows, which
// returns io.ErrUnexpectedEOF unswallowed when the last row is short -- so byblos's
// recovery is entered with fewer than four trailer bytes. Without the guard that
// document panics with a slice-bounds error instead of dropping the element
// (byb-3iw).
//
// The sibling is what makes the test observable: the damaged element is dropped and
// the page still reads, where pdfcpu v0.13 refused the whole page.
func PartialPredictorRowCutTrailer() []byte {
	// /Columns 8 /Colors 1 /BitsPerComponent 8: rows of one filter-type byte plus
	// eight data bytes. The payload is deliberately one byte short of a whole row.
	const columns = 8
	payload := []byte("BT /F1 12 Tf 50 750 Td (Partial row.) Tj ET")
	var filtered []byte
	for i := 0; i < len(payload); i += columns {
		end := i + columns
		if end > len(payload) {
			end = len(payload) // the short final row
		}
		filtered = append(filtered, 0x00)
		filtered = append(filtered, payload[i:end]...)
	}

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(filtered); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	// Cut the four-byte Adler-32 down to one.
	cut := z.Bytes()[:z.Len()-3]

	var good bytes.Buffer
	gw := zlib.NewWriter(&good)
	if _, err := gw.Write([]byte(PartialPredictorSiblingExpected)); err != nil {
		panic(err)
	}
	if err := gw.Close(); err != nil {
		panic(err)
	}

	w := newWriter()
	cat, pages, p1, cBad, cGood := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents [%d 0 R %d 0 R] >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, cBad, cGood))
	dict := "/Filter /FlateDecode /DecodeParms << /Predictor 12 /Colors 1 /BitsPerComponent 8 /Columns 8 >>"
	w.fillRawStream(cBad, dict, cut)
	w.fillRawStream(cGood, "/Filter /FlateDecode", good.Bytes())
	return w.finish(cat)
}

// PartialPredictorSiblingExpected is the healthy element's content in
// PartialPredictorRowCutTrailer, and the whole page's content once the damaged
// element is dropped.
const PartialPredictorSiblingExpected = "BT /F1 12 Tf 50 700 Td (Healthy sibling.) Tj ET"

// TruncatedAdlerTrailer returns a one-page document whose FlateDecode content
// stream has COMPLETE deflate data and a trailer cut short: two of the Adler-32's
// four bytes are missing.
//
// It is the one shape where the deflate data inflates cleanly and there is still
// no trailer to repair. A recovery that sizes its buffer as end+4 without checking
// that four bytes remain reads past the end of the stream and PANICS, which is a
// crash on a malformed file rather than a refusal (byb-3iw).
func TruncatedAdlerTrailer() []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write([]byte("BT /F1 12 Tf 50 750 Td (Cut trailer.) Tj ET")); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	cut := z.Bytes()[:z.Len()-2]

	w := newWriter()
	cat, pages, p1, c1 := w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents %d 0 R >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, c1))
	w.fillRawStream(c1, "/Filter /FlateDecode", cut)
	return w.finish(cat)
}

// CorruptElementBesideHealthySibling returns a one-page document whose /Contents
// array holds CorruptContentStreamPayload -- a valid zlib header followed by a
// RESERVED deflate block type, so it inflates to NOTHING -- next to an ordinary
// healthy stream.
//
// This is the case CorruptContentStream cannot observe. That fixture pairs the
// corrupt stream with an EMPTY one, so the sibling contributes no bytes either
// way and a reader that discards the whole page looks identical to one that
// keeps the sibling. pdfcpu v0.13's relaxed mode dropped the damaged element and
// returned the sibling's bytes; refusing the page instead loses content byblos
// used to read (byb-3iw).
//
// CorruptElementSiblingExpected is the sibling's content.
func CorruptElementBesideHealthySibling() []byte {
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write([]byte(CorruptElementSiblingExpected)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}

	w := newWriter()
	cat, pages, p1, cBad, cGood := w.reserve(), w.reserve(), w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << /Font << /F1 %s >> >> /Contents [%d 0 R %d 0 R] >>",
		pages, PageWidthPt, PageHeightPt, helveticaFont, cBad, cGood))
	w.fillRawStream(cBad, "/Filter /FlateDecode", CorruptContentStreamPayload)
	w.fillRawStream(cGood, "/Filter /FlateDecode", z.Bytes())
	return w.finish(cat)
}

// CorruptElementSiblingExpected is the healthy element's content in
// CorruptElementBesideHealthySibling.
const CorruptElementSiblingExpected = "BT /F1 12 Tf 50 700 Td (Healthy sibling.) Tj ET"

// NullContentsRef returns a one-page document whose /Contents is an indirect
// reference to an object that does not exist.
//
// ISO 32000-1 7.3.10 makes a reference to a nonexistent object null, and 7.3.9
// makes null equivalent to omitting the entry, so this is a legally BLANK page.
// pdfcpu v0.13's PageContent had an explicit arm for it; a reader without one
// refuses a page every other reader renders empty (byb-3iw).
func NullContentsRef() []byte {
	w := newWriter()
	cat, pages, p1 := w.reserve(), w.reserve(), w.reserve()
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	w.fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", p1))
	// 9999 is deliberately outside the xref table this writer emits.
	w.fill(p1, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
		" /Resources << >> /Contents 9999 0 R >>",
		pages, PageWidthPt, PageHeightPt))
	return w.finish(cat)
}

// BadAdler32InArray returns a two-element /Contents array where the first

// BookletPages is how many pages booklet() carries. Six to ten was the range
// byb-woy asked for: enough that the page-offset hint table's per-page loop
// runs a meaningful number of times, and few enough that the generated document
// stays in the same size class as the rest of the corpus.
const BookletPages = 8

// bookletBodyFontFrom is the first page (1-based) that uses the shared OCR body
// font; bookletHeadFontFrom is the first that ALSO uses the shared heading
// font. Page 1 is the cover and carries no text at all, which is what makes
// both fonts objects shared between LATER pages only. The two ranges differ on
// purpose: it is what stops every later page from having the same set of shared
// objects as every other, which is the property that gives the shared
// identifier column something to get wrong (byb-woy).
const (
	bookletBodyFontFrom = 2
	bookletHeadFontFrom = 5
)

// bookletIndirectLengthPage is the page (1-based) whose content stream states
// its /Length indirectly. bookletAnnotPage is the page carrying the hidden
// annotation that makes its object group one bigger than its siblings'.
const (
	bookletIndirectLengthPage = 5
	bookletAnnotPage          = 6
)

// booklet is the corpus's multi-page document, and it exists because every
// other one is one or two pages (byb-woy).
//
// MEASURED, over byblos's own linearized output for every committed fixture
// before this document was added: the deepest corpus document was two pages,
// and part 8 -- "objects shared between later pages but not used by the first
// page", Annex F.3.1 -- was EMPTY for every single one of them. That is not an
// accident of the fixtures, it is arithmetic: PlanLayout counts an object's
// users across pages 2..N only, so on a two-page document that count can never
// exceed one and the part-8 branch is unreachable. No corpus document carried
// an outline tree either, so the outline hint table was never present. Both
// shapes existed only in linearize_test.go's hand-built fixtures, which no
// other sweep over the corpus -- Inspect, extraction, provenance, the pdfcpu
// write round trip, the poppler oracle -- ever sees.
//
// The shape is an OCR'd scanned booklet, which is where all four properties
// occur together in the wild:
//
//   - EIGHT pages, so the per-page columns of the page offset hint table have
//     seven rows to disagree about rather than one, and part 7 has seven groups
//     whose object numbering has to run consecutively across all of them.
//   - TWO font objects shared between later pages and NOT reachable from page
//     1, because the cover is a bare scan with no text over it. That is the
//     part-8 shape, and it is what Table F.5's first_shared_obj and
//     first_shared_offset describe. They are shared by DIFFERENT ranges of
//     pages -- the body font from page 2, the heading font from page 5 -- so
//     the later pages do not all list the same shared identifiers. MEASURED on
//     byblos's linearized output: pages 2-4 list [7] and pages 5-8 list [7 8],
//     and this is the ONLY document in the whole linearization sweep whose
//     later pages differ from each other at all. Every other fixture gives
//     every later page an identical shared set -- including pdfcpu's 64-page
//     bookletTest.pdf, where all 63 later pages list [2 3 4 5 6 7 8] -- so
//     before this document a writer that handed every later page PAGE 2's
//     identifiers passed the entire suite.
//   - An OUTLINE TREE spanning the document, with /PageMode /UseOutlines so
//     F.3.8 places it in the first-page section and the primary hint stream
//     must carry an outline hint table (/O).
//   - DIFFERENT amounts of OCR text per page, so the page-length column is not
//     a run of equal values that any bit order encodes identically. Measured on
//     byblos's linearized output: eight distinct page lengths.
//   - ONE page carrying a hidden annotation, so that page's group holds one
//     more object than its siblings and the object-count column is not a run of
//     equal values either. /F 2 is the hidden flag: the annotation paints
//     nothing, so extraction's DroppedAnnots count is unaffected, which is the
//     same device indirectKids uses.
//   - Page 5's content stream states its /Length INDIRECTLY, a shape byblos's
//     own writer never emits and which no corpus document carried. NOTE what
//     this does and does not reach: Optimize runs pdfcpu's rewrite before the
//     linearizer, and that rewrite turns the reference back into a direct
//     integer, so the linearizer never sees it here. What it does exercise is
//     every path that reads the corpus bytes as they are -- pdfdoc.Open,
//     Inspect, extraction, the write round trip. linearize_test.go's
//     indirectLengthFixture, which goes to the linearizer directly, is what
//     covers the orphan-/Length bug itself.
//
// Every body page is a page-covering raster under an invisible OCR layer, so
// classification treats pages 2..8 exactly as it treats "invisible-text": they
// extract. Page 1 is a bare page-covering scan.
func booklet() []byte {
	w := newWriter()
	cat, tree := w.reserve(), w.reserve()
	bodyFont, headFont := w.reserve(), w.reserve()
	outlineRoot := w.reserve()
	outlineItem := []int{w.reserve(), w.reserve(), w.reserve()}
	page := make([]int, BookletPages)
	cont := make([]int, BookletPages)
	img := make([]int, BookletPages)
	for i := range page {
		page[i], cont[i], img[i] = w.reserve(), w.reserve(), w.reserve()
	}
	contLen := w.reserve() // page bookletIndirectLengthPage's /Length object
	annot := w.reserve()   // page bookletAnnotPage's hidden annotation

	var kids strings.Builder
	for i := range page {
		fmt.Fprintf(&kids, "%d 0 R ", page[i])
	}
	w.fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R /Outlines %d 0 R"+
		" /PageMode /UseOutlines >>", tree, outlineRoot))
	w.fill(tree, fmt.Sprintf("<< /Type /Pages /Kids [ %s] /Count %d >>", kids.String(), BookletPages))
	w.fill(bodyFont, helveticaFont)
	w.fill(headFont, "<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>")

	for i := range page {
		fonts := ""
		body := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im0 Do Q\n", PageWidthPt, PageHeightPt)
		if i+1 >= bookletBodyFontFrom {
			fonts = fmt.Sprintf("/F1 %d 0 R", bodyFont)
			// Rendering mode 3 paints nothing, so this deposits no ink over
			// the raster; the line count rises with the page number so no two
			// pages have the same content length.
			var t strings.Builder
			t.WriteString("BT\n3 Tr /F1 1 Tf\n")
			for j := 0; j <= i; j++ {
				fmt.Fprintf(&t, "11.4 0 0 12 119 %d Tm (page %d line %d)Tj\n", 703-14*j, i+1, j)
			}
			t.WriteString("ET\n")
			body += t.String()
		}
		if i+1 >= bookletHeadFontFrom {
			fonts += fmt.Sprintf(" /F2 %d 0 R", headFont)
			body += fmt.Sprintf("BT\n3 Tr /F2 1 Tf\n9 0 0 9 72 750 Tm (Chapter %d)Tj\nET\n", i)
		}
		res := fmt.Sprintf("/XObject << /Im0 %d 0 R >>", img[i])
		if fonts != "" {
			res = fmt.Sprintf("/Font << %s >> %s", fonts, res)
		}
		annots := ""
		if i+1 == bookletAnnotPage {
			annots = fmt.Sprintf(" /Annots [%d 0 R]", annot)
		}
		w.fill(page[i], fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 %d %d]"+
			" /Resources << %s >> /Contents %d 0 R%s >>",
			tree, PageWidthPt, PageHeightPt, res, cont[i], annots))
		if i+1 == bookletIndirectLengthPage {
			w.fillStreamIndirectLength(cont[i], "", []byte(body), contLen)
		} else {
			w.fillStream(cont[i], "", []byte(body))
		}
		// A distinct seed per page: the pages must be told apart pixel by
		// pixel, or a per-page extraction bug that returned page 1's raster
		// every time would look correct.
		w.fillStream(img[i], imageDict(ScanImageW, ScanImageH), grayPixels(ScanImageW, ScanImageH, 10+i))
	}
	w.fill(annot, "<< /Type /Annot /Subtype /Square /Rect [ 0 0 0 0 ] /F 2 >>")

	// A three-item outline naming the first, a middle and the last page, chained
	// with /Prev and /Next as a reader expects. The /Dest entries are what made
	// a naive first-page closure wrong: an item's destination must not drag a
	// later page's objects into the first-page section.
	dest := []int{0, 3, BookletPages - 1}
	title := []string{"Cover", "Chapter one", "Colophon"}
	w.fill(outlineRoot, fmt.Sprintf("<< /Type /Outlines /Count %d /First %d 0 R /Last %d 0 R >>",
		len(outlineItem), outlineItem[0], outlineItem[len(outlineItem)-1]))
	for k := range outlineItem {
		links := ""
		if k > 0 {
			links += fmt.Sprintf(" /Prev %d 0 R", outlineItem[k-1])
		}
		if k+1 < len(outlineItem) {
			links += fmt.Sprintf(" /Next %d 0 R", outlineItem[k+1])
		}
		w.fill(outlineItem[k], fmt.Sprintf("<< /Title (%s) /Parent %d 0 R /Dest [ %d 0 R /Fit ]%s >>",
			title[k], outlineRoot, page[dest[k]], links))
	}
	return w.finish(cat)
}

// malformed truncates the scan document mid-body, which is what a partial
// upload or a truncated S3 object looks like: a plausible header, a broken
// stream, and no cross-reference table.
func malformed() []byte {
	full := scan(0)
	return full[:len(full)*6/10]
}
