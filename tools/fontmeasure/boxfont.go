//go:build ignore

// Command boxfont builds a TrueType font whose every glyph is a rectangle at
// the correct Helvetica advance width. It is arm (b) of the byb-8b9.6
// measurement: "synthesise from metrics only".
//
// Derived from internal/glyphless/gen.go, which already emits a valid sfnt
// with the Helvetica width table. The only substantive change is
// buildLocaAndGlyf, which here emits a real outline instead of an empty one,
// plus the head/maxp fields that must agree with a non-empty glyf.
//
// Usage: boxfont -style=filled|hollow -top=520 -bottom=0 -inset=0.10 -out=f.ttf
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"
)

const (
	firstRune   = ' '
	lastRune    = '~'
	unitsPerEm  = 1000
	numGlyphs   = lastRune - firstRune + 2
	notdefWidth = 0
)

// widths is the Helvetica metric set, copied from internal/glyphless/gen.go.
// Arm (b) must carry the SAME advances as arm (a), or the comparison measures
// layout rather than glyph shape.
var widths = [lastRune - firstRune + 1]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

var (
	style  = flag.String("style", "filled", "filled | hollow")
	top    = flag.Int("top", 520, "box top in font units (520 ~ Helvetica x-height)")
	bottom = flag.Int("bottom", 0, "box bottom in font units")
	inset  = flag.Float64("inset", 0.10, "side bearing as a fraction of the advance")
	stroke = flag.Int("stroke", 60, "hollow wall thickness in font units")
	family = flag.String("family", "Byblos Box", "name table family")
	out    = flag.String("out", "boxfont.ttf", "output path")
)

