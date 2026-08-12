package jbig2

// The referred-to segment numbers of T.88 7.2.5, which parseSegments stepped
// over until byb-9v0 and now retains.
//
// THEY ARE THE ONLY THING THAT SAYS WHICH DICTIONARY A TEXT REGION USES. A text
// region names a symbol by its index in a list, and that list is the exported
// symbols of its referred-to dictionaries concatenated in the order the header
// names them. Nothing else in a JBIG2 stream carries the mapping, so getting the
// order or the width wrong renumbers every glyph on the page -- with no error
// anywhere, because every index still lands inside a list of the right length.

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"
)

// The three widths a referred-to segment number can take (T.88 7.2.5), and the
// long form of the count (7.2.4), read back as VALUES rather than merely stepped
// over at the right length. TestParseSegmentsHeaderArithmetic already pins that
// the parse lands in the right place afterwards; this pins what it read.
func TestParseSegmentsRetainsReferredToNumbers(t *testing.T) {
	for _, c := range []struct {
		name string
		h    handHeader
	}{
		{"one-byte numbers", handHeader{num: 200, typ: segTypeImmediateTextRegion,
			refs: []uint32{1, 2, 250}, refBytes: 1, data: []byte{0x01}}},
		{"two-byte numbers", handHeader{num: 60000, typ: segTypeImmediateTextRegion,
			refs: []uint32{1, 60000, 65535}, refBytes: 2, data: []byte{0x01}}},
		{"four-byte numbers", handHeader{num: 70000, typ: segTypeImmediateTextRegion,
			refs: []uint32{1, 70000, 4000000000}, refBytes: 4, data: []byte{0x01}}},
		{"long-form count", handHeader{num: 70001, typ: segTypeImmediateTextRegion,
			refs: []uint32{9, 8, 7, 6, 5, 4, 3, 2, 1}, refBytes: 4,
			longCount: true, retainBytes: 2, data: []byte{0x01}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := parseSegments(c.h.bytes())
			if err != nil {
				t.Fatalf("parseSegments() error = %v", err)
			}
			if len(p.segs) != 1 {
				t.Fatalf("parseSegments() returned %d segments; want 1", len(p.segs))
			}
			got := p.refsOf(p.segs[0])
			if !slices.Equal(got, c.h.refs) {
				t.Errorf("refs = %v; want %v, IN THAT ORDER -- the order is the symbol numbering",
					got, c.h.refs)
			}
		})
	}
}

// A segment referring to nothing must come back with an empty list and not with
// somebody else's: the slab is shared, so an off-by-one in refFirst hands one
// segment another's references, which is the failure that renumbers glyphs.
func TestRefsOfIsPerSegmentAcrossASharedSlab(t *testing.T) {
	first := handHeader{num: 1, typ: segTypeSymbolDictionary, data: []byte{0x01}}
	second := handHeader{num: 2, typ: segTypeImmediateTextRegion,
		refs: []uint32{1}, refBytes: 1, data: []byte{0x02}}
	third := handHeader{num: 3, typ: segTypeImmediateTextRegion,
		refs: []uint32{1, 2}, refBytes: 1, data: []byte{0x03}}

	s := append(first.bytes(), second.bytes()...)
	p, err := parseSegments(append(s, third.bytes()...))
	if err != nil {
		t.Fatalf("parseSegments() error = %v", err)
	}
	for i, want := range [][]uint32{{}, {1}, {1, 2}} {
		if got := p.refsOf(p.segs[i]); !slices.Equal(got, want) {
			t.Errorf("segment %d refs = %v; want %v", p.segs[i].number, got, want)
		}
	}
}

// Rule 5's other half. The count field is attacker-chosen and the slab is
// allocated from it, so a stream referring to more segments than a decoder could
// ever use is refused while the headers are still being read.
func TestTotalReferredToNumbersAreBounded(t *testing.T) {
	// EIGHT references per segment, which needs the long form of the count.
	// Four per segment would not do: maxStreamRefs is four times
	// maxStreamSegments by construction, so a stream averaging four hits the
	// SEGMENT cap at the same moment and the wrong rule would be what refused
	// it. Eight is what makes this test about the reference bound.
	//
	// refBytes tracks the segment number exactly as T.88 7.2.5 sizes the field,
	// because the parser sizes it that way and a mismatch slides the parse into
	// reading a reference as a page association.
	var s []byte
	for n := uint32(0); n < maxStreamRefs/8+2; n++ {
		refBytes := 1
		switch {
		case n > 65536:
			refBytes = 4
		case n > 256:
			refBytes = 2
		}
		s = append(s, handHeader{num: n, typ: segTypeImmediateTextRegion,
			refs: make([]uint32, 8), refBytes: refBytes,
			longCount: true, retainBytes: 2, data: nil}.bytes()...)
	}
	_, err := parseSegments(s)
	if err == nil {
		t.Fatalf("parseSegments accepted a stream carrying more than %d referred-to numbers",
			int64(maxStreamRefs))
	}
	if !strings.Contains(err.Error(), "refers to more than") {
		t.Errorf("error = %v; want the referred-to bound to be what refuses it", err)
	}
}

// A text region whose referred-to dictionary is not in the stream must be
// refused, not decoded against an empty symbol list. An empty list places
// nothing, so the page would come back blank and correct-looking.
func TestTextRegionRefersToASegmentThatIsNotThere(t *testing.T) {
	syms := testSymbols()
	insts := []instance{{0, 20, 14}}
	p := textParams{w: 120, h: 100}
	text := buildTextRegion(p, syms, insts)

	// The stream symbolStream builds, with the dictionary segment left out.
	pi := pageInfoSegmentData(p.w, p.h)
	s := append(segmentHeader(0, segTypePageInformation, 1, len(pi)), pi...)
	h := make([]byte, 0, 12)
	h = binary.BigEndian.AppendUint32(h, 2)
	h = append(h, segTypeImmediateTextRegion, 0x20, 0x01, 0x01)
	h = binary.BigEndian.AppendUint32(h, uint32(len(text)))
	s = append(append(s, h...), text...)

	got, err := DecodeEmbeddedStream(s)
	if err == nil {
		t.Fatalf("decoded a %dx%d page from a text region whose symbol dictionary is not in the "+
			"stream; it carries %d ink pixels and every one of them is invented", got.W, got.H, inkCount(got))
	}
	if !strings.Contains(err.Error(), "no symbol dictionary") {
		t.Errorf("error = %v; want it to name the missing dictionary", err)
	}
}
