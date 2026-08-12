package jbig2

import (
	"encoding/binary"
	"fmt"
)

// Segment types that carry symbols, and the ones that consume them. T.88 7.3
// numbers them; the generic-region types this package already knew are in
// segment.go and segment_decode.go.
//
// The halftone, pattern-dictionary and refinement types are deliberately NOT
// named here. Nothing dispatches on them -- planStream's default branch reports
// them by number as an unsupported feature, which is the whole of what this
// package does with them -- so a constant for each would be a name with no
// reader, and the number in the error message is what a maintainer looks up.
const (
	segTypeSymbolDictionary            = 0
	segTypeIntermediateTextRegion      = 4
	segTypeImmediateTextRegion         = 6
	segTypeImmediateLosslessTextRegion = 7
)

// symbolDictHeader is the symbol dictionary segment header of T.88 7.4.4:
// everything before the arithmetic data, parsed and no more.
//
// THE HUFFMAN SELECTOR FIELDS ARE READ AND KEPT even though nothing decodes a
// Huffman symbol dictionary, and ProfileStream is what reads them. A selector of
// 3 means "a custom table carried in a referred-to table segment" and 0-2 mean
// one of Annex B's standard tables, which are two quite different amounts of
// work to add. Refusing SDHUFF wholesale does not need the distinction; pricing
// what is left after byb-9v0 does, and a refusal that cannot say WHICH Huffman
// shape it met is a refusal nobody can act on.
type symbolDictHeader struct {
	huff      bool // SDHUFF
	refAgg    bool // SDREFAGG
	huffDH    int  // SDHUFFDH selector, 0-3
	huffDW    int  // SDHUFFDW selector, 0-3
	huffBM    int  // SDHUFFBMSIZE selector, 0-1
	huffAgg   int  // SDHUFFAGGINST selector, 0-1
	ctxUsed   bool // bitmap coding context used
	template  int  // SDTEMPLATE, 0-3
	rTemplate int  // SDRTEMPLATE, 0-1

	at  [4][2]int8 // SDAT: 4 pairs for template 0, else 1
	nAT int
	rat [2][2]int8 // SDRAT, when refAgg and rTemplate == 0

	numEx  uint32 // SDNUMEXSYMS
	numNew uint32 // SDNUMNEWSYMS

	dataOff int // offset in the segment data where the coded data starts
}

// parseSymbolDictHeader reads T.88 7.4.4.1 through 7.4.4.5.
//
// Every length below is checked before it is read. The fields are
// variable-length in two independent ways -- the AT field's size depends on
// SDTEMPLATE, and the refinement AT field is present only under SDREFAGG -- so a
// single up-front minimum would either be too large (refusing a legal 14-byte
// header) or too small (reading past a truncated one).
func parseSymbolDictHeader(d []byte) (symbolDictHeader, error) {
	var h symbolDictHeader
	if len(d) < 2 {
		return h, fmt.Errorf("jbig2: symbol dictionary segment is %d bytes; want at least 2 for its flags", len(d))
	}
	f := binary.BigEndian.Uint16(d[0:2])
	h.huff = f&0x0001 != 0
	h.refAgg = f&0x0002 != 0
	h.huffDH = int(f>>2) & 3
	h.huffDW = int(f>>4) & 3
	h.huffBM = int(f>>6) & 1
	h.huffAgg = int(f>>7) & 1
	h.ctxUsed = f&0x0100 != 0
	// Bit 9, "bitmap coding context retained", is deliberately not kept. A
	// dictionary that retains its statistics for a later segment costs this
	// decoder nothing; what it cannot do is READ a retained context, and that is
	// bit 8 above.
	h.template = int(f>>10) & 3
	h.rTemplate = int(f>>12) & 1
	i := 2

	// T.88 7.4.4.1.2: the AT field is present only for arithmetic coding, and
	// carries one pair for templates 1-3 against template 0's four.
	if !h.huff {
		h.nAT = 1
		if h.template == 0 {
			h.nAT = 4
		}
		if len(d) < i+2*h.nAT {
			return h, fmt.Errorf("jbig2: symbol dictionary segment is %d bytes; want %d for the AT field of template %d",
				len(d), i+2*h.nAT, h.template)
		}
		for k := 0; k < h.nAT; k++ {
			h.at[k] = [2]int8{int8(d[i]), int8(d[i+1])}
			i += 2
		}
	}
	if h.refAgg && h.rTemplate == 0 {
		if len(d) < i+4 {
			return h, fmt.Errorf("jbig2: symbol dictionary segment is %d bytes; want %d for the refinement AT field", len(d), i+4)
		}
		for k := 0; k < 2; k++ {
			h.rat[k] = [2]int8{int8(d[i]), int8(d[i+1])}
			i += 2
		}
	}
	if len(d) < i+8 {
		return h, fmt.Errorf("jbig2: symbol dictionary segment is %d bytes; want %d for its two symbol counts", len(d), i+8)
	}
	h.numEx = binary.BigEndian.Uint32(d[i : i+4])
	h.numNew = binary.BigEndian.Uint32(d[i+4 : i+8])
	h.dataOff = i + 8
	return h, nil
}

