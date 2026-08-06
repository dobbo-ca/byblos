package byblos

// StampTextLayer (byb-b4 part B) writes an invisible OCR-style text layer
// into a PDF: PDF text rendering mode 3 (invisible), through
// internal/glyphless's empty-outline font, so a reader can select and search
// text that paints nothing (design spec section 7).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"strings"

	"github.com/dobbo-ca/byblos/internal/glyphless"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// PositionedWord is one recognized word and where it sits on the page.
//
// Bounds is in POINTS, in PDF default user space (origin lower-left, y
// increasing upward -- the same convention as PageInfo.Bounds and
// PageRaster.Bounds), with the byb-b1.2 residual affine ALREADY APPLIED and
// /Rotate NOT applied. StampTextLayer places text at Bounds directly; it
// applies no transform of its own. Two consequences worth stating up front,
// because both are easy to get backwards:
//
//   - Points, not the raster's pixel space, so that a caller reading
//     PageRaster.Bounds and a caller writing PositionedWord.Bounds speak the
//     same unit without a conversion at the call site -- two units in one API
//     is a caller trap.
//   - The affine must already be applied. At the measured max 1.09 degree
//     scanner deskew that is ~11.6 pt of drift across a 612 pt page -- two
//     line heights -- and text stamped without it drifts progressively
//     relative to the scan, which is exactly what makes an invisible layer
//     mis-select. StampTextLayer cannot check this; a caller that forgets
//     produces silently drifting text.
//   - /Rotate is NOT applied, and must not be: it is a display attribute that
//     already rotates stamped text along with the page's own content (see
//     extract.go's identical note on PageRaster), so applying it here would
//     rotate it twice.
type PositionedWord struct {
	Text   string
	Bounds image.Rectangle
}

// TextLayer is the recognized text for one document, one page at a time.
// Pages[i] is page i+1's words; a page with no recognized text is an empty
// slice, and StampTextLayer does not touch such a page at all.
type TextLayer struct {
	Pages [][]PositionedWord
}

// ErrUnstampableRune reports a rune outside the glyphless font's coverage
// (internal/glyphless.FirstRune..LastRune, printable ASCII). Substituting a
// different glyph would mangle recognized text invisibly, so StampTextLayer
// errors instead of guessing; a Type0/Identity-H font is the follow-up for
// non-ASCII OCR text.
var ErrUnstampableRune = errors.New("byblos: rune is outside the glyphless font's coverage")

// glyphlessBaseFont names the embedded font object. Every page shares one
// font object, keyed by this name (pdfdoc.Doc.AddFontResource memoizes on
// TrueTypeFont.BaseFont).
const glyphlessBaseFont = "BbyblosGlyphless"

// glyphlessTrueTypeFont builds the pdfdoc.TrueTypeFont value for
// internal/glyphless.Font.
//
// /Ascent 800 and /Descent -200 are not free parameters: they sum to
// glyphless.UnitsPerEm, so the em box spans [baseline-0.2*fs, baseline+0.8*fs]
// -- exactly [Bounds.Min.Y, Bounds.Max.Y] for the ty computed below. Changing
// either value independently breaks that identity; see prepareWord.
func glyphlessTrueTypeFont() pdfdoc.TrueTypeFont {
	widths := make([]int, glyphless.LastRune-glyphless.FirstRune+1)
	for r := glyphless.FirstRune; r <= glyphless.LastRune; r++ {
		w, _ := glyphless.Width(r) // every rune in range is covered
		widths[r-glyphless.FirstRune] = int(w)
	}
	return pdfdoc.TrueTypeFont{
		BaseFont:    glyphlessBaseFont,
		Program:     glyphless.Font,
		FirstChar:   int(glyphless.FirstRune),
		Widths:      widths,
		Flags:       32, // bit 6, Nonsymbolic -- required alongside /Encoding /WinAnsiEncoding
		FontBBox:    [4]int{0, 0, 0, 0},
		ItalicAngle: 0,
		Ascent:      800,
		Descent:     -200,
		CapHeight:   700,
		StemV:       80,
	}
}

// preparedWord is a word's placement, computed once and independent of the
// font resource's name on the page it lands on.
type preparedWord struct {
	text           string
	fs, tz, tx, ty float64
}

