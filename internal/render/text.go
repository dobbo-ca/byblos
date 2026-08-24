package render

// Stage 4c (byb-8b9.3): embedded TrueType text.

import (
	"fmt"
	"math"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/dobbo-ca/byblos/internal/content"
)

// Font is a simple embedded TrueType font, resolved by the caller from the
// page's /Font resources. Like Image, the raw bytes are the caller's to
// fetch; parsing (x/image/font/sfnt) happens here because the outlines could
// not usefully cross the seam.
type Font struct {
	// Program is the raw font program: /FontFile2 sfnt bytes, or (stage 4d)
	// /FontFile3 /Subtype /Type1C bare-CFF bytes, told apart by their
	// headers. A program neither stage can parse makes every glyph skip
	// cleanly; the widths below still advance.
	Program []byte
	// FirstChar and Widths mirror the font dict's /FirstChar and /Widths, in
	// thousandths of text space (ISO 32000-1 9.2.4). A code outside the array
	// advances by /MissingWidth's default, 0 -- the spec's fallback, not the
	// font program's own advance.
	FirstChar int
	Widths    []float64
}

// FontFor resolves a Tf operand to an embedded font. ok=false skips that
// font's shows entirely, advance included -- with no Font there are no
// /Widths to advance by -- where a supplied-but-unparsable Program (see
// textFont) keeps its widths advancing. Either way a non-embedded font, a
// Type1 PFB program (stages 4e-4f), or an unresolved name must not stop the
// rest of the page.
type FontFor func(name string) (Font, bool)

// textFont is one Tf resolution: the parsed program plus the PDF-side widths.
// fnt may be nil (program absent, unparsable, or not an indexable TrueType --
// see parseGlyfIndex): the widths still advance so later shows keep their
// positions, but no glyph inks.
type textFont struct {
	fnt   *sfnt.Font
	upem  float64
	buf   sfnt.Buffer
	first int
	width []float64
}

// resolveFont resolves and caches a Tf operand. A nil cache entry records a
// name FontFor could not supply, so a stream that repeats the Tf pays once.
func (r *renderer) resolveFont(name string) *textFont {
	if f, ok := r.fontsBy[name]; ok {
		return f
	}
	if r.fontsBy == nil {
		r.fontsBy = map[string]*textFont{}
	}
	if r.fonts == nil || name == "" {
		r.fontsBy[name] = nil
		return nil
	}
	src, ok := r.fonts(name)
	if !ok {
		r.fontsBy[name] = nil
		return nil
	}
	tf := &textFont{first: src.FirstChar, width: src.Widths}
	// Refuse before allocating, and before sfnt.Parse: Parse itself loads
	// whole glyphs ('x' and 'H', the OS/2 metrics fallback), so a hostile
	// compound bomb must be caught FIRST -- see glyf.go. A program whose
	// glyphs cannot all be bounded degrades to widths-only exactly like an
	// unparsable one: a collection, malformed tables, or an expansion past
	// the path budget. Past that gate, sfnt.Parse bounds its own remaining
	// work against the hostile shapes it was fuzzed for, and a parse failure
	// degrades the same way, never an error.
	parse := func(program []byte) {
		if f, err := sfnt.Parse(program); err == nil {
			tf.fnt = f
			tf.upem = float64(f.UnitsPerEm())
		}
	}
	if g := parseGlyfIndex(src.Program); g != nil {
		if g.boundedBy(maxPathPoints) {
			parse(src.Program)
		}
	} else if otf := cffToSFNT(src.Program); otf != nil {
		// Stage 4d: a bare CFF (/FontFile3 /Subtype /Type1C), gated and
		// wrapped by cff.go into the container sfnt parses. Anything else --
		// Type1 PFB (stage 4e), garbage -- leaves otf nil: widths-only.
		parse(otf)
	}
	r.fontsBy[name] = tf
	return tf
}

// w0 is the code's glyph displacement in text space (ISO 32000-1 9.4.4):
// /Widths in thousandths where the array covers the code, else /MissingWidth,
// whose default -- and this stage's only value, no descriptor crosses the
// seam -- is 0. Deliberately NOT the font program's own advance: the PDF
// widths override it by spec, and TestTextWidthsOverrideFontAdvance pins
// that.
func (f *textFont) w0(code byte) float64 {
	if i := int(code) - f.first; i >= 0 && i < len(f.width) {
		return f.width[i] / 1000
	}
	return 0
}

// inksText reports whether rendering mode tr deposits ink THIS stage can
// deposit: the filling modes 0, 2, 4 and 6 (ISO 32000-1 table 106). The
// stroke-only modes 1 and 5 and the clip-only mode 7 are recorded here as
// DEFERRED -- their glyphs advance but do not mark, and modes 4-7 install no
// clip -- along with mode 3, which is invisible by definition. A mode outside
// the table marks nothing.
func inksText(tr int) bool { return tr == 0 || tr == 2 || tr == 4 || tr == 6 }

