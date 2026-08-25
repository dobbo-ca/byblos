package render

// Stage 4c (byb-8b9.3): embedded TrueType text.

import (
	"crypto/sha256"
	"fmt"
	"math"

	"golang.org/x/image/font"
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
	// font program's own advance. A dict with NO /Widths at all (the
	// standard-14 shape) instead advances by the resolved program's own
	// metrics; see w0.
	FirstChar int
	Widths    []float64
	// BaseFont and Flags mirror the font dict's /BaseFont and its
	// /FontDescriptor /Flags. Stage 4f reads them only when Program is empty,
	// to pick a bundled substitute face; see substitute.go.
	BaseFont string
	Flags    int
	// Type0 (byb-8b9.8) marks a composite font: a /Subtype /Type0 dict whose
	// /Encoding is Identity-H or Identity-V and whose descendant carries the
	// Program (a CID-keyed CFF, /FontFile3 /Subtype /CIDFontType0C). showText
	// then decodes the string as 2-byte big-endian codes, each code the CID
	// itself (the Identity CMap, ISO 32000-1 9.7.6.2), and W/DW below replace
	// FirstChar/Widths. The caller must NOT set Type0 for an embedded-CMap
	// /Encoding stream or a predefined CJK CMap -- return ok=false instead:
	// with the CMap undecoded there are no code boundaries to advance by.
	// Both that deferral and rendering Identity-V with horizontal metrics are
	// population-based: the epic's census puts Type0-without-/ToUnicode (the
	// only class that can carry predefined CJK CMaps) at 0.65% of shown font
	// dicts, and the corpus is US-government English with no consumer of
	// vertical layout (byb-8b9, pdftotext-scope spec sections 3-4).
	Type0 bool
	// W and DW mirror the descendant CIDFont's /W -- flattened by the caller
	// to CID -> width in thousandths -- and /DW (ISO 32000-1 9.7.4.3). A CID
	// in neither takes DW's spec default 1000; a zero DW means an absent one
	// (an explicit /DW 0 is indistinguishable and lands on the default too).
	W  map[uint32]float64
	DW float64
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
	t1    *t1Font // stage 4e: classic Type 1; nil means fnt (or nothing)
	upem  float64
	gwork []int64 // per-GID gated charstring work (CFF only); fillGID charges it
	buf   sfnt.Buffer
	first int
	width []float64
	// Composite state (byb-8b9.8): type0/wmap/dw mirror Font.Type0/W/DW, and
	// cid2gid is the CID-keyed CFF's charset-as-CID-map (nil for any other
	// program: such a Type0 font advances but never inks; see fillCIDGlyph).
	type0   bool
	cid2gid map[uint16]uint16
	wmap    map[uint32]float64
	dw      float64
}

// fontProg is one parsed font program. resolveFont caches it by content hash
// so a resource dict aliasing one program under many Tf names pays the parse
// -- and above all the pre-Parse work gates, up to ~256 x maxCharstringWork
// walk steps for a hostile CFF -- once per program, not once per name.
type fontProg struct {
	fnt     *sfnt.Font
	t1      *t1Font
	upem    float64
	gwork   []int64
	cid2gid map[uint16]uint16
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
	program := src.Program
	var key [sha256.Size]byte
	if len(program) == 0 {
		// Stage 4f: no embedded program -- substitute a bundled face by name,
		// else by descriptor flags (substitute.go). The face PATH is the cache
		// key (the prefix keeps an embedded program from aliasing it), so the
		// ~400 KB embed copy and hash happen once per face, not once per Tf
		// name; the file is read only on a miss. The deferred faces leave
		// program empty and degrade to widths-only below, like any program no
		// stage parses.
		face := substituteFace(src.BaseFont, src.Flags)
		key = sha256.Sum256([]byte("substitute:" + face))
		if _, hit := r.progsBy[key]; !hit && face != "" {
			if b, err := substituteFonts.ReadFile(face); err == nil {
				program = b
			}
		}
	} else {
		key = sha256.Sum256(program)
	}
	prog, hit := r.progsBy[key]
	if !hit {
		prog = &fontProg{}
		// Refuse before allocating, and before sfnt.Parse: Parse itself loads
		// whole glyphs ('x' and 'H', the OS/2 metrics fallback), so a hostile
		// compound bomb must be caught FIRST -- see glyf.go. A program whose
		// glyphs cannot all be bounded degrades to widths-only exactly like an
		// unparsable one: a collection, malformed tables, or an expansion past
		// the path budget. Past that gate, sfnt.Parse bounds its own remaining
		// work against the hostile shapes it was fuzzed for, and a parse
		// failure degrades the same way, never an error.
		parse := func(program []byte) {
			if f, err := sfnt.Parse(program); err == nil {
				prog.fnt = f
				prog.upem = float64(f.UnitsPerEm())
			}
		}
		if g := parseGlyfIndex(program); g != nil {
			if g.boundedBy(maxPathPoints) {
				parse(program)
			}
		} else if otf, gwork, cid2gid := cffToSFNT(program); otf != nil {
			// Stage 4d: a bare CFF (/FontFile3 /Subtype /Type1C), gated and
			// wrapped by cff.go into the container sfnt parses. cid2gid is
			// non-nil only for a CID-keyed CFF (byb-8b9.8).
			parse(otf)
			prog.gwork = gwork
			prog.cid2gid = cid2gid
		} else {
			// Stage 4e: a classic Type 1 (/FontFile, PFB or raw). Anything
			// none of the three stages parse stays widths-only.
			prog.t1 = parseType1(program)
		}
		if r.progsBy == nil {
			r.progsBy = map[[sha256.Size]byte]*fontProg{}
		}
		r.progsBy[key] = prog
	}
	tf := &textFont{fnt: prog.fnt, t1: prog.t1, upem: prog.upem, gwork: prog.gwork,
		first: src.FirstChar, width: src.Widths,
		type0: src.Type0, cid2gid: prog.cid2gid, wmap: src.W, dw: src.DW}
	r.fontsBy[name] = tf
	return tf
}

