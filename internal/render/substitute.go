package render

// Stage 4f (byb-8b9.7): substitute glyphs for non-embedded fonts.
//
// 47.8% of the corpus's measured page-1 font uses embed NO font program: the
// PDF names a face and expects the consumer to supply glyphs. Per the
// byb-8b9.6 decision (arm (a), measured), byblos bundles the metric-compatible
// Liberation faces (SIL OFL 1.1 -- the licence file sits beside the TTFs and
// clears design spec section 10's MPL bar; pdf.js ships Liberation Sans for
// exactly this purpose) and substitutes by name, falling back to the
// /FontDescriptor flag bits for names it does not know.
//
// The substitute rides the EXISTING 4c sfnt path -- the bundled files are
// ordinary TrueType, so resolveFont's pre-parse glyph gate, sfnt.Parse, and
// fillGlyph all apply unchanged. This file is a mapping table plus embedded
// assets, not a renderer. The PDF's /Widths still drive every advance where
// present; the substitute supplies outlines, and its own metrics only where
// the PDF carries no /Widths at all (the standard-14 shape -- see w0).
//
// DEFERRED: Symbol and ZapfDingbats have no metric-compatible Liberation
// face; they degrade to widths-only (TestSubstituteFaceMapping records it).
// Non-Latin coverage follows the byb-8b9.6 note: the measured population is
// 0x20-0x7e.

import (
	"embed"
	"strings"
)

//go:embed fonts/Liberation*.ttf
var substituteFonts embed.FS

// FontDescriptor flag bits (ISO 32000-1 table 123; the spec numbers bits
// from 1, so Serif is "bit 2" = 1<<1).
const (
	flagFixedPitch = 1 << 0
	flagSerif      = 1 << 1
	flagSymbolic   = 1 << 2
	flagItalic     = 1 << 6
)

// substituteFace maps a /BaseFont name plus descriptor flags to a bundled
// face path, or "" for the deferred faces. Name wins over flags: the
// standard-14 names and their everyday aliases (Arial for Helvetica, Times
// New Roman for Times, Courier New for Courier -- poppler's own substitution
// table draws the same lines) carry style in the name; anything unrecognised
// falls back to the fixed-pitch/serif/italic flag bits.
func substituteFace(baseFont string, flags int) string {
	name := baseFont
	if i := strings.IndexByte(name, '+'); i >= 0 {
		name = name[i+1:] // subset tag ("ABCDEF+...")
	}
	name = strings.ToLower(name)
	var family string
	switch {
	case strings.Contains(name, "courier"):
		family = "Mono"
	case strings.Contains(name, "times"):
		family = "Serif"
	case strings.Contains(name, "helvetica") || strings.Contains(name, "arial"):
		family = "Sans"
	case strings.Contains(name, "symbol") || strings.Contains(name, "dingbat"):
		return "" // DEFERRED: no metric-compatible open face exists
	default:
		switch {
		case flags&flagSymbolic != 0:
			// An unknown-name symbolic face (Wingdings, a TeX math font):
			// Latin glyphs for symbol codes would misrender, so degrade to
			// widths-only like the named symbol faces above.
			return ""
		case flags&flagFixedPitch != 0:
			family = "Mono"
		case flags&flagSerif != 0:
			family = "Serif"
		default:
			family = "Sans"
		}
	}
	bold := strings.Contains(name, "bold")
	italic := strings.Contains(name, "italic") || strings.Contains(name, "oblique") ||
		flags&flagItalic != 0
	style := "Regular"
	switch {
	case bold && italic:
		style = "BoldItalic"
	case bold:
		style = "Bold"
	case italic:
		style = "Italic"
	}
	return "fonts/Liberation" + family + "-" + style + ".ttf"
}

// substituteProgram returns the bundled TrueType bytes for a non-embedded
// font, or nil for the deferred faces.
func substituteProgram(baseFont string, flags int) []byte {
	face := substituteFace(baseFont, flags)
	if face == "" {
		return nil
	}
	b, err := substituteFonts.ReadFile(face)
	if err != nil {
		return nil
	}
	return b
}
