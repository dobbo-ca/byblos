package linearize

import (
	"encoding/binary"
	"fmt"
)

// The primary hint stream (ISO 32000-1:2008 Annex F.4).
//
// Two tables are emitted: the page offset hint table and the shared object
// hint table, plus the generic outline table when the document has outlines.
// The thumbnail, thread, named-destination, logical-structure, page-label,
// renumbering and embedded-file tables are all optional and are not emitted;
// nothing reads them (qpdf's checker skips every table it is not given).
//
// LAYOUT RULES, and the reason this package holds no PDF parser.
//
// Every table is a fixed-width big-endian header followed by bit-packed
// COLUMNS, not rows. Table F.3 lists the per-page items in an order that reads
// like a row per page, and the natural misreading of it -- write page 0's items,
// then page 1's -- produces a payload of exactly the right length with entirely
// wrong bytes. The accumulator is MSB-first and is flushed to a byte boundary
// after each column, never within one and never only at the end of the table.
//
// Bit widths come from nbits, which is 0 for a zero value: a column whose
// values are all equal to the header's minimum occupies no bytes at all.
//
// hints_test.go pins both facts against two byte vectors taken from files
// linearized by qpdf 12.3.2.

// PageHint is one page's row of the page offset hint table.
//
// Length is the number of bytes the page's objects occupy, i.e. the sum over
// the page's NObjects consecutive object numbers of each object's serialized
// extent. SharedIDs are indices into Hints.Shared, and must be empty for page
// 0: Table F.4 item 3 defines the first page's shared objects implicitly, and
// qpdf warns "page 0 has shared identifier entries" for a file that lists them.
type PageHint struct {
	NObjects  int
	Length    int
	SharedIDs []int
}

// SharedHint is one group of the shared object hint table. Every group here is
// a single object, so the table's "number of objects in the group" column is
// zero-width.
type SharedHint struct {
	GroupLength int
}

// OutlineHint is the generic hint table Annex F.4.6 defines for the outline
// tree. It is emitted only when the document has one; the hint stream
// dictionary's /O names its offset.
type OutlineHint struct {
	FirstObject       int
	FirstObjectOffset int
	NObjects          int
	GroupLength       int
}

// Hints is everything the primary hint stream is computed from.
//
// FirstPageOffset, like every offset stored in a hint table, is measured as if
// the primary hint stream were not present (Annex F.4) -- so it is the offset
// the first page's first object had before the hint stream was spliced in, not
// its offset in the finished file.
type Hints struct {
	Pages             []PageHint
	FirstPageOffset   int
	Shared            []SharedHint
	NSharedFirstPage  int
	FirstSharedObj    int
	FirstSharedOffset int
	Outline           *OutlineHint
}

// Hint is an encoded primary hint stream.
//
// S and O are the /S and /O entries of the hint stream dictionary: the offsets,
// within Payload, of the shared object table and of the outline table. O is 0
// when there is no outline table, and /O must then be omitted.
type Hint struct {
	Payload []byte
	S       int
	O       int
}

// sharedDenominator is item 13 of Table F.3. The numerator column is
// zero-width, so the fraction is always 0/4 and the denominator is inert; 4 is
// what qpdf writes and what its checker was measured against.
const sharedDenominator = 4

// EncodeHints builds the primary hint stream payload.
func EncodeHints(h Hints) (Hint, error) {
	if len(h.Pages) == 0 {
		return Hint{}, fmt.Errorf("linearize: hint tables need at least one page")
	}
	if len(h.Pages[0].SharedIDs) != 0 {
		// Not defensive: an encoder that treats page 0 like the others changes
		// both the nshared_objects column and the identifier column's length,
		// and qpdf rejects the result.
		return Hint{}, fmt.Errorf("linearize: page 0 must carry no shared identifiers, has %d",
			len(h.Pages[0].SharedIDs))
	}
	if h.NSharedFirstPage > len(h.Shared) {
		return Hint{}, fmt.Errorf("linearize: %d first-page shared groups out of %d",
			h.NSharedFirstPage, len(h.Shared))
	}

	var w bitWriter
	writePageOffsetTable(&w, h)
	s := len(w.out)
	writeSharedObjectTable(&w, h)
	o := 0
	if h.Outline != nil {
		o = len(w.out)
		w.uint32(h.Outline.FirstObject)
		w.uint32(h.Outline.FirstObjectOffset)
		w.uint32(h.Outline.NObjects)
		w.uint32(h.Outline.GroupLength)
	}
	return Hint{Payload: w.out, S: s, O: o}, nil
}