func be16(buf []byte, v uint16) []byte { return append(buf, byte(v>>8), byte(v)) }
func bei16(buf []byte, v int16) []byte { return be16(buf, uint16(v)) }
func be32(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// boxFor returns the rectangle drawn for a glyph of the given advance width.
// A space (advance 278 at index 0) draws nothing, exactly as a real face does:
// if the synthesised page inked its spaces it would be trivially
// distinguishable and the measurement would be rigged.
func boxFor(adv uint16, isSpace bool) (x0, y0, x1, y1 int16, blank bool) {
	if isSpace || adv == 0 {
		return 0, 0, 0, 0, true
	}
	pad := int16(float64(adv) * *inset)
	x0, x1 = pad, int16(adv)-pad
	y0, y1 = int16(*bottom), int16(*top)
	if x1 <= x0 || y1 <= y0 {
		return 0, 0, 0, 0, true
	}
	return x0, y0, x1, y1, false
}

// rect appends one closed 4-point contour. Every point is on-curve and both
// deltas are int16, which is flag 0x01 alone -- the simplest legal encoding
// (OpenType spec, "Simple Glyph Description").
func rect(flags, xs, ys []byte, px, py *int16, x0, y0, x1, y1 int16, reverse bool) ([]byte, []byte, []byte) {
	pts := [][2]int16{{x0, y0}, {x1, y0}, {x1, y1}, {x0, y1}}
	if reverse {
		pts = [][2]int16{{x0, y0}, {x0, y1}, {x1, y1}, {x1, y0}}
	}
	for _, p := range pts {
		flags = append(flags, 0x01)
		xs = bei16(xs, p[0]-*px)
		ys = bei16(ys, p[1]-*py)
		*px, *py = p[0], p[1]
	}
	return flags, xs, ys
}

func buildGlyph(adv uint16, isSpace bool) []byte {
	x0, y0, x1, y1, blank := boxFor(adv, isSpace)
	if blank {
		return nil // empty outline: loca[i] == loca[i+1]
	}
	contours := 1
	var ends []byte
	var flags, xs, ys []byte
	var px, py int16
	ends = be16(ends, 3)
	flags, xs, ys = rect(flags, xs, ys, &px, &py, x0, y0, x1, y1, false)
	if *style == "hollow" {
		s := int16(*stroke)
		i0, j0, i1, j1 := x0+s, y0+s, x1-s, y1-s
		if i1 > i0 && j1 > j0 {
			contours = 2
			ends = be16(ends, 7)
			// opposite winding, so non-zero fill leaves the interior white
			flags, xs, ys = rect(flags, xs, ys, &px, &py, i0, j0, i1, j1, true)
		}
	}
	var b []byte
	b = bei16(b, int16(contours))
	b = bei16(b, x0)
	b = bei16(b, y0)
	b = bei16(b, x1)
	b = bei16(b, y1)
	b = append(b, ends...)
	b = be16(b, 0) // instructionLength
	b = append(b, flags...)
	b = append(b, xs...)
	b = append(b, ys...)
	return b
}

// buildLocaAndGlyf uses the LONG loca format. Short loca stores offset/2 and
// would silently corrupt any odd-length glyph; long loca removes that class of
// bug entirely at the cost of 2 bytes per glyph.
func buildLocaAndGlyf() (loca, glyf []byte, maxPts, maxCont uint16) {
	loca = be32(loca, 0)
	for g := 0; g < numGlyphs; g++ {
		if g > 0 {
			r := rune(firstRune + g - 1)
			adv := widths[g-1]
			gl := buildGlyph(adv, r == ' ')
			if gl != nil {
				n := uint16(4)
				c := uint16(1)
				if *style == "hollow" && len(gl) > 0 && int16(binary.BigEndian.Uint16(gl[0:2])) == 2 {
					n, c = 8, 2
				}
				if n > maxPts {
					maxPts = n
				}
				if c > maxCont {
					maxCont = c
				}
			}
			glyf = append(glyf, gl...)
			for len(glyf)%4 != 0 {
				glyf = append(glyf, 0)
			}
		}
		loca = be32(loca, uint32(len(glyf)))
	}
	return loca, glyf, maxPts, maxCont
}

func buildHead(xMin, yMin, xMax, yMax int16) []byte {
	var b []byte
	b = be32(b, 0x00010000)
	b = be32(b, 0x00010000)
	b = be32(b, 0) // checkSumAdjustment, patched at the end
	b = be32(b, 0x5F0F3CF5)
	b = be16(b, 0x0003)
	b = be16(b, unitsPerEm)
	epoch1904 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() + 2082844800
	b = append(b, 0, 0, 0, 0, byte(epoch1904>>24), byte(epoch1904>>16), byte(epoch1904>>8), byte(epoch1904))
	b = append(b, 0, 0, 0, 0, byte(epoch1904>>24), byte(epoch1904>>16), byte(epoch1904>>8), byte(epoch1904))
	b = bei16(b, xMin)
	b = bei16(b, yMin)
	b = bei16(b, xMax)
	b = bei16(b, yMax)
	b = be16(b, 0)
	b = be16(b, 8)
	b = be16(b, 2)
	b = be16(b, 1) // indexToLocFormat: 1 = long
	b = be16(b, 0)
	return b
}

func buildHhea() []byte {
	var b []byte
	b = be32(b, 0x00010000)
	b = be16(b, 800)
	b = bei16(b, -200)
	b = be16(b, 0)
	max := uint16(0)
	for _, w := range widths {
		if w > max {
			max = w
		}
	}
	b = be16(b, max)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, uint16(max))
	b = be16(b, 1)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, numGlyphs)
	return b
}

func buildMaxp(maxPts, maxCont uint16) []byte {
	var b []byte
	b = be32(b, 0x00010000)
	b = be16(b, numGlyphs)
	b = be16(b, maxPts)
	b = be16(b, maxCont)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 2)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	return b
}

func buildHmtx() []byte {
	var b []byte
	b = be16(b, notdefWidth)
	b = be16(b, 0)
	for _, w := range widths {
		b = be16(b, w)
		b = be16(b, 0)
	}
	return b
}

