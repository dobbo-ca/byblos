package glyphless

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// requiredTables is the OpenType "required tables" set for a TrueType glyph
// outline font (spec: "Required Tables"). A font missing any of these is not
// a font a conforming reader will load, whatever its glyf table says.
var requiredTables = []string{"cmap", "glyf", "head", "hhea", "hmtx", "loca", "maxp", "name", "post"}

// sfntTable is a parsed table directory entry plus the table's own bytes.
type sfntTable struct {
	tag      string
	checksum uint32
	offset   uint32
	length   uint32
	data     []byte
}

// parseSFNT reads the offset subtable and table directory of an sfnt-wrapped
// TrueType font. It does not validate individual table contents -- that is
// what the tests below do -- only the container structure every table sits
// inside.
func parseSFNT(t *testing.T, raw []byte) (scalerType uint32, tables map[string]sfntTable) {
	t.Helper()
	if len(raw) < 12 {
		t.Fatalf("font is %d bytes, too short for an sfnt header", len(raw))
	}
	scalerType = binary.BigEndian.Uint32(raw[0:4])
	numTables := binary.BigEndian.Uint16(raw[4:6])
	tables = make(map[string]sfntTable, numTables)
	for i := 0; i < int(numTables); i++ {
		off := 12 + i*16
		if off+16 > len(raw) {
			t.Fatalf("table directory entry %d runs past end of file", i)
		}
		tag := string(raw[off : off+4])
		checksum := binary.BigEndian.Uint32(raw[off+4 : off+8])
		tOff := binary.BigEndian.Uint32(raw[off+8 : off+12])
		tLen := binary.BigEndian.Uint32(raw[off+12 : off+16])
		if int(tOff+tLen) > len(raw) {
			t.Fatalf("table %q claims [%d:%d], font is only %d bytes", tag, tOff, tOff+tLen, len(raw))
		}
		tables[tag] = sfntTable{tag: tag, checksum: checksum, offset: tOff, length: tLen, data: raw[tOff : tOff+tLen]}
	}
	return scalerType, tables
}

// tableChecksum is the OpenType table checksum algorithm: sum the table as
// big-endian uint32 words, zero-padding the final partial word. head's own
// checksum entry is a documented exception (see TestHeadChecksumAdjustment)
// and is not exercised through this helper the same way as the rest.
func tableChecksum(data []byte) uint32 {
	var sum uint32
	for i := 0; i < len(data); i += 4 {
		var word [4]byte
		copy(word[:], data[i:min(i+4, len(data))])
		sum += binary.BigEndian.Uint32(word[:])
	}
	return sum
}

func TestSFNTHeaderAndDirectory(t *testing.T) {
	scalerType, tables := parseSFNT(t, Font)
	if scalerType != 0x00010000 {
		t.Errorf("scalerType = %#08x, want 0x00010000 (TrueType outlines)", scalerType)
	}
	for _, tag := range requiredTables {
		if _, ok := tables[tag]; !ok {
			t.Errorf("required table %q is missing", tag)
		}
	}
}

func TestTableChecksums(t *testing.T) {
	_, tables := parseSFNT(t, Font)
	for tag, tbl := range tables {
		if tag == "head" {
			// head's checkSumAdjustment field (bytes 8:12) must be zeroed
			// before checksumming: the directory entry records the checksum
			// AS COMPUTED during generation, before that field was patched
			// with the whole-font adjustment. Recomputing over the live
			// (patched) bytes would never match -- that is not corruption,
			// it is how every sfnt does it. See TestHeadChecksumAdjustment
			// for what the patched field itself must satisfy.
			zeroed := append([]byte(nil), tbl.data...)
			for i := 8; i < 12; i++ {
				zeroed[i] = 0
			}
			if got := tableChecksum(zeroed); got != tbl.checksum {
				t.Errorf("head checksum (adjustment zeroed) = %#08x, directory says %#08x", got, tbl.checksum)
			}
			continue
		}
		if got := tableChecksum(tbl.data); got != tbl.checksum {
			t.Errorf("table %q checksum = %#08x, directory says %#08x", tag, got, tbl.checksum)
		}
	}
}