func writePageOffsetTable(w *bitWriter, h Hints) {
	minObjects, maxObjects := extent(h.Pages, func(p PageHint) int { return p.NObjects })
	minLength, maxLength := extent(h.Pages, func(p PageHint) int { return p.Length })
	_, maxShared := extent(h.Pages, func(p PageHint) int { return len(p.SharedIDs) })

	nbitsObjects := nbits(maxObjects - minObjects)
	nbitsLength := nbits(maxLength - minLength)
	nbitsShared := nbits(maxShared)
	nbitsIdent := nbits(len(h.Shared))

	// Items 1-13 of Table F.3. Items 6-9 describe the first content stream of
	// each page; a conforming reader may treat the whole page as one unit, so
	// the content offset column is zero-width from a minimum of 0 and the
	// content length column mirrors the page length column exactly. That is
	// what qpdf writes (calculateHPageOffset) and what its checker accepts.
	w.uint32(minObjects)        // 1  least number of objects in a page
	w.uint32(h.FirstPageOffset) // 2  location of the first page's first object
	w.uint16(nbitsObjects)      // 3  bits for the object-count delta
	w.uint32(minLength)         // 4  least page length
	w.uint16(nbitsLength)       // 5  bits for the page-length delta
	w.uint32(0)                 // 6  least content stream offset
	w.uint16(0)                 // 7  bits for the content-offset delta
	w.uint32(minLength)         // 8  least content stream length
	w.uint16(nbitsLength)       // 9  bits for the content-length delta
	w.uint16(nbitsShared)       // 10 bits for a page's shared-object count
	w.uint16(nbitsIdent)        // 11 bits for a shared-object identifier
	w.uint16(0)                 // 12 bits for a shared-object numerator
	w.uint16(sharedDenominator) // 13 shared-object denominator

	for _, p := range h.Pages {
		w.bits(p.NObjects-minObjects, nbitsObjects)
	}
	w.flush()
	for _, p := range h.Pages {
		w.bits(p.Length-minLength, nbitsLength)
	}
	w.flush()
	for _, p := range h.Pages {
		w.bits(len(p.SharedIDs), nbitsShared)
	}
	w.flush()
	for _, p := range h.Pages {
		for _, id := range p.SharedIDs {
			w.bits(id, nbitsIdent)
		}
	}
	w.flush()
	// Shared numerators: zero-width, so nothing is written, but the flush is
	// kept so the column structure reads as it is defined.
	w.flush()
	// Content offsets: zero-width for the same reason as item 7.
	w.flush()
	for _, p := range h.Pages {
		w.bits(p.Length-minLength, nbitsLength)
	}
	w.flush()
}

func writeSharedObjectTable(w *bitWriter, h Hints) {
	minGroup, maxGroup := 0, 0
	for i, s := range h.Shared {
		if i == 0 || s.GroupLength < minGroup {
			minGroup = s.GroupLength
		}
		if s.GroupLength > maxGroup {
			maxGroup = s.GroupLength
		}
	}
	nbitsGroup := nbits(maxGroup - minGroup)

	// Table F.5 items 1-7. Item 5, the bits needed for a group's object count
	// minus one, is 0 because every group here holds exactly one object.
	w.uint32(h.FirstSharedObj)
	w.uint32(h.FirstSharedOffset)
	w.uint32(h.NSharedFirstPage)
	w.uint32(len(h.Shared))
	w.uint16(0)
	w.uint32(minGroup)
	w.uint16(nbitsGroup)

	for _, s := range h.Shared {
		w.bits(s.GroupLength-minGroup, nbitsGroup)
	}
	w.flush()
	// Signature present: one bit per group, always 0. Annex F.4.5 makes the
	// signature optional and nothing consumes it.
	for range h.Shared {
		w.bits(0, 1)
	}
	w.flush()
	// Object count minus one: zero-width, see item 5 above.
	w.flush()
}

func extent[T any](xs []T, f func(T) int) (lo, hi int) {
	for i, x := range xs {
		v := f(x)
		if i == 0 || v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// nbits is the number of bits needed to represent v, with nbits(0) == 0.
//
// The zero case is not a rounding choice: a column whose values are all equal
// to the header minimum has width 0 and occupies no bytes, and an encoder that
// returned 1 there would emit an extra flush byte per column and three wrong
// header fields on a single-page document.
func nbits(v int) int {
	n := 0
	for v > 0 {
		n++
		v >>= 1
	}
	return n
}

// bitWriter appends fixed-width values MSB-first.
type bitWriter struct {
	out  []byte
	acc  uint64
	nAcc uint // bits currently held in acc, always < 8 outside bits()
}

func (w *bitWriter) bits(v, n int) {
	if n <= 0 {
		return
	}
	for i := n - 1; i >= 0; i-- {
		w.acc = w.acc<<1 | uint64((v>>uint(i))&1)
		w.nAcc++
		if w.nAcc == 8 {
			w.out = append(w.out, byte(w.acc))
			w.acc, w.nAcc = 0, 0
		}
	}
}

// flush pads the accumulator out to a byte boundary with zeroes. It is a no-op
// on an already-aligned writer, which is what makes it safe to call after a
// zero-width column.
func (w *bitWriter) flush() {
	if w.nAcc == 0 {
		return
	}
	w.out = append(w.out, byte(w.acc<<(8-w.nAcc)))
	w.acc, w.nAcc = 0, 0
}

func (w *bitWriter) uint32(v int) {
	w.flush()
	w.out = binary.BigEndian.AppendUint32(w.out, uint32(v)) //nolint:gosec // header fields are non-negative and bounded by file size
}

func (w *bitWriter) uint16(v int) {
	w.flush()
	w.out = binary.BigEndian.AppendUint16(w.out, uint16(v)) //nolint:gosec // bit widths and counts are small
}