func buildCmap() []byte {
	segCount := 2
	var sub []byte
	sub = be16(sub, 4)
	sub = be16(sub, 0)
	sub = be16(sub, 0)
	sub = be16(sub, uint16(segCount*2))
	entrySelector := uint16(0)
	for (1 << (entrySelector + 1)) <= segCount {
		entrySelector++
	}
	searchRange := uint16(1<<entrySelector) * 2
	sub = be16(sub, searchRange)
	sub = be16(sub, entrySelector)
	sub = be16(sub, uint16(segCount*2)-searchRange)
	sub = be16(sub, lastRune)
	sub = be16(sub, 0xFFFF)
	sub = be16(sub, 0)
	sub = be16(sub, firstRune)
	sub = be16(sub, 0xFFFF)
	idDelta0 := int16(1 - firstRune)
	sub = be16(sub, uint16(idDelta0))
	sub = be16(sub, 1)
	sub = be16(sub, 0)
	sub = be16(sub, 0)
	binary.BigEndian.PutUint16(sub[2:4], uint16(len(sub)))

	var tbl []byte
	tbl = be16(tbl, 0)
	tbl = be16(tbl, 1)
	tbl = be16(tbl, 3)
	tbl = be16(tbl, 1)
	tbl = be32(tbl, uint32(len(tbl)+4))
	tbl = append(tbl, sub...)
	return tbl
}

func buildPost() []byte {
	var b []byte
	b = be32(b, 0x00030000)
	for i := 0; i < 7; i++ {
		b = be32(b, 0)
	}
	return b
}

// buildOS2 is not in glyphless.go. fontconfig indexes a font through FreeType
// and wants OS/2 for weight, width and the Unicode range bits; without it the
// font can be scanned but ranks oddly against other candidates. Version 4 is
// the last version whose field list is fixed.
func buildOS2() []byte {
	var b []byte
	b = be16(b, 4)      // version
	b = bei16(b, 500)   // xAvgCharWidth
	b = be16(b, 400)    // usWeightClass: normal
	b = be16(b, 5)      // usWidthClass: medium
	b = be16(b, 0)      // fsType: installable
	for i := 0; i < 5; i++ { // subscript/superscript metrics
		b = bei16(b, 0)
		b = bei16(b, 0)
	}
	b = bei16(b, 50)  // yStrikeoutSize
	b = bei16(b, 250) // yStrikeoutPosition
	b = bei16(b, 0)   // sFamilyClass
	b = append(b, make([]byte, 10)...) // PANOSE
	b = be32(b, 0x00000003)            // ulUnicodeRange1: Basic Latin + Latin-1
	b = be32(b, 0)
	b = be32(b, 0)
	b = be32(b, 0)
	b = append(b, []byte("BYBL")...) // achVendID
	b = be16(b, 0x0040)              // fsSelection: REGULAR
	b = be16(b, firstRune)           // usFirstCharIndex
	b = be16(b, lastRune)            // usLastCharIndex
	b = bei16(b, 800)                // sTypoAscender
	b = bei16(b, -200)               // sTypoDescender
	b = bei16(b, 0)                  // sTypoLineGap
	b = be16(b, 800)                 // usWinAscent
	b = be16(b, 200)                 // usWinDescent
	b = be32(b, 1)                   // ulCodePageRange1: Latin-1
	b = be32(b, 0)
	b = bei16(b, 520) // sxHeight
	b = bei16(b, 700) // sCapHeight
	b = be16(b, 0)    // usDefaultChar
	b = be16(b, 32)   // usBreakChar
	b = be16(b, 1)    // usMaxContext
	return b
}

func utf16be(s string) []byte {
	var b []byte
	for _, r := range s {
		b = be16(b, uint16(r))
	}
	return b
}

