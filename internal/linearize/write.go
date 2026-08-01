package linearize

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
	"io"
)

// Assembling the file (ISO 32000-1:2008 Annex F.3).
//
// THE CHICKEN AND EGG. The linearization parameter dictionary states the total
// file length, the offset and length of the hint stream, and the offset of the
// end of the first-page section -- none of which are known until the file has
// been laid out, and the dictionary is itself part of the layout. Both
// cross-reference sections have the same problem.
//
// The reference implementation resolves it by emitting the whole file twice.
// Byblos emits it once, because it emits classic cross-reference tables and no
// object streams, and that removes every source of pass-to-pass drift qpdf has
// to absorb (deflate size, xref-stream field widths, digit growth in a
// compressed stream's /Length). What is left is:
//
//   - Every unknown value sits in a slot whose WIDTH is known before the first
//     byte is written: 20 bytes per cross-reference entry (7.5.4), 201 bytes
//     for the parameter dictionary, 21 for the front trailer's /Prev, and a
//     /Size that is known from the plan. So back-patching never changes a byte
//     count.
//   - The hint stream is left out of the layout entirely and spliced in
//     afterwards at a recorded offset, and every offset at or past that point
//     is corrected by exactly its length. That is sound because Annex F.4
//     defines every offset STORED IN A HINT TABLE as measured with the hint
//     stream absent -- so the hint tables are computed from the pre-splice
//     layout and the splice does not invalidate them.
//
// There is therefore no fixed point to iterate to: the finished length is the
// pre-splice length plus the hint stream's length, and that is the value
// written into /L.

const (
	// linDictSlot is the fixed size of part 2, copied from the reference
	// implementation: 200 bytes of content padded with spaces, then a newline.
	// Measured on two independently produced linearized files, the parameter
	// dictionary starts at offset 15 and the next part starts at 216.
	linDictSlot = 201

	// prevSlot is the fixed width of the front trailer's /Prev value.
	prevSlot = 21

	// xrefEntry is the size ISO 32000-1 7.5.4 fixes for a cross-reference
	// entry: exactly 20 bytes, including the two-character end-of-line.
	xrefEntry = 20
)

// Meta is what the assembled file needs that is not structural.
type Meta struct {
	Version string    // e.g. "1.5"; the %PDF- header. "1.4" when empty.
	ID      [2][]byte // the trailer /ID; omitted when the first half is nil
}