// showText shows one string operand's single-byte codes: for each code, ink
// the glyph under the text rendering matrix, then advance the text matrix by
// the PDF width plus spacing (ISO 32000-1 9.4.4).
func (r *renderer) showText(gs *gstate, raw []byte) error {
	f := gs.font
	if f == nil {
		return nil
	}
	ink := inksText(gs.tr)
	for _, code := range raw {
		if ink {
			if err := r.fillGlyph(gs, f, code); err != nil {
				return err
			}
		}
		tx := gs.fontSize*f.w0(code) + gs.charSp
		if code == ' ' {
			// Word spacing applies to single-byte code 32 (9.3.3).
			tx += gs.wordSp
		}
		gs.tm = content.Matrix{1, 0, 0, 1, tx * gs.hscale, 0}.Mul(gs.tm)
	}
	return nil
}

// glyphMatrix maps sfnt glyph coordinates (font units, y down -- LoadGlyph's
// documented orientation) to device space: flip y and scale by 1/unitsPerEm
// into text space, then the text-space parameters, the text matrix and the
// CTM (Trm, ISO 32000-1 9.4.4).
func glyphMatrix(gs *gstate, upem float64) content.Matrix {
	params := content.Matrix{gs.fontSize * gs.hscale, 0, 0, gs.fontSize, 0, gs.rise}
	return content.Matrix{1 / upem, 0, 0, -1 / upem, 0, 0}.
		Mul(params).Mul(gs.tm).Mul(gs.ctm)
}

// fillGlyph rasterises one glyph outline through the same path machinery as
// the path operators: flattened points charge the path budget (a hostile
// font's absurd contour count trips it rather than hanging) and fillWork too
// (so a stream re-showing one expensive glyph stays bounded per Page), and
// the fill is the 4a scanline filler under the nonzero rule, TrueType's own.
// Any failure short of a budget -- no glyph for the code, a load error, a
// non-finite device coordinate from hostile text parameters -- skips the
// glyph cleanly.
func (r *renderer) fillGlyph(gs *gstate, f *textFont, code byte) error {
	if f.fnt == nil {
		return nil
	}
	// rune(code) is Latin-1, which agrees with WinAnsi/Standard over ASCII;
	// an /Encoding-aware mapping waits for a caller that can supply one.
	gi, err := f.fnt.GlyphIndex(&f.buf, rune(code))
	if err != nil || gi == 0 {
		return nil
	}
	// LoadGlyph materialises the glyph's whole expansion up front, which is
	// safe only because resolveFont refused any font with a glyph expanding
	// past maxPathPoints (glyf.go); the flattened points below still pay the
	// per-point charge, which curve flattening can amplify past the on-disk
	// count.
	// ppem = fixed.Int26_6(upem) hands segment coordinates back in raw font
	// units with no rounding (sfnt.go:598's documented convention), keeping
	// the only scaling in float space under glyphMatrix. fixed.I(upem) would
	// mean the same thing 64x larger, overflowing sfnt's int32 coordinate
	// scaling for legal coordinates beyond 2^31/(64*upem) font units -- 16384
	// at Arial's upem 2048.
	segs, err := f.fnt.LoadGlyph(&f.buf, gi, fixed.Int26_6(f.upem), nil)
	if err != nil {
		return nil
	}
	m := glyphMatrix(gs, f.upem)
	var pth path
	defer pth.reset(r)
	ok := true
	pt := func(p fixed.Point26_6) point {
		x, y := m.Apply(float64(p.X), float64(p.Y))
		// One finite check covers every hostile text parameter at the only
		// place they meet the scanline filler: a NaN or Inf here (a huge Tfs,
		// an Inf TJ adjustment poisoning tm) would otherwise reach fillEdges'
		// int conversions.
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			ok = false
		}
		return point{x, y}
	}
	for _, seg := range segs {
		if !ok {
			break
		}
		var err error
		switch seg.Op {
		case sfnt.SegmentOpMoveTo:
			err = pth.moveTo(r, pt(seg.Args[0]))
		case sfnt.SegmentOpLineTo:
			err = pth.lineTo(r, pt(seg.Args[0]))
		case sfnt.SegmentOpQuadTo:
			// Exact degree elevation to the cubic the 4a flattener speaks:
			// c1 = p0 + 2/3(q-p0), c2 = p2 + 2/3(q-p2), computed in device
			// space (affine maps commute with affine combinations).
			q, end := pt(seg.Args[0]), pt(seg.Args[1])
			p0 := pth.cur
			c1 := point{p0.x + 2*(q.x-p0.x)/3, p0.y + 2*(q.y-p0.y)/3}
			c2 := point{end.x + 2*(q.x-end.x)/3, end.y + 2*(q.y-end.y)/3}
			err = pth.curveTo(r, c1, c2, end)
		case sfnt.SegmentOpCubeTo:
			// Stage 4d: sfnt emits cubics for CFF outlines, already in the
			// form the 4a flattener speaks.
			err = pth.curveTo(r, pt(seg.Args[0]), pt(seg.Args[1]), pt(seg.Args[2]))
		}
		if err != nil {
			return err
		}
	}
	if !ok {
		return nil
	}
	n := int64(pathPoints(pth.subs))
	r.fillWork += n
	if r.fillWork > maxFillWork {
		return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
	}
	return r.fillSubpaths(pth.subs, false, r.deviceClip(gs.clip), gs.fill.rgba)
}
