package linearize_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/dobbo-ca/byblos/internal/linearize"
)

// byb-1y7's highest-risk component: the primary hint stream's bit packing,
// pinned by two fixed byte vectors.
//
// WHY BYTE VECTORS AND NOT "COMPARE TO QPDF". The hint tables are written
// column-major, MSB-first, with the bit accumulator flushed to a byte boundary
// after EVERY column (qpdf QPDF_linearization.cc write_vector_int:1876-1886,
// whose own comment reads "The PDF spec says that each hint table starts at a
// byte boundary. Each 'row' actually must start on a byte boundary"). ISO
// 32000-1 Table F.3 lists the per-page items in an order that reads like rows,
// so the natural misreading is a row-major encoder -- which produces a payload
// of exactly the right LENGTH and entirely wrong bytes. A length check would
// pass. A round-trip through our own decoder would pass. Only fixed expected
// bytes catch it, and only a fixed vector runs when qpdf is not installed,
// which is the case this repo's oracle gating creates on every machine without
// it.
//
// Specifically, these two vectors catch:
//
//	(a) row-major instead of column-major layout;
//	(b) LSB-first bit order within a value;
//	(c) flushing per table or per row instead of per column;
//	(d) nbits(0) == 1 instead of 0 (qpdf's nbits, :1701-1703, is
//	    val == 0 ? 0 : 1 + nbits(val >> 1)) -- vector 1 has THREE zero-width
//	    columns, so an off-by-one nbits changes both the header and the
//	    payload length;
//	(e) deriving the header's minima or bit widths from the wrong extremum.
//
// PROVENANCE OF THE VECTORS. Both were encoded by hand from the field layout in
// ISO 32000-1 Tables F.3 and F.5, from the stated inputs and nothing else:
//
//	vector 1: a 1-page document,  68 B payload, /S 36  (sha256 437062543725058b...)
//	vector 2: a 3-page document,  98 B payload, /S 57  (sha256 c87cbe435f55bf73...)
//
// Independently re-derived since, by a second bit-packer written from the same
// two tables with no reference to hints.go: both vectors reproduce byte for byte
// from the inputs below. So the test really is "these inputs must produce these
// bytes", not "these bytes equal themselves" -- but the guarantee is the spec
// plus a second reading of it, not a captured artefact.
//
// Do NOT describe them as extracted from a qpdf-linearized file. That claim was
// made here once and does not hold: 77 hint streams -- every PDF in pdfcpu
// v0.13.0's testdata plus every corpus document, each run through
// `qpdf --linearize` and inflated from /H -- match neither vector. The source
// files, if there were any, are unidentified.
//
// What IS confirmed against qpdf 12.3.2, by reading `qpdf --show-linearization`
// on its own output, is the three conventions the vectors encode where Annex F
// leaves a choice: min_content_length equals min_page_length with the same bit
// width, nbits_delta_content_offset is 0, and shared_denominator is 4.

// The /S value is the byte offset of the shared-object hint table within the
// payload, i.e. the length of the page-offset hint table. It is emitted into
// the hint stream dictionary, so it is part of the contract, not a detail.
const (
	wantSSinglePage = 36
	wantSThreePage  = 57
)

// mustHex fails the test rather than returning an error: a malformed literal in
// this file is a bug in the test, not a result.
func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex literal in this test file: %v", err)
	}
	return b
}

// TestEncodeHintsSinglePageVector pins the degenerate shape that 24 of the 28
// corpus documents have: one page, so every per-page column is zero-width and
// the whole page-offset table is its 36-byte header.
//
// It is the (d) vector. nbits_delta_nobjects, nbits_delta_page_length and
// nbits_nshared_objects are all 0 here because their maxima equal their minima;
// an encoder using nbits(0) == 1 would emit three 1-bit columns of one row
// each, three extra flush bytes, a payload of 71 rather than 68 bytes, and
// three wrong 16-bit header fields. It also pins the two values Annex F leaves
// to convention and qpdf fixes (calculateHPageOffset:1783-1795):
// min_content_offset = 0 with a zero-width delta column, and min_content_length
// mirroring min_page_length with the same bit width.
//
// A do-nothing EncodeHints returning (nil, 0, nil) fails on the first
// comparison; one returning a zeroed 68-byte buffer fails on the header.
func TestEncodeHintsSinglePageVector(t *testing.T) {
	want := mustHex(t,
		"000000030000022500000001dac30000"+
			"0000000000000001dac3000000000002"+
			"00000004000000000000000000000003"+
			"000000030000000000500011001a0000"+
			"3b33e000")

	got, err := linearize.EncodeHints(linearize.Hints{
		// One page holding 3 objects and 121539 bytes.
		Pages: []linearize.PageHint{
			{NObjects: 3, Length: 121539},
		},
		FirstPageOffset: 549,
		// Those same 3 objects are the first page's shared groups. Their
		// lengths sum to the page length: 132 + 80 + 121327 == 121539, which is
		// what makes min_group_length 80 and nbits_delta_group_length 17
		// (121327 - 80 == 121247, which needs 17 bits). That 17 is the
		// interesting number here: it is not a multiple of 8, so the three
		// 17-bit values straddle byte boundaries and the last one is padded --
		// the exact arithmetic an LSB-first or per-row-flushing encoder gets
		// wrong. The expected bytes for that column are 00 1a 00 00 3b 33 e0.
		Shared: []linearize.SharedHint{
			{GroupLength: 132}, {GroupLength: 80}, {GroupLength: 121327},
		},
		NSharedFirstPage: 3,
		// Left at zero exactly as qpdf leaves them when nshared_total is not
		// greater than nshared_first_page (calculateHSharedObject).
		FirstSharedObj:    0,
		FirstSharedOffset: 0,
	})
	if err != nil {
		t.Fatalf("EncodeHints: %v", err)
	}
	if got.S != wantSSinglePage {
		t.Errorf("S = %d; want %d (the shared table must start right after the "+
			"36-byte page-offset header when every per-page column is zero-width)",
			got.S, wantSSinglePage)
	}
	assertPayload(t, got.Payload, want)
}