// unsupported reports the symbol dictionary variants this package parses and
// will not decode, or nil.
//
// IT IS CHECKED FROM THE HEADER WALK, BEFORE ANY BUDGET IS CHARGED, and the
// order is not cosmetic. A Huffman dictionary's header has no AT field, so its
// two symbol counts sit eight bytes earlier than an arithmetic one's; read under
// the wrong assumption they are whatever the coded data happens to start with,
// and the first thing to notice is the symbol budget. That would report a legal
// Huffman stream as one asking for four billion symbols -- damage rather than a
// feature byblos lacks. An archive deciding what to re-process later acts on
// exactly that difference (jbig2.go), and the byb-9v0 census counts by it.
func (h symbolDictHeader) unsupported() error {
	switch {
	case h.huff:
		return fmt.Errorf("%w: Huffman-coded symbol dictionary (SDHUFF)", ErrUnsupportedFeature)
	case h.refAgg:
		return fmt.Errorf("%w: symbol dictionary using refinement or aggregate coding (SDREFAGG)",
			ErrUnsupportedFeature)
	case h.ctxUsed:
		// T.88 7.4.4.1.1 bit 8: the dictionary resumes the arithmetic statistics
		// another segment retained. Nothing here retains any, so decoding it
		// would start from reset statistics and desynchronise on the first
		// symbol.
		return fmt.Errorf("%w: symbol dictionary using a retained bitmap coding context",
			ErrUnsupportedFeature)
	}
	return nil
}

// unsupported reports the text region variants this package parses and will not
// decode, or nil. Same ordering argument as the symbol dictionary's: a Huffman
// text region carries a selector word the arithmetic one does not, so every
// field after it is displaced.
func (h textRegionHeader) unsupported() error {
	switch {
	case h.huff:
		return fmt.Errorf("%w: Huffman-coded text region (SBHUFF)", ErrUnsupportedFeature)
	case h.refine:
		return fmt.Errorf("%w: text region using refinement coding (SBREFINE)", ErrUnsupportedFeature)
	}
	return nil
}

// Reference corners, T.88 6.4.5 step 3c x. The number IS the encoding in the
// text region flags field, so these are not an internal enumeration.
const (
	refCornerBottomLeft = iota
	refCornerTopLeft
	refCornerBottomRight
	refCornerTopRight
)

// textRegionHeader is the text region segment header of T.88 7.4.3: the region
// segment information field, the flags, and the instance count.
type textRegionHeader struct {
	info regionInfo

	huff       bool // SBHUFF
	refine     bool // SBREFINE
	logStrips  int  // LOGSBSTRIPS, 0-3; SBSTRIPS is 1<<LOGSBSTRIPS
	refCorner  int  // REFCORNER
	transposed bool // TRANSPOSED
	combOp     int  // SBCOMBOP
	defPixel   int  // SBDEFPIXEL
	dsOffset   int  // SBDSOFFSET, a signed 5-bit field
	rTemplate  int  // SBRTEMPLATE

	huffFlags uint16     // the Huffman selector word, present only under SBHUFF
	rat       [2][2]int8 // SBRAT, when refine and rTemplate == 0

	numInstances uint32 // SBNUMINSTANCES

	dataOff int // offset in the segment data where the symbol ID codes start
}

func parseTextRegionHeader(d []byte) (textRegionHeader, error) {
	var h textRegionHeader
	info, err := parseRegionInfo(d)
	if err != nil {
		return h, err
	}
	h.info = info
	if len(d) < 19 {
		return h, fmt.Errorf("jbig2: text region segment is %d bytes; want at least 19 for its flags", len(d))
	}
	f := binary.BigEndian.Uint16(d[17:19])
	h.huff = f&0x0001 != 0
	h.refine = f&0x0002 != 0
	h.logStrips = int(f>>2) & 3
	h.refCorner = int(f>>4) & 3
	h.transposed = f&0x0040 != 0
	h.combOp = int(f>>7) & 3
	h.defPixel = int(f>>9) & 1
	// SBDSOFFSET is five bits in two's complement (T.88 7.4.3.1.1), so the
	// range is -16..15 and the sign extension is not optional: an encoder
	// writing -1 writes 31, and reading it as 31 shifts every symbol after the
	// first in a strip 32 pixels to the right.
	h.dsOffset = int(f>>10) & 0x1F
	if h.dsOffset > 15 {
		h.dsOffset -= 32
	}
	h.rTemplate = int(f>>15) & 1
	i := 19

	if h.huff {
		if len(d) < i+2 {
			return h, fmt.Errorf("jbig2: text region segment is %d bytes; want %d for its Huffman flags", len(d), i+2)
		}
		h.huffFlags = binary.BigEndian.Uint16(d[i : i+2])
		i += 2
	}
	if h.refine && h.rTemplate == 0 {
		if len(d) < i+4 {
			return h, fmt.Errorf("jbig2: text region segment is %d bytes; want %d for the refinement AT field", len(d), i+4)
		}
		for k := 0; k < 2; k++ {
			h.rat[k] = [2]int8{int8(d[i]), int8(d[i+1])}
			i += 2
		}
	}
	if len(d) < i+4 {
		return h, fmt.Errorf("jbig2: text region segment is %d bytes; want %d for its instance count", len(d), i+4)
	}
	h.numInstances = binary.BigEndian.Uint32(d[i : i+4])
	h.dataOff = i + 4
	return h, nil
}

// symCodeLen is SBSYMCODELEN of T.88 6.4.6: the number of bits an arithmetically
// coded symbol ID occupies, ceil(log2(n)) over the symbol list.
//
// n == 1 GIVES ZERO BITS, and that is the specification rather than an edge case
// left unhandled: with one symbol in the list there is nothing to distinguish,
// and decodeIAID reads no bits and returns 0. Encoders are known to write one
// bit there instead, and this decoder does not try to detect which. Guessing
// wrong desynchronises every decision after it, so the failure mode is a page of
// noise rather than an error -- which is the one outcome this package exists to
// avoid, and the reason the choice is stated here rather than left implicit.
func symCodeLen(n int) int {
	bits := 0
	for 1<<bits < n {
		bits++
	}
	return bits
}
