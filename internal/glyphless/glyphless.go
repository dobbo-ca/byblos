// Package glyphless is a minimal TrueType font whose glyphs carry no
// outlines: every character prints nothing, but still occupies exactly as
// much horizontal space as a normal proportional glyph would. This is the
// asset byb-b4 (StampTextLayer) needs to write invisible text -- an OCR-style
// layer at PDF text render mode 3 -- without a reader complaining that the
// font is broken or a naive one drawing black boxes.
//
// The font is built once by gen.go and the resulting glyphless.ttf is
// committed. Nothing in this package constructs a font at runtime: gen.go is
// build-tagged //go:build ignore, the same convention testdata/oracle/gen.go
// uses, so it never compiles into the module and CI never runs it. See gen.go
// for how to regenerate the asset (never necessary unless the glyph coverage
// or width table changes).
package glyphless

import _ "embed"

// UnitsPerEm is the font's design grid. It is 1000, not the TrueType-typical
// 2048, so that a glyph's PDF /Widths entry -- always expressed in 1000ths of
// text space per ISO 32000-1 9.2.4 -- equals its hmtx advance width with no
// conversion at the call site that embeds this font.
const UnitsPerEm = 1000

// FirstRune and LastRune bound the codepoints this font covers: printable
// ASCII, space through tilde. That is what an invisible OCR text layer needs
// to reproduce recognized words; it is not meant to cover the codepoints a
// visible body font would.
const (
	FirstRune = ' ' // 0x20
	LastRune  = '~' // 0x7E
)

// NumGlyphs is the font's glyph count: one glyph per covered rune, plus glyph
// 0, which every TrueType font reserves for .notdef (ISO/IEC 14496-22 /
// OpenType spec, "glyf" table). GlyphID never returns 0 for a covered rune.
const NumGlyphs = LastRune - FirstRune + 2

//go:embed glyphless.ttf
var Font []byte

// widths holds each covered rune's advance, in font units (== UnitsPerEm),
// indexed by rune-FirstRune. The values are Adobe's published Helvetica AFM
// metrics (Helvetica.afm, Core 14 set): real per-character proportions, not a
// flat guess, so a word stamped through this font takes the same relative
// shape a visible rendering of it would, which is what lets a caller size
// invisible text to match a measured word box (byb-b4's acceptance criterion
// that word bounding boxes round-trip within tolerance).
//
// This table is duplicated in gen.go, which cannot import this package: the
// package's own go:embed directive requires glyphless.ttf to already exist,
// so gen.go -- the thing that creates that file -- cannot depend on it. If
// you change one, change both; see the identical note on pixelHash in
// testdata/oracle/gen.go, the same constraint for the same reason.
var widths = [LastRune - FirstRune + 1]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // ' ' .. '/'
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // '0' .. '?'
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // '@' .. 'O'
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 'P' .. '_'
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // '`' .. 'o'
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 'p' .. '~'
}

// GlyphID returns r's glyph index in Font. Glyphs are assigned in rune order
// starting at 1 (0 is .notdef), which is also the order gen.go writes hmtx
// entries in and the order its cmap segment maps -- see gen.go's single cmap
// segment, built on exactly this arithmetic.
func GlyphID(r rune) (uint16, bool) {
	if r < FirstRune || r > LastRune {
		return 0, false
	}
	return uint16(r-FirstRune) + 1, true
}

// Width returns r's advance width in font units (UnitsPerEm == 1000), the
// same value gen.go wrote into Font's hmtx table for r's glyph.
func Width(r rune) (uint16, bool) {
	if r < FirstRune || r > LastRune {
		return 0, false
	}
	return widths[r-FirstRune], true
}