// TestEncodeHintsThreePageVector is the vector that actually exercises the bit
// packing: three pages, four columns of non-trivial width, and a
// vector-of-vectors column.
//
// Widths in play: 3-bit delta_nobjects, 13-bit delta_page_length, 3-bit
// nshared_objects, 4-bit shared_identifiers (14 entries), zero-width
// shared_numerators and delta_content_offset, 13-bit delta_content_length,
// then 13-bit delta_group_length over 9 groups and a 1-bit signature column.
// Nine of those numbers do not divide 8. A row-major encoder -- one that writes
// each page's items together, as Table F.3's ordering suggests -- emits the
// same 98 bytes with different contents and would pass any length or
// round-trip check. A per-table (rather than per-column) flush loses the pad
// bits between columns and shortens the payload to 96.
//
// It also pins the two derived widths: nbits_shared_identifier is nbits of
// nshared_total (9 -> 4), NOT of the largest identifier used (8 -> 4 by
// coincidence here, which is why the shared table's own 9 groups matter), and
// nbits_nshared_objects is nbits of the largest per-page count (7 -> 3).
// shared_denominator is the constant 4 qpdf writes.
//
// Page 0 carries no shared identifiers. That is required: ISO 32000-1 Table F.4
// item 3 defines the first page's shared objects implicitly, and qpdf warns
// "page 0 has shared identifier entries" (QPDF_linearization.cc:924) for a file
// that lists them. An encoder that treats page 0 like the others changes the
// nshared_objects column and the identifier column's length.
func TestEncodeHintsThreePageVector(t *testing.T) {
	want := mustHex(t,
		"000000020000029f000300000241000d"+
			"00000000000000000241000d00030004"+
			"00000004e000a1b000000a1f80234567"+
			"82345678a1b000000a00000000000000"+
			"00000000090000000900000000002800"+
			"0d061049c00000003503242260a181f8"+
			"0000")

	shared := []int{2, 3, 4, 5, 6, 7, 8}
	got, err := linearize.EncodeHints(linearize.Hints{
		Pages: []linearize.PageHint{
			{NObjects: 9, Length: 5751},
			{NObjects: 2, Length: 577, SharedIDs: shared},
			{NObjects: 2, Length: 582, SharedIDs: shared},
		},
		FirstPageOffset: 671,
		// Page 0's nine objects, in order. Their lengths sum to 5751, page 0's
		// length. min_group_length is 40 and the widest delta is 4199 - 40,
		// hence 13 bits over nine values: 117 bits, so the column is padded to
		// 15 bytes and the 1-bit signature column that follows starts on a
		// fresh byte.
		Shared: []linearize.SharedHint{
			{GroupLength: 234}, {GroupLength: 335}, {GroupLength: 40},
			{GroupLength: 40}, {GroupLength: 146}, {GroupLength: 241},
			{GroupLength: 315}, {GroupLength: 201}, {GroupLength: 4199},
		},
		NSharedFirstPage:  9,
		FirstSharedObj:    0,
		FirstSharedOffset: 0,
	})
	if err != nil {
		t.Fatalf("EncodeHints: %v", err)
	}
	if got.S != wantSThreePage {
		t.Errorf("S = %d; want %d", got.S, wantSThreePage)
	}
	assertPayload(t, got.Payload, want)
}

// assertPayload reports the first differing byte rather than dumping two blobs,
// because the failure that matters (column-major vs row-major) diverges at a
// known offset and the offset is the diagnosis.
func assertPayload(t *testing.T, got, want []byte) {
	t.Helper()
	if bytes.Equal(got, want) {
		return
	}
	if len(got) != len(want) {
		t.Errorf("payload is %d bytes; want %d", len(got), len(want))
	}
	for i := 0; i < min(len(got), len(want)); i++ {
		if got[i] != want[i] {
			lo, hi := max(0, i-4), min(len(want), i+8)
			t.Fatalf("payload differs at byte %d: got %#02x, want %#02x\n"+
				"  got  [%d:%d] % x\n  want [%d:%d] % x\n"+
				"(byte %d is in the %s)",
				i, got[i], want[i],
				lo, min(len(got), hi), got[lo:min(len(got), hi)],
				lo, hi, want[lo:hi],
				i, whichRegion(i))
		}
	}
	t.Fatalf("payload lengths differ: got %d, want %d", len(got), len(want))
}

// whichRegion names the structure a differing byte offset falls in, so a
// failure says which table is wrong rather than just "byte 41". The header is
// a fixed 36 bytes for every document (Table F.3 items 1-13, 288 bits).
func whichRegion(i int) string {
	if i < 36 {
		return "page-offset hint table header (ISO 32000-1 Table F.3 items 1-13)"
	}
	return "per-page columns, or the shared-object table that follows them at /S"
}