// TestHeadChecksumAdjustment is the whole-font check: sum the entire file as
// big-endian uint32 words and the result must be exactly 0xB1B0AFBA once
// head's checkSumAdjustment is added back in. This is the "is this actually a
// valid sfnt" test every real validator runs; the per-table checksums above
// only prove each table matches its own directory entry.
func TestHeadChecksumAdjustment(t *testing.T) {
	if len(Font)%4 != 0 {
		t.Fatalf("font length %d is not a multiple of 4", len(Font))
	}
	_, tables := parseSFNT(t, Font)
	head, ok := tables["head"]
	if !ok {
		t.Fatal("no head table")
	}
	adjustment := binary.BigEndian.Uint32(head.data[8:12])

	raw := append([]byte(nil), Font...)
	for i := 0; i < 4; i++ {
		raw[int(head.offset)+8+i] = 0
	}
	sum := tableChecksum(raw)
	if got := 0xB1B0AFBA - sum; got != adjustment {
		t.Errorf("checkSumAdjustment = %#08x, want %#08x (0xB1B0AFBA - %#08x)", adjustment, got, sum)
	}
}

// TestGlyphOutlinesAreEmpty is the property that makes this a glyphless font:
// every glyph's loca entry brackets zero bytes of outline data, so nothing
// paints when the glyph is shown.
func TestGlyphOutlinesAreEmpty(t *testing.T) {
	_, tables := parseSFNT(t, Font)
	head, ok := tables["head"]
	if !ok {
		t.Fatal("no head table")
	}
	indexToLocFormat := int16(binary.BigEndian.Uint16(head.data[50:52]))
	maxp, ok := tables["maxp"]
	if !ok {
		t.Fatal("no maxp table")
	}
	numGlyphs := int(binary.BigEndian.Uint16(maxp.data[4:6]))
	if numGlyphs != NumGlyphs {
		t.Fatalf("maxp numGlyphs = %d, want %d (NumGlyphs)", numGlyphs, NumGlyphs)
	}

	loca, ok := tables["loca"]
	if !ok {
		t.Fatal("no loca table")
	}
	glyf, ok := tables["glyf"]
	if !ok {
		t.Fatal("no glyf table")
	}

	offset := func(i int) uint32 {
		switch indexToLocFormat {
		case 0: // short: stored offset/2
			return uint32(binary.BigEndian.Uint16(loca.data[i*2:i*2+2])) * 2
		case 1: // long
			return binary.BigEndian.Uint32(loca.data[i*4 : i*4+4])
		default:
			t.Fatalf("indexToLocFormat = %d, want 0 or 1", indexToLocFormat)
			return 0
		}
	}
	if got, want := len(loca.data), (numGlyphs+1)*2; indexToLocFormat == 0 && got != want {
		t.Fatalf("loca table is %d bytes, want %d for short format with %d glyphs", got, want, numGlyphs)
	}
	for i := 0; i < numGlyphs; i++ {
		start, end := offset(i), offset(i+1)
		if start != end {
			t.Errorf("glyph %d has a non-empty outline: loca[%d]=%d loca[%d]=%d", i, i, start, i+1, end)
		}
	}
	if len(glyf.data) != 0 {
		t.Errorf("glyf table is %d bytes, want 0: every glyph is empty so there is no outline data to store", len(glyf.data))
	}
}

// TestWidthTableMapsItsCodepoints checks the three tables that together
// position a glyph agree with each other: cmap resolves a covered rune to the
// glyph GlyphID also claims, and hmtx's advance for that glyph equals what
// Width reports.
func TestWidthTableMapsItsCodepoints(t *testing.T) {
	_, tables := parseSFNT(t, Font)
	hmtx, ok := tables["hmtx"]
	if !ok {
		t.Fatal("no hmtx table")
	}
	cmap, ok := tables["cmap"]
	if !ok {
		t.Fatal("no cmap table")
	}
	lookupCmap := parseCmapFormat4(t, cmap.data)

	for r := rune(FirstRune); r <= LastRune; r++ {
		wantGID, ok := GlyphID(r)
		if !ok {
			t.Fatalf("GlyphID(%q) reports not covered, want covered", r)
		}
		gotGID, ok := lookupCmap(uint16(r))
		if !ok {
			t.Errorf("cmap has no mapping for %q (U+%04X)", r, r)
			continue
		}
		if gotGID != wantGID {
			t.Errorf("cmap maps %q to glyph %d, GlyphID says %d", r, gotGID, wantGID)
		}
		wantWidth, ok := Width(r)
		if !ok {
			t.Fatalf("Width(%q) reports not covered, want covered", r)
		}
		if int(gotGID)*4+4 > len(hmtx.data) {
			t.Fatalf("hmtx too short for glyph %d", gotGID)
		}
		gotWidth := binary.BigEndian.Uint16(hmtx.data[gotGID*4 : gotGID*4+2])
		if gotWidth != wantWidth {
			t.Errorf("hmtx advance for %q (glyph %d) = %d, Width says %d", r, gotGID, gotWidth, wantWidth)
		}
	}

	// A rune outside the covered range must be absent from both the API and
	// the cmap, or GlyphID and the font itself disagree about what "covered"
	// means.
	if _, ok := GlyphID(0x20 - 1); ok {
		t.Error("GlyphID reports a rune below FirstRune as covered")
	}
	if _, ok := lookupCmap(0x20 - 1); ok {
		t.Error("cmap maps a codepoint below FirstRune; it should only cover FirstRune..LastRune")
	}
}