func buildName(fam string) []byte {
	ps := ""
	for _, r := range fam {
		if r != ' ' {
			ps += string(r)
		}
	}
	recs := []struct {
		id int
		s  string
	}{
		{1, fam},
		{2, "Regular"},
		{3, fam + " 1.0;" + ps},
		{4, fam},
		{6, ps},
	}
	var strs, dir []byte
	for _, r := range recs {
		enc := utf16be(r.s)
		dir = be16(dir, 3)
		dir = be16(dir, 1)
		dir = be16(dir, 0x0409)
		dir = be16(dir, uint16(r.id))
		dir = be16(dir, uint16(len(enc)))
		dir = be16(dir, uint16(len(strs)))
		strs = append(strs, enc...)
	}
	var tbl []byte
	tbl = be16(tbl, 0)
	tbl = be16(tbl, uint16(len(recs)))
	tbl = be16(tbl, uint16(6+len(dir)))
	tbl = append(tbl, dir...)
	tbl = append(tbl, strs...)
	return tbl
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func checksum(b []byte) uint32 {
	p := pad4(append([]byte(nil), b...))
	var sum uint32
	for i := 0; i < len(p); i += 4 {
		sum += binary.BigEndian.Uint32(p[i : i+4])
	}
	return sum
}

func main() {
	flag.Parse()
	if *style != "filled" && *style != "hollow" {
		fmt.Fprintln(os.Stderr, "boxfont: -style must be filled or hollow")
		os.Exit(2)
	}
	loca, glyf, maxPts, maxCont := buildLocaAndGlyf()

	var xMin, yMin, xMax, yMax int16
	for i, w := range widths {
		x0, y0, x1, y1, blank := boxFor(w, rune(firstRune+i) == ' ')
		if blank {
			continue
		}
		if x0 < xMin {
			xMin = x0
		}
		if y0 < yMin {
			yMin = y0
		}
		if x1 > xMax {
			xMax = x1
		}
		if y1 > yMax {
			yMax = y1
		}
	}

	tables := map[string][]byte{
		"OS/2": buildOS2(),
		"cmap": buildCmap(),
		"glyf": glyf,
		"head": buildHead(xMin, yMin, xMax, yMax),
		"hhea": buildHhea(),
		"hmtx": buildHmtx(),
		"loca": loca,
		"maxp": buildMaxp(maxPts, maxCont),
		"name": buildName(*family),
		"post": buildPost(),
	}
	tags := []string{"OS/2", "cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"}

	numTables := uint16(len(tags))
	entrySelector := uint16(0)
	for (1 << (entrySelector + 1)) <= int(numTables) {
		entrySelector++
	}
	searchRange := uint16(1<<entrySelector) * 16

	var o []byte
	o = be32(o, 0x00010000)
	o = be16(o, numTables)
	o = be16(o, searchRange)
	o = be16(o, entrySelector)
	o = be16(o, numTables*16-searchRange)

	dirStart := len(o)
	o = append(o, make([]byte, int(numTables)*16)...)

	headDirOffset := -1
	for i, tag := range tags {
		data := tables[tag]
		off := uint32(len(o))
		cs := checksum(data)
		e := dirStart + i*16
		copy(o[e:e+4], tag)
		binary.BigEndian.PutUint32(o[e+4:e+8], cs)
		binary.BigEndian.PutUint32(o[e+8:e+12], off)
		binary.BigEndian.PutUint32(o[e+12:e+16], uint32(len(data)))
		if tag == "head" {
			headDirOffset = int(off)
		}
		o = append(o, data...)
		o = pad4(o)
	}
	if len(o)%4 != 0 {
		panic("boxfont: font length is not a multiple of 4 after padding")
	}
	binary.BigEndian.PutUint32(o[headDirOffset+8:headDirOffset+12], uint32(0xB1B0AFBA)-checksum(o))

	if err := os.WriteFile(*out, o, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "boxfont:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s (%s), %d bytes, maxPoints=%d maxContours=%d\n", *out, *style, len(o), maxPts, maxCont)
}
