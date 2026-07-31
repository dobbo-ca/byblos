//go:build ignore

// Command gen builds internal/glyphless/glyphless.ttf: a minimal TrueType
// font, sfnt-wrapped, with every glyph outline empty (loca[i] == loca[i+1]
// for all i, glyf table zero bytes) and an hmtx table giving each covered
// rune a real proportional advance. Manual step; run `make glyphless`. Never
// run in CI: the committed .ttf is the asset glyphless.go embeds, not
// something rebuilt on every commit -- and glyphless.go's own go:embed
// directive is exactly why this file cannot import package glyphless to
// reuse its constants: the package fails to compile until glyphless.ttf
// already exists. See the widths duplication note below and the identical
// constraint on testdata/oracle/gen.go's pixelHash.
package main

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// firstRune, lastRune, unitsPerEm mirror glyphless.go's FirstRune, LastRune,
// UnitsPerEm. Keep them identical.
const (
	firstRune   = ' '
	lastRune    = '~'
	unitsPerEm  = 1000
	numGlyphs   = lastRune - firstRune + 2 // +1 for the covered range, +1 for .notdef
	notdefWidth = 0                        // glyph 0 is never addressed by GlyphID
)

// widths mirrors glyphless.go's widths exactly -- see the duplication note in
// this file's header comment and in glyphless.go.
var widths = [lastRune - firstRune + 1]uint16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // ' ' .. '/'
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // '0' .. '?'
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // '@' .. 'O'
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 'P' .. '_'
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // '`' .. 'o'
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 'p' .. '~'
}