// parseCmapFormat4 returns a lookup function over the font's (3,1) Windows
// Unicode BMP subtable. It is deliberately narrow -- format 4 only, one
// encoding record -- because that is the only subtable this font ever writes;
// see gen.go.
func parseCmapFormat4(t *testing.T, data []byte) func(uint16) (uint16, bool) {
	t.Helper()
	numTables := binary.BigEndian.Uint16(data[2:4])
	var subOff uint32 = 0xFFFFFFFF
	for i := 0; i < int(numTables); i++ {
		rec := data[4+i*8 : 4+i*8+8]
		platformID := binary.BigEndian.Uint16(rec[0:2])
		encodingID := binary.BigEndian.Uint16(rec[2:4])
		if platformID == 3 && encodingID == 1 {
			subOff = binary.BigEndian.Uint32(rec[4:8])
		}
	}
	if subOff == 0xFFFFFFFF {
		t.Fatal("cmap has no (3,1) Windows Unicode BMP subtable")
	}
	sub := data[subOff:]
	format := binary.BigEndian.Uint16(sub[0:2])
	if format != 4 {
		t.Fatalf("cmap (3,1) subtable format = %d, want 4", format)
	}
	segCountX2 := binary.BigEndian.Uint16(sub[6:8])
	segCount := int(segCountX2 / 2)
	endCodeAt := 14
	startCodeAt := endCodeAt + int(segCountX2) + 2 // +2 for reservedPad
	idDeltaAt := startCodeAt + int(segCountX2)
	idRangeOffsetAt := idDeltaAt + int(segCountX2)

	return func(c uint16) (uint16, bool) {
		for i := 0; i < segCount; i++ {
			end := binary.BigEndian.Uint16(sub[endCodeAt+i*2 : endCodeAt+i*2+2])
			start := binary.BigEndian.Uint16(sub[startCodeAt+i*2 : startCodeAt+i*2+2])
			if c < start || c > end {
				continue
			}
			delta := binary.BigEndian.Uint16(sub[idDeltaAt+i*2 : idDeltaAt+i*2+2])
			rangeOffset := binary.BigEndian.Uint16(sub[idRangeOffsetAt+i*2 : idRangeOffsetAt+i*2+2])
			if rangeOffset == 0 {
				gid := c + delta
				if gid == 0 {
					return 0, false
				}
				return gid, true
			}
			// glyphIdArray indirection is unused by this font (idRangeOffset
			// is always 0, see gen.go) but is implemented here so a future
			// change to the generator has a test that actually exercises it
			// rather than one that silently stops checking.
			glyphIDAt := idRangeOffsetAt + i*2 + int(rangeOffset) + int(c-start)*2
			gid := binary.BigEndian.Uint16(sub[glyphIDAt : glyphIDAt+2])
			if gid == 0 {
				return 0, false
			}
			return gid + delta, true
		}
		return 0, false
	}
}

// --- optional end-to-end check against poppler ------------------------------