// w0 is the code's glyph displacement in text space (ISO 32000-1 9.4.4):
// /Widths in thousandths where the array covers the code, else /MissingWidth,
// whose default is 0. Deliberately NOT the font program's own advance where
// any /Widths exist: the PDF widths override it by spec, and
// TestTextWidthsOverrideFontAdvance pins that. A dict with NO /Widths at all
// -- the standard-14 shape, where the viewer is expected to know the metrics
// (9.6.2.2) -- takes the resolved program's own advance instead (stage 4f:
// for a substituted face those ARE the metric-compatible widths).
func (f *textFont) w0(code byte) float64 {
	if i := int(code) - f.first; i >= 0 && i < len(f.width) {
		return f.width[i] / 1000
	}
	if len(f.width) == 0 && f.t1 != nil {
		// Stage 4e: the charstring's own hsbw width, in font units.
		return f.t1.advance(code) / f.t1.upem
	}
	if len(f.width) == 0 && f.fnt != nil {
		if gi, err := f.fnt.GlyphIndex(&f.buf, codeRune(code)); err == nil && gi != 0 {
			// ppem = upem returns the advance in raw font units, the same
			// convention fillGlyph uses for LoadGlyph.
			if adv, err := f.fnt.GlyphAdvance(&f.buf, gi, fixed.Int26_6(f.upem), font.HintingNone); err == nil {
				return float64(adv) / f.upem
			}
		}
	}
	return 0
}

// w0CID is the CID's glyph displacement in text space: /W in thousandths
// where present, else /DW, whose absence -- a zero dw -- means the spec
// default 1000 (ISO 32000-1 9.7.4.3).
func (f *textFont) w0CID(cid uint16) float64 {
	if w, ok := f.wmap[uint32(cid)]; ok {
		return w / 1000
	}
	if f.dw != 0 {
		return f.dw / 1000
	}
	return 1
}

// winAnsiC1 maps codes 0x80..0x9f -- where WinAnsi (CP1252) departs from
// Latin-1's C1 controls -- to their runes; 0 for the five undefined slots.
var winAnsiC1 = [32]rune{
	0x20ac, 0, 0x201a, 0x0192, 0x201e, 0x2026, 0x2020, 0x2021,
	0x02c6, 0x2030, 0x0160, 0x2039, 0x0152, 0, 0x017d, 0,
	0, 0x2018, 0x2019, 0x201c, 0x201d, 0x2022, 0x2013, 0x2014,
	0x02dc, 0x2122, 0x0161, 0x203a, 0x0153, 0, 0x017e, 0x0178,
}

// codeRune maps a single-byte code to a rune: Latin-1, which agrees with
// WinAnsi/Standard over ASCII, except that 0x80..0x9f take their WinAnsi
// meanings (smart quotes, dashes, euro) -- as Latin-1 C1 controls they map to
// NO glyph and NO advance, collapsing the rest of the line onto one point. A
// full /Encoding-aware mapping still waits for a caller that can supply one.
func codeRune(code byte) rune {
	if code >= 0x80 && code <= 0x9f {
		if r := winAnsiC1[code-0x80]; r != 0 {
			return r
		}
	}
	return rune(code)
}

// inksText reports whether rendering mode tr deposits ink THIS stage can
// deposit: the filling modes 0, 2, 4 and 6 (ISO 32000-1 table 106). The
// stroke-only modes 1 and 5 and the clip-only mode 7 are recorded here as
// DEFERRED -- their glyphs advance but do not mark, and modes 4-7 install no
// clip -- along with mode 3, which is invisible by definition. A mode outside
// the table marks nothing.
func inksText(tr int) bool { return tr == 0 || tr == 2 || tr == 4 || tr == 6 }