// Write assembles the linearized file.
//
// bodies is keyed by NEW object number and holds each object's serialized bytes
// WITHOUT the "N 0 obj" / "endobj" wrapper, with every reference already
// rewritten to new numbers. A stream's body is its dictionary, then "\nstream\n",
// then the raw payload, then "\nendstream" -- the shape pdfcpu's own writer
// produces, so an object that round-trips through this package is byte-identical
// to one that did not.
func Write(w io.Writer, p Plan, bodies map[int][]byte, m Meta) error {
	if len(p.Part6) == 0 {
		return fmt.Errorf("linearize: the first-page section is empty")
	}

	var buf []byte
	starts := make(map[int]int, len(bodies))
	ends := make(map[int]int, len(bodies))
	emit := func(n int) error {
		body, ok := bodies[n]
		if !ok {
			return fmt.Errorf("linearize: no body for object %d", n)
		}
		starts[n] = len(buf)
		buf = fmt.Appendf(buf, "%d 0 obj\n", n)
		buf = append(buf, body...)
		buf = append(buf, "\nendobj\n"...)
		ends[n] = len(buf)
		return nil
	}

	// Part 1. The four bytes above 127 are what tells a transfer agent the file
	// is binary (7.5.2).
	version := m.Version
	if version == "" {
		version = "1.4"
	}
	buf = fmt.Appendf(buf, "%%PDF-%s\n", version)
	buf = append(buf, "%\xE2\xE3\xCF\xD3\n"...)

	// Part 2, reserved and filled in at the end. It has no body in bodies -- it
	// is this function's own object -- so its offset is recorded by hand.
	linOff := len(buf)
	buf = append(buf, bytes.Repeat([]byte{' '}, linDictSlot)...)
	starts[p.LinDict] = linOff

	// Part 3. F.3.4: one subsection, no free entries, covering exactly the
	// first-group objects, which are one ascending run by construction.
	firstGroup := append([]int{p.LinDict}, p.Part4...)
	firstGroup = append(firstGroup, p.Hint)
	firstGroup = append(firstGroup, p.Part6...)
	for i, n := range firstGroup {
		if n != p.LinDict+i {
			return fmt.Errorf("linearize: the first object group is not contiguous: "+
				"object %d is at index %d of a run starting at %d", n, i, p.LinDict)
		}
	}
	xref3Off := len(buf)
	buf = fmt.Appendf(buf, "xref\n%d %d\n", firstGroup[0], len(firstGroup))
	frontEntry := make([]int, len(firstGroup))
	for i := range firstGroup {
		frontEntry[i] = len(buf)
		buf = append(buf, "0000000000 00000 n \n"...)
	}
	id := idLiteral(m.ID)
	trailer := fmt.Sprintf("trailer\n<< /Size %d /Root %d 0 R", p.Size, p.Catalog)
	if p.Info != 0 {
		trailer += fmt.Sprintf(" /Info %d 0 R", p.Info)
	}
	trailer += " /Prev "
	buf = append(buf, trailer...)
	prevPos := len(buf)
	buf = append(buf, bytes.Repeat([]byte{' '}, prevSlot)...)
	if id != "" {
		buf = append(buf, " /ID "...)
		buf = append(buf, id...)
	}
	// F.3.4 makes this startxref optional and says its value shall be ignored;
	// the reference implementation writes a literal 0 and so does this.
	buf = append(buf, " >>\nstartxref\n0\n%%EOF\n"...)

	// Part 4.
	for _, n := range p.Part4 {
		if err := emit(n); err != nil {
			return err
		}
	}

	// Part 5 goes here, and is spliced in later. Everything from this offset on
	// moves by the hint stream's length.
	hintOff := len(buf)

	// Part 6.
	for _, n := range p.Part6 {
		if err := emit(n); err != nil {
			return err
		}
	}
	endFirstPage := len(buf)

	// Parts 7, 8 and 9.
	for _, grp := range p.Part7 {
		for _, n := range grp {
			if err := emit(n); err != nil {
				return err
			}
		}
	}
	for _, n := range p.Part8 {
		if err := emit(n); err != nil {
			return err
		}
	}
	for _, n := range p.Part9 {
		if err := emit(n); err != nil {
			return err
		}
	}

	// Part 11. F.3.11: one subsection beginning at object 0, covering the
	// second object group only, and a trailer with no /Prev. A reader that
	// knows nothing about linearization sees an ordinary incremental update.
	secondGroup := p.LinDict - 1
	xrefMainOff := len(buf)
	buf = fmt.Appendf(buf, "xref\n0 %d", secondGroup+1)
	// Table F.1 defines /T for a classic table as the offset of the white-space
	// character preceding the entry for object 0 -- which is this byte, not the
	// offset of the "xref" line.
	spaceBeforeZero := len(buf)
	buf = append(buf, '\n')
	buf = append(buf, "0000000000 65535 f \n"...)
	mainEntry := make([]int, secondGroup)
	for i := range mainEntry {
		mainEntry[i] = len(buf)
		buf = append(buf, "0000000000 00000 n \n"...)
	}
	trailer2 := fmt.Sprintf("trailer\n<< /Size %d", secondGroup+1)
	if id != "" {
		trailer2 += " /ID " + id
	}
	trailer2 += fmt.Sprintf(" >>\nstartxref\n%d\n", xref3Off)
	buf = append(buf, trailer2...)
	buf = append(buf, "%%EOF\n"...)
	preSpliceLen := len(buf)

	// The hint tables, computed from the layout as it stands -- that is, with
	// the hint stream absent, which is exactly how Annex F.4 defines every
	// offset they store.
	hintObj, err := buildHint(p, starts, ends, hintOff, endFirstPage)
	if err != nil {
		return err
	}
	hintLen := len(hintObj)

	out := make([]byte, 0, preSpliceLen+hintLen)
	out = append(out, buf[:hintOff]...)
	out = append(out, hintObj...)
	out = append(out, buf[hintOff:]...)

	// Every offset at or past the splice point moves by the hint stream's
	// length. The hint stream itself lands exactly on the splice point.
	shift := func(off int) int {
		if off >= hintOff {
			return off + hintLen
		}
		return off
	}

	lin := fmt.Sprintf("%d 0 obj\n<< /Linearized 1 /L %d /H [ %d %d ] /O %d /E %d /N %d /T %d >>\nendobj\n",
		p.LinDict, preSpliceLen+hintLen, hintOff, hintLen,
		p.Part6[0], shift(endFirstPage), p.NPages, shift(spaceBeforeZero))
	if len(lin) > linDictSlot-1 {
		return fmt.Errorf("linearize: the parameter dictionary is %d bytes and the "+
			"reserved slot is %d", len(lin), linDictSlot-1)
	}
	copy(out[linOff:], lin)
	out[linOff+linDictSlot-1] = '\n'

	for i, n := range firstGroup {
		off := hintOff
		if n != p.Hint {
			off = shift(starts[n])
		}
		putOffset(out, frontEntry[i], off)
	}
	prev := fmt.Sprintf("%d", shift(xrefMainOff))
	if len(prev) > prevSlot {
		return fmt.Errorf("linearize: /Prev is %d bytes and the reserved slot is %d",
			len(prev), prevSlot)
	}
	copy(out[prevPos:], prev)

	for i := range mainEntry {
		n := i + 1
		putOffset(out, mainEntry[i]+hintLen, shift(starts[n]))
	}

	_, err = w.Write(out)
	return err
}