// minimalPDFWithFont writes a one-page PDF that shows text through this
// embedded font at invisible render mode 3, using a hand-rolled writer in the
// style of internal/corpus's -- this package cannot import corpus (that would
// make a test-only helper part of the module's real dependency graph) and
// does not need corpus's fixture catalog, only its minimal object/xref shape.
func minimalPDFWithFont(t *testing.T, text string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, 0, 8)
	reserve := func() int { offsets = append(offsets, -1); return len(offsets) }
	fill := func(n int, body string) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}
	fillStream := func(n int, dict string, payload []byte) {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		if _, err := zw.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Filter /FlateDecode /Length %d >>\nstream\n", n, dict, z.Len())
		buf.Write(z.Bytes())
		buf.WriteString("\nendstream\nendobj\n")
	}
	fillRawStream := func(n int, dict string, payload []byte) {
		offsets[n-1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n<< %s /Length %d >>\nstream\n", n, dict, len(payload))
		buf.Write(payload)
		buf.WriteString("\nendstream\nendobj\n")
	}

	cat, pages, page, cont := reserve(), reserve(), reserve(), reserve()
	font, descr, fontFile := reserve(), reserve(), reserve()

	var widths bytes.Buffer
	widths.WriteByte('[')
	for r := rune(FirstRune); r <= LastRune; r++ {
		w, _ := Width(r)
		fmt.Fprintf(&widths, "%d ", w)
	}
	widths.WriteByte(']')

	fill(cat, fmt.Sprintf("<< /Type /Catalog /Pages %d 0 R >>", pages))
	fill(pages, fmt.Sprintf("<< /Type /Pages /Kids [%d 0 R] /Count 1 >>", page))
	fill(page, fmt.Sprintf("<< /Type /Page /Parent %d 0 R /MediaBox [0 0 200 100]"+
		" /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", pages, font, cont))
	fillStream(cont, "", []byte(fmt.Sprintf("BT 3 Tr /F1 12 Tf 1 0 0 1 10 50 Tm (%s) Tj ET\n", text)))
	fill(font, fmt.Sprintf("<< /Type /Font /Subtype /TrueType /BaseFont /BbyblosGlyphless"+
		" /FirstChar %d /LastChar %d /Widths %s /FontDescriptor %d 0 R /Encoding /WinAnsiEncoding >>",
		FirstRune, LastRune, widths.String(), descr))
	fill(descr, fmt.Sprintf("<< /Type /FontDescriptor /FontName /BbyblosGlyphless /Flags 32"+
		" /FontBBox [0 0 0 0] /ItalicAngle 0 /Ascent 800 /Descent -200 /CapHeight 700"+
		" /StemV 80 /FontFile2 %d 0 R >>", fontFile))
	fillRawStream(fontFile, fmt.Sprintf("/Length1 %d", len(Font)), Font)

	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root %d 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, cat, start)
	return buf.Bytes()
}

// TestPdftotextExtractsStampedText is the acceptance criterion in byb-b4: text
// shown through this font at render mode 3 round-trips through pdftotext, and
// the embedded font program itself is one poppler actually loads. Those are
// two separate claims -- pdftotext resolves glyphs from the content stream's
// /Encoding, never from FontFile2, so it round-trips the text even when the
// embedded program is garbage (verified: swapping Font for 8 bytes of junk
// still makes pdftotext print "Hello Byblos", it just also prints "Syntax
// Error: Embedded font file may be invalid" to a stream this test would
// otherwise ignore). pdftoppm does consult FontFile2 to rasterize glyphs and
// reports that same syntax error on stderr when the program fails to parse,
// so a silent pdftoppm run is what actually proves the glyf/loca/head/hmtx
// tables this package generates are well-formed. Both tools skip cleanly when
// not installed (see oracle_test.go:52 for the established pattern) rather
// than failing CI on tools this repo does not depend on.
func TestPdftotextExtractsStampedText(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed")
	}
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	const want = "Hello Byblos"
	pdf := minimalPDFWithFont(t, want)

	dir := t.TempDir()
	path := dir + "/glyphless.pdf"
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("pdftotext", path, "-").Output()
	if err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	if got := string(bytes.TrimSpace(out)); got != want {
		t.Errorf("pdftotext extracted %q, want %q", got, want)
	}

	// pdftoppm forces poppler to load FontFile2 to rasterize the page, unlike
	// pdftotext above. A malformed font doesn't fail the process -- poppler
	// substitutes a fallback and only reports the problem on stderr -- so the
	// exit code proves nothing here; the stderr text is the only signal that
	// the font itself, not just the content stream, survived the round trip.
	ppOut, err := exec.Command("pdftoppm", path, dir+"/page").CombinedOutput()
	if err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, ppOut)
	}
	if len(ppOut) != 0 {
		t.Errorf("pdftoppm reported a problem loading the embedded font: %s", ppOut)
	}
}