// showText shows one string operand's codes: for each code, ink the glyph
// under the text rendering matrix, then advance the text matrix by the PDF
// width plus spacing (ISO 32000-1 9.4.4). A simple font's codes are the
// bytes; a Type0 font's are 2-byte big-endian CIDs (byb-8b9.8).
func (r *renderer) showText(gs *gstate, raw []byte) error {
	f := gs.font
	if f == nil {
		return nil
	}
	ink := inksText(gs.tr)
	if f.type0 {
		// The Identity CMap: the code IS the CID (9.7.6.2). Word spacing
		// never applies -- it is defined for the SINGLE-byte code 32 only
		// (9.3.3) -- and a dangling odd byte has no code to belong to, so it
		// is dropped.
		for i := 0; i+1 < len(raw); i += 2 {
			cid := uint16(raw[i])<<8 | uint16(raw[i+1])
			if ink {
				if err := r.fillCIDGlyph(gs, f, cid); err != nil {
					return err
				}
			}
			tx := gs.fontSize*f.w0CID(cid) + gs.charSp
			gs.tm = content.Matrix{1, 0, 0, 1, tx * gs.hscale, 0}.Mul(gs.tm)
		}
		return nil
	}
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

// glyphMatrix maps glyph coordinates (font units) to device space: scale by
// 1/unitsPerEm into text space, then the text-space parameters, the text
// matrix and the CTM (Trm, ISO 32000-1 9.4.4). ySign is -1 for sfnt glyphs
// (font units y down, LoadGlyph's documented orientation) and +1 for Type 1
// charstring space, which is y up already.
func glyphMatrix(gs *gstate, upem, ySign float64) content.Matrix {
	params := content.Matrix{gs.fontSize * gs.hscale, 0, 0, gs.fontSize, 0, gs.rise}
	return content.Matrix{1 / upem, 0, 0, ySign / upem, 0, 0}.
		Mul(params).Mul(gs.tm).Mul(gs.ctm)
}

// chargeFill charges n units against the per-Page fill budget.
func (r *renderer) chargeFill(n int64) error {
	r.fillWork += n
	if r.fillWork > maxFillWork {
		return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
	}
	return nil
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
	if f.t1 != nil {
		// Stage 4e: Type 1 outlines go through their own interpreter (and its
		// budgets) into the same path machinery; see type1.go.
		return r.fillT1Glyph(gs, f, code)
	}
	if f.fnt == nil {
		return nil
	}
	gi, err := f.fnt.GlyphIndex(&f.buf, codeRune(code))
	if err != nil || gi == 0 {
		return nil
	}
	return r.fillGID(gs, f, gi)
}

// fillCIDGlyph inks one CID's glyph: CID to GID through the CID-keyed CFF's
// charset (cid2gid), then the shared GID path. A Type0 font whose program is
// anything else has NO cid2gid and inks nothing -- deliberately, not just
// unimplemented: that program's glyphs were work-gated for the 256
// single-byte codes alone, so letting 2-byte codes index them (CID as GID)
// would hand sfnt charstrings the gate never walked.
func (r *renderer) fillCIDGlyph(gs *gstate, f *textFont, cid uint16) error {
	if f.fnt == nil || f.cid2gid == nil {
		return nil
	}
	gid := f.cid2gid[cid]
	if gid == 0 {
		return nil
	}
	return r.fillGID(gs, f, sfnt.GlyphIndex(gid))
}

// fillGID loads and fills one glyph by index -- the shared back half of
// fillGlyph and fillCIDGlyph.
func (r *renderer) fillGID(gs *gstate, f *textFont, gi sfnt.GlyphIndex) error {
	// Stage 4d: charge the charstring work LoadGlyph is about to execute
	// (measured by cff.go's gate) BEFORE executing it. A glyph can run ~64 KB
	// of charstring yet emit zero segments (an hstem sled), so the per-point
	// charge below would price its re-shows at nothing -- one Tj per
	// content-stream byte re-running the interpreter forever. Charging the
	// gate total per SHOW keeps interpreter time under maxFillWork per Page,
	// exactly like scanline time.
	if int(gi) < len(f.gwork) {
		if err := r.chargeFill(f.gwork[gi]); err != nil {
			return err
		}
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
	m := glyphMatrix(gs, f.upem, -1)
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
	if err := r.chargeFill(int64(pathPoints(pth.subs))); err != nil {
		return err
	}
	return r.fillSubpaths(pth.subs, false, r.deviceClip(gs.clip), gs.fill.rgba)
}