// prepareWord validates w and computes its placement. It returns (nil, nil)
// for an empty word (nothing to stamp) and an error naming the page and word
// for a non-positive box or an unstampable rune -- silently dropping
// recognized text is the failure class this repo rejects elsewhere.
func prepareWord(w PositionedWord) (*preparedWord, error) {
	if w.Text == "" {
		return nil, nil
	}
	b := w.Bounds
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("box %v is not positive", b)
	}
	fs := float64(b.Dy())
	var natFU float64 // natural advance, in font units (1000ths of an em)
	for _, r := range w.Text {
		wd, ok := glyphless.Width(r)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnstampableRune, r)
		}
		natFU += float64(wd)
	}
	return &preparedWord{
		text: w.Text,
		fs:   fs,
		// tz scales the natural advance (natFU/1000 * fs points) to the box
		// width; legal outside 0..100 and not clamped, same as poppler and
		// the rest of the PDF ecosystem treat it.
		tz: 100 * float64(b.Dx()) * 1000 / (natFU * fs),
		tx: float64(b.Min.X),
		// 0.2*fs is -Descent/UnitsPerEm; see glyphlessTrueTypeFont's comment.
		ty: float64(b.Min.Y) + 0.2*fs,
	}, nil
}

// ops renders the word's Tf/Tz/Tm/Tj operator group. Tm stays a pure
// translation and all scaling lives in Tf/Tz, so a Tm bug is obvious rather
// than absorbed into a scale factor.
func (p preparedWord) ops(fontName string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "/%s %.4f Tf\n%.4f Tz\n1 0 0 1 %.4f %.4f Tm\n(%s) Tj\n",
		fontName, p.fs, p.tz, p.tx, p.ty, escapePDFString(p.text))
	return buf.Bytes()
}

// escapePDFString backslash-escapes the three bytes ISO 32000-1 7.3.4.2
// requires inside a literal string.
func escapePDFString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\', '(', ')':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// StampTextLayer writes tl into r's pages as an invisible text-render-mode-3
// layer, and copies the result to w.
//
// A page whose tl.Pages[i] is empty (including one beyond the end of a
// shorter tl.Pages) is not touched at all: no font resource is added and no
// content is appended. len(tl.Pages) greater than r's page count is an error.
//
// It cannot be cancelled. Use StampTextLayerContext when the caller has a
// deadline.
func StampTextLayer(w io.Writer, r io.ReadSeeker, tl TextLayer) error {
	return StampTextLayerContext(context.Background(), w, r, tl)
}

// StampTextLayerContext is StampTextLayer, cancellable at each page boundary
// and at each word within a page (byb-xyn).
//
// CANCELLATION LATENCY: ONE PDFCPU PASS. The checks sit at the top of the page
// loop and the top of the word loop, so the stamping itself is interrupted
// between words -- but the per-page tail after that loop (AddFontResource and
// AppendContent) runs unchecked, and the Open before and the d.Write after are
// whole uninterruptible pdfcpu passes. Measured over 120 pages carrying one
// word each, the longest stretch between two context checks was 22% of the
// call; on a document with fewer, longer pages the write dominates further.
// A cancelled call writes nothing to w. See context.go.
func StampTextLayerContext(ctx context.Context, w io.Writer, r io.ReadSeeker, tl TextLayer) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	d, err := pdfdoc.Open(r)
	if err != nil {
		return fmt.Errorf("byblos: stamp text layer: %w", err)
	}
	if len(tl.Pages) > d.PageCount() {
		return fmt.Errorf("byblos: stamp text layer: text layer has %d pages, document has %d",
			len(tl.Pages), d.PageCount())
	}

	font := glyphlessTrueTypeFont()
	for i, words := range tl.Pages {
		if err := checkContext(ctx); err != nil {
			return err
		}
		if len(words) == 0 {
			continue
		}
		pageNum := i + 1

		var prepared []preparedWord
		for wi, word := range words {
			if err := checkContext(ctx); err != nil {
				return err
			}
			pw, err := prepareWord(word)
			if err != nil {
				return fmt.Errorf("byblos: stamp text layer: page %d word %d: %w", pageNum, wi, err)
			}
			if pw == nil {
				continue
			}
			prepared = append(prepared, *pw)
		}
		if len(prepared) == 0 {
			continue
		}

		fontName, err := d.AddFontResource(pageNum, font)
		if err != nil {
			return fmt.Errorf("byblos: stamp text layer: page %d: %w", pageNum, err)
		}
		var buf bytes.Buffer
		buf.WriteString("BT\n3 Tr\n")
		for _, pw := range prepared {
			buf.Write(pw.ops(fontName))
		}
		buf.WriteString("ET\n")
		if err := d.AppendContent(pageNum, buf.Bytes()); err != nil {
			return fmt.Errorf("byblos: stamp text layer: page %d: %w", pageNum, err)
		}
	}

	if err := d.Write(w); err != nil {
		return fmt.Errorf("byblos: stamp text layer: %w", err)
	}
	return nil
}