// buildHint encodes the primary hint stream and wraps it as an indirect object.
func buildHint(p Plan, starts, ends map[int]int, hintOff, endFirstPage int) ([]byte, error) {
	h := Hints{
		FirstPageOffset:  hintOff,
		NSharedFirstPage: len(p.Part6),
	}
	h.Pages = append(h.Pages, PageHint{
		NObjects: len(p.Part6),
		Length:   endFirstPage - hintOff,
	})
	for i, grp := range p.Part7 {
		if len(grp) == 0 {
			return nil, fmt.Errorf("linearize: page %d has no objects of its own", i+2)
		}
		h.Pages = append(h.Pages, PageHint{
			NObjects:  len(grp),
			Length:    ends[grp[len(grp)-1]] - starts[grp[0]],
			SharedIDs: p.PageShared[i+1],
		})
	}
	for _, n := range p.Part6 {
		h.Shared = append(h.Shared, SharedHint{GroupLength: ends[n] - starts[n]})
	}
	for _, n := range p.Part8 {
		h.Shared = append(h.Shared, SharedHint{GroupLength: ends[n] - starts[n]})
	}
	if len(p.Part8) > 0 {
		h.FirstSharedObj = p.Part8[0]
		h.FirstSharedOffset = starts[p.Part8[0]]
	}
	if len(p.Outlines) > 0 {
		// The group's length is measured from the /Outlines root to the far end
		// of the whole group, which is how the reference implementation
		// recomputes it (maxEnd over the outline object set, minus the root's
		// offset). The objects are contiguous, so that is the last one's end.
		end := 0
		for _, n := range p.Outlines {
			if ends[n] > end {
				end = ends[n]
			}
		}
		h.Outline = &OutlineHint{
			FirstObject:       p.Outlines[0],
			FirstObjectOffset: starts[p.Outlines[0]],
			NObjects:          len(p.Outlines),
			GroupLength:       end - starts[p.Outlines[0]],
		}
	}

	hint, err := EncodeHints(h)
	if err != nil {
		return nil, err
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(hint.Payload); err != nil {
		return nil, fmt.Errorf("linearize: deflating the hint stream: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("linearize: deflating the hint stream: %w", err)
	}

	// F.3.6: every value in the hint stream dictionary is direct, including
	// /Length.
	dict := fmt.Sprintf("<< /Filter /FlateDecode /S %d", hint.S)
	if hint.O != 0 {
		dict += fmt.Sprintf(" /O %d", hint.O)
	}
	dict += fmt.Sprintf(" /Length %d >>", z.Len())

	obj := fmt.Appendf(nil, "%d 0 obj\n%s\nstream\n", p.Hint, dict)
	obj = append(obj, z.Bytes()...)
	obj = append(obj, "\nendstream\nendobj\n"...)
	return obj, nil
}

// putOffset writes a cross-reference entry's ten-digit offset field in place.
// The field is fixed width, so this cannot change any other offset.
func putOffset(out []byte, pos, off int) {
	copy(out[pos:pos+xrefEntry], fmt.Sprintf("%010d", off))
}

func idLiteral(id [2][]byte) string {
	if len(id[0]) == 0 {
		return ""
	}
	second := id[1]
	if len(second) == 0 {
		second = id[0]
	}
	return "[<" + hex.EncodeToString(id[0]) + "><" + hex.EncodeToString(second) + ">]"
}
