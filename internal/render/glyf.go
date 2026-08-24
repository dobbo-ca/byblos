package render

// Stage 4c (byb-8b9.3): pre-flight bounding of TrueType glyph expansion.
//
// sfnt materialises a compound glyph's WHOLE expansion when it loads one: its
// own limits (maxCompoundRecursionDepth = 8, stack 64) still admit ~9^7
// component instances, so a sub-KB hostile /FontFile2 buys seconds of CPU and
// gigabytes of heap before any per-point charge sees a segment. And the first
// load is not fillGlyph's: sfnt.Parse ITSELF loads the glyphs for 'x' and 'H'
// (the OS/2-table-below-version-2 metrics fallback, initOS2VersionBelow2), so
// a bomb mapped to either explodes inside Parse. The package rule is
// refuse-before-allocating, so resolveFont bounds EVERY glyph's expansion
// here, from the same raw glyf tree sfnt will read, before calling Parse.

import "encoding/binary"

var be = binary.BigEndian

// glyfIndex indexes the raw loca/glyf tables of a font program sfnt.Parse
// accepted, so a glyph's expanded point count can be bounded before sfnt
// materialises the outline.
type glyfIndex struct {
	glyf, loca []byte
	long       bool // head indexToLocFormat 1: 4-byte loca entries
	numGlyphs  int
}

// parseGlyfIndex locates head/maxp/loca/glyf in a raw sfnt program. nil means
// no glyph's expansion can be bounded -- a CFF flavour (OTTO, stages 4d-4f), a
// collection, or tables this parser cannot index -- and the caller must treat
// the program as unusable rather than render unbudgeted outlines.
func parseGlyfIndex(p []byte) *glyfIndex {
	if len(p) < 12 {
		return nil
	}
	switch be.Uint32(p) {
	case 0x00010000, 0x74727565: // the TrueType flavours ("true" is Apple's)
	default:
		return nil
	}
	var head, maxp, loca, glyf []byte
	n := int(be.Uint16(p[4:6]))
	for i := 0; i < n; i++ {
		e := 12 + i*16
		if e+16 > len(p) {
			return nil
		}
		off, ln := int64(be.Uint32(p[e+8:])), int64(be.Uint32(p[e+12:]))
		if off+ln > int64(len(p)) {
			continue // sfnt never read this table either, or it failed Parse
		}
		t := p[off : off+ln]
		switch string(p[e : e+4]) {
		case "head":
			head = t
		case "maxp":
			maxp = t
		case "loca":
			loca = t
		case "glyf":
			glyf = t
		}
	}
	if len(head) < 52 || len(maxp) < 6 || loca == nil || glyf == nil {
		return nil
	}
	g := &glyfIndex{
		glyf:      glyf,
		loca:      loca,
		long:      be.Uint16(head[50:52]) == 1,
		numGlyphs: int(be.Uint16(maxp[4:6])),
	}
	need := (g.numGlyphs + 1) * 2
	if g.long {
		need *= 2
	}
	if len(loca) < need {
		return nil
	}
	return g
}

// glyphData returns glyph gi's slice of the glyf table, or nil when the glyph
// is empty, out of range, or its loca entries are malformed -- every case
// where sfnt loads nothing (or errors, and fillGlyph skips), so nil costs the
// budget nothing.
func (g *glyfIndex) glyphData(gi int) []byte {
	if gi < 0 || gi >= g.numGlyphs {
		return nil
	}
	var o1, o2 int64
	if g.long {
		o1 = int64(be.Uint32(g.loca[gi*4:]))
		o2 = int64(be.Uint32(g.loca[gi*4+4:]))
	} else {
		o1 = 2 * int64(be.Uint16(g.loca[gi*2:]))
		o2 = 2 * int64(be.Uint16(g.loca[gi*2+2:]))
	}
	if o2 <= o1 || o2 > int64(len(g.glyf)) {
		return nil
	}
	return g.glyf[o1:o2]
}

// boundedBy reports whether EVERY glyph's expanded outline stays within limit
// points. The count charges every on-disk point of every component INSTANCE
// plus one per component record, so both a point bomb and an all-empty
// compound bomb (which still costs sfnt a leaf load per instance) are caught.
// Counts are memoised across the component DAG and capped at limit+1, so the
// whole check is O(len(glyf)) however the components alias. Two deliberate
// overcounts, both in the refusing (safe) direction: sfnt's depth-8 cap is
// ignored (a deeper-than-8 legal font degrades to widths-only), and a
// component cycle -- which sfnt would refuse glyph-by-glyph -- refuses the
// font. Component layout follows OpenType glyf.
func (g *glyfIndex) boundedBy(limit int64) bool {
	memo := make([]int64, g.numGlyphs) // 0 unvisited, -1 in progress, else count+1
	var count func(gi int) int64
	count = func(gi int) int64 {
		if gi < 0 || gi >= g.numGlyphs {
			return 0
		}
		switch m := memo[gi]; {
		case m == -1:
			return limit + 1 // cycle
		case m > 0:
			return m - 1
		}
		memo[gi] = -1
		n := int64(0)
		d := g.glyphData(gi)
		if len(d) >= 10 {
			if nc := int16(be.Uint16(d)); nc >= 0 {
				// Simple glyph: point count = last end-of-contour index + 1.
				if end := 10 + int(nc)*2; nc > 0 && len(d) >= end {
					n = int64(be.Uint16(d[end-2:])) + 1
				}
			} else {
				for off := 10; off+4 <= len(d); {
					flags := int(be.Uint16(d[off:]))
					child := int(be.Uint16(d[off+2:]))
					n += 1 + count(child)
					if n > limit {
						n = limit + 1
						break
					}
					off += 8
					if flags&0x0001 == 0 { // args are bytes, not words
						off -= 2
					}
					switch {
					case flags&0x0008 != 0: // WE_HAVE_A_SCALE
						off += 2
					case flags&0x0040 != 0: // WE_HAVE_AN_X_AND_Y_SCALE
						off += 4
					case flags&0x0080 != 0: // WE_HAVE_A_TWO_BY_TWO
						off += 8
					}
					if flags&0x0020 == 0 { // no MORE_COMPONENTS
						break
					}
				}
			}
		}
		memo[gi] = n + 1
		return n
	}
	for gi := 0; gi < g.numGlyphs; gi++ {
		if count(gi) > limit {
			return false
		}
	}
	return true
}