// be16/be32 append a big-endian value. Every sfnt field is big-endian
// (OpenType spec, "Data Types").
func be16(buf []byte, v uint16) []byte { return append(buf, byte(v>>8), byte(v)) }
func be32(buf []byte, v uint32) []byte {
	return append(buf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

func buildHead() []byte {
	var b []byte
	b = be32(b, 0x00010000) // version
	b = be32(b, 0x00010000) // fontRevision
	b = be32(b, 0)          // checkSumAdjustment, patched once the whole font is assembled
	b = be32(b, 0x5F0F3CF5) // magicNumber
	b = be16(b, 0x0003)     // flags: baseline at y=0, left sidebearing at x=0
	b = be16(b, unitsPerEm)
	// created/modified: LONGDATETIME, seconds since 1904-01-01 00:00:00. Fixed
	// rather than time.Now(), so regenerating without a width/coverage change
	// reproduces byte-identical output -- there would otherwise be no way to
	// tell "the asset changed" from "someone re-ran gen.go" in a diff.
	epoch1904 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix() + 2082844800
	b = append(b, 0, 0, 0, 0, byte(epoch1904>>24), byte(epoch1904>>16), byte(epoch1904>>8), byte(epoch1904))
	b = append(b, 0, 0, 0, 0, byte(epoch1904>>24), byte(epoch1904>>16), byte(epoch1904>>8), byte(epoch1904))
	b = be16(b, 0) // xMin
	b = be16(b, 0) // yMin
	b = be16(b, 0) // xMax
	b = be16(b, 0) // yMax: all zero, every glyph is an empty outline
	b = be16(b, 0) // macStyle
	b = be16(b, 8) // lowestRecPPEM
	b = be16(b, 2) // fontDirectionHint (deprecated; 2 is the spec default)
	b = be16(b, 0) // indexToLocFormat: 0 = short (loca entries fit in uint16 since every offset is 0)
	b = be16(b, 0) // glyphDataFormat
	return b
}

func buildHhea() []byte {
	var b []byte
	b = be32(b, 0x00010000) // version
	b = be16(b, 800)        // ascender
	descender := int16(-200)
	b = be16(b, uint16(descender))
	b = be16(b, 0) // lineGap
	max := uint16(0)
	for _, w := range widths {
		if w > max {
			max = w
		}
	}
	b = be16(b, max) // advanceWidthMax
	b = be16(b, 0)   // minLeftSideBearing
	b = be16(b, 0)   // minRightSideBearing
	b = be16(b, 0)   // xMaxExtent
	b = be16(b, 1)   // caretSlopeRise
	b = be16(b, 0)   // caretSlopeRun
	b = be16(b, 0)   // caretOffset
	b = be16(b, 0)   // reserved x4
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)
	b = be16(b, 0)         // metricDataFormat
	b = be16(b, numGlyphs) // numberOfHMetrics: every glyph gets its own hmtx entry
	return b
}

func buildMaxp() []byte {
	var b []byte
	b = be32(b, 0x00010000) // version 1.0 (required for a glyf-backed font)
	b = be16(b, numGlyphs)
	b = be16(b, 0) // maxPoints: no glyph has any
	b = be16(b, 0) // maxContours
	b = be16(b, 0) // maxCompositePoints
	b = be16(b, 0) // maxCompositeContours
	b = be16(b, 2) // maxZones
	b = be16(b, 0) // maxTwilightPoints
	b = be16(b, 0) // maxStorage
	b = be16(b, 0) // maxFunctionDefs
	b = be16(b, 0) // maxInstructionDefs
	b = be16(b, 0) // maxStackElements
	b = be16(b, 0) // maxSizeOfInstructions
	b = be16(b, 0) // maxComponentElements
	b = be16(b, 0) // maxComponentDepth
	return b
}

func buildHmtx() []byte {
	var b []byte
	b = be16(b, notdefWidth) // glyph 0, .notdef: never shown, GlyphID never returns it
	b = be16(b, 0)           // lsb
	for _, w := range widths {
		b = be16(b, w)
		b = be16(b, 0) // lsb: irrelevant with an empty outline, kept at 0
	}
	return b
}

// buildLocaAndGlyf returns loca (short format) and glyf. Every glyph is
// empty, so every loca entry is 0 and glyf is zero bytes -- that pairing is
// exactly what "this glyph has no outline" means (OpenType spec, "loca").
func buildLocaAndGlyf() (loca, glyf []byte) {
	for i := 0; i <= numGlyphs; i++ {
		loca = be16(loca, 0)
	}
	return loca, nil
}

// buildCmap writes one (3,1) Windows-Unicode-BMP format-4 subtable mapping
// firstRune..lastRune to glyphs 1..numGlyphs-1 in order, plus the format-4
// terminator segment 0xFFFF the spec requires. A single idDelta covers the
// whole covered range because glyph ids were assigned in rune order for
// exactly this reason: no glyphIdArray indirection needed.
func buildCmap() []byte {
	segCount := 2
	var sub []byte
	sub = be16(sub, 4) // format
	// length, patched below once known
	sub = be16(sub, 0)
	sub = be16(sub, 0) // language
	sub = be16(sub, uint16(segCount*2))
	entrySelector := uint16(0)
	for (1 << (entrySelector + 1)) <= segCount {
		entrySelector++
	}
	searchRange := uint16(1<<entrySelector) * 2
	sub = be16(sub, searchRange)
	sub = be16(sub, entrySelector)
	sub = be16(sub, uint16(segCount*2)-searchRange)

	sub = be16(sub, lastRune)  // endCode[0]
	sub = be16(sub, 0xFFFF)    // endCode[1]: required terminator segment
	sub = be16(sub, 0)         // reservedPad
	sub = be16(sub, firstRune) // startCode[0]
	sub = be16(sub, 0xFFFF)    // startCode[1]
	idDelta0 := int16(1 - firstRune)
	sub = be16(sub, uint16(idDelta0)) // idDelta[0]: glyphID(c) = c + idDelta for c in [firstRune,lastRune]
	sub = be16(sub, 1)                // idDelta[1]: 0xFFFF + 1 wraps to 0, i.e. "no glyph"
	sub = be16(sub, 0)                // idRangeOffset[0]: 0 means use idDelta directly, no glyphIdArray
	sub = be16(sub, 0)                // idRangeOffset[1]
	binary.BigEndian.PutUint16(sub[2:4], uint16(len(sub)))

	var tbl []byte
	tbl = be16(tbl, 0) // version
	tbl = be16(tbl, 1) // numTables
	tbl = be16(tbl, 3) // platformID: Windows
	tbl = be16(tbl, 1) // encodingID: Unicode BMP
	tbl = be32(tbl, uint32(len(tbl)+4))
	tbl = append(tbl, sub...)
	return tbl
}

func buildPost() []byte {
	var b []byte
	b = be32(b, 0x00030000) // version 3.0: no glyph names stored
	b = be32(b, 0)          // italicAngle
	b = be16(b, 0)          // underlinePosition
	b = be16(b, 0)          // underlineThickness
	b = be32(b, 0)          // isFixedPitch
	b = be32(b, 0)          // minMemType42
	b = be32(b, 0)          // maxMemType42
	b = be32(b, 0)          // minMemType1
	b = be32(b, 0)          // maxMemType1
	return b
}

func utf16be(s string) []byte {
	var b []byte
	for _, r := range s {
		b = be16(b, uint16(r)) // ASCII input only, single UTF-16 unit per rune
	}
	return b
}

func buildName() []byte {
	type rec struct {
		id int
		s  string
	}
	recs := []rec{
		{1, "Byblos Glyphless"},                      // Family
		{2, "Regular"},                               // Subfamily
		{3, "Byblos Glyphless 1.0;byblos-glyphless"}, // Unique ID
		{4, "Byblos Glyphless"},                      // Full name
		{6, "BbyblosGlyphless"},                      // PostScript name: no spaces allowed
	}
	var strings []byte
	var dir []byte
	for _, r := range recs {
		enc := utf16be(r.s)
		dir = be16(dir, 3)      // platformID: Windows
		dir = be16(dir, 1)      // encodingID: Unicode BMP
		dir = be16(dir, 0x0409) // languageID: en-US
		dir = be16(dir, uint16(r.id))
		dir = be16(dir, uint16(len(enc)))
		dir = be16(dir, uint16(len(strings)))
		strings = append(strings, enc...)
	}
	var tbl []byte
	tbl = be16(tbl, 0) // format
	tbl = be16(tbl, uint16(len(recs)))
	tbl = be16(tbl, uint16(6+len(dir))) // stringOffset
	tbl = append(tbl, dir...)
	tbl = append(tbl, strings...)
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
	loca, glyf := buildLocaAndGlyf()
	tables := map[string][]byte{
		"cmap": buildCmap(),
		"glyf": glyf,
		"head": buildHead(),
		"hhea": buildHhea(),
		"hmtx": buildHmtx(),
		"loca": loca,
		"maxp": buildMaxp(),
		"name": buildName(),
		"post": buildPost(),
	}
	// Table directory order must be ascending by tag (OpenType spec, "Table
	// Directory"); this list already is.
	tags := []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"}

	numTables := uint16(len(tags))
	entrySelector := uint16(0)
	for (1 << (entrySelector + 1)) <= int(numTables) {
		entrySelector++
	}
	searchRange := uint16(1<<entrySelector) * 16

	var out []byte
	out = be32(out, 0x00010000) // scalerType: TrueType outlines
	out = be16(out, numTables)
	out = be16(out, searchRange)
	out = be16(out, entrySelector)
	out = be16(out, numTables*16-searchRange)

	dirStart := len(out)
	out = append(out, make([]byte, int(numTables)*16)...)

	headDirOffset := -1
	for i, tag := range tags {
		data := tables[tag]
		off := uint32(len(out))
		cs := checksum(data)
		entry := dirStart + i*16
		copy(out[entry:entry+4], tag)
		binary.BigEndian.PutUint32(out[entry+4:entry+8], cs)
		binary.BigEndian.PutUint32(out[entry+8:entry+12], off)
		binary.BigEndian.PutUint32(out[entry+12:entry+16], uint32(len(data)))
		if tag == "head" {
			headDirOffset = int(off)
		}
		out = append(out, data...)
		out = pad4(out)
	}

	// Whole-font checksum adjustment (OpenType spec, "head"): with
	// checkSumAdjustment at 0 (already true above -- buildHead wrote 0 and it
	// was never patched before this point), sum the entire file and store
	// 0xB1B0AFBA - sum back into head.
	if len(out)%4 != 0 {
		panic("gen: font length is not a multiple of 4 after padding every table")
	}
	adjustment := uint32(0xB1B0AFBA) - checksum(out)
	binary.BigEndian.PutUint32(out[headDirOffset+8:headDirOffset+12], adjustment)

	if err := os.WriteFile("internal/glyphless/glyphless.ttf", out, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
	fmt.Println("wrote internal/glyphless/glyphless.ttf,", len(out), "bytes")
}
