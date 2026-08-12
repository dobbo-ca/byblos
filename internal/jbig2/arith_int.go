package jbig2

// The arithmetic integer decoding procedure of T.88 Annex A, and the symbol ID
// decoding procedure of A.3. Everything a symbol dictionary or a text region
// reads that is not a bitmap pixel comes through here: symbol heights and
// widths, strip and instance coordinates, export run lengths, symbol IDs.
//
// IT IS A SECOND ARITHMETIC ALPHABET OVER THE SAME MQ DECODER. The generic
// region procedure spends one decision per pixel in a context taken from the
// pixels around it; this spends a handful of decisions per integer in a context
// taken from the bits it has already decoded. Both run on the same decoder and
// the same coded bytes, interleaved, which is why the decoder is passed in
// rather than constructed here: a symbol dictionary reads an integer, then a
// bitmap, then another integer, all from one MQ decoder whose state carries
// across. Constructing a decoder per field would restart the code register and
// decode noise from the second field onwards.

// intLengths is the prefix-to-length table of T.88 A.2 steps 2 and 3: after the
// sign bit, up to five more decisions select how many value bits follow and what
// offset they carry.
//
// THE OFFSETS ARE CUMULATIVE, NOT POWERS OF TWO. Each is the previous offset
// plus the number of values the previous length could express -- 0, 0+4, 4+16,
// 20+64, 84+256, 340+4096 -- so the six ranges tile the integers without a gap
// or an overlap. Writing 1<<n there instead is the obvious mistake and it
// decodes every value above 3 wrongly.
var intLengths = [6]struct{ bits, offset int }{
	{2, 0}, {4, 4}, {6, 20}, {8, 84}, {12, 340}, {32, 4436},
}

// newIntContexts returns one integer decoding context: 512 states, one per value
// of the PREV register of T.88 A.2.
//
// Each of the procedure's uses -- IADH, IADW, IAEX, IAAI, IADT, IAFS, IADS,
// IAIT, IARI, IARDW, IARDH, IARDX, IARDY -- gets its own and they are never
// shared, because the whole point of the register is that the statistics of a
// symbol height are not the statistics of a text coordinate.
func newIntContexts() contexts { return make(contexts, 512) }

// decodeInt is the integer arithmetic decoding procedure of T.88 A.2. The second
// return is false for OOB.
//
// OOB IS NOT A NUMBER AND IS NOT A SENTINEL INTEGER. The procedure yields it
// where a value would be meaningless, and the two places that receive it -- the
// width loop of a symbol dictionary height class (6.5.5) and the S coordinate
// loop of a text region strip (6.4.5) -- have no other signal that the run has
// ended. Every value in the 32-bit range is a legal decode, so no integer is
// free to mean this and it is returned beside the value instead.
//
// PREV IS CLAMPED AT 256 (A.2 step 3, final paragraph). Once it reaches nine
// bits the top bit is pinned and the low eight rotate, so the context array
// stays 512 entries however long the value is. Dropping the clamp indexes out of
// bounds on the tenth decision, which is a panic on a stream that is merely
// large rather than malformed.
func decodeInt(d *decoder, cx contexts) (int, bool) {
	prev := 1
	bit := func() int {
		b := d.decode(cx, prev)
		if prev < 256 {
			prev = prev<<1 | b
		} else {
			prev = ((prev<<1 | b) & 511) | 256
		}
		return b
	}

	sign := bit()
	// The last row needs no selector bit of its own: five 1 bits have already
	// selected it, so the loop below leaves the table's final entry in place.
	sel := intLengths[len(intLengths)-1]
	for _, l := range intLengths[:len(intLengths)-1] {
		if bit() == 0 {
			sel = l
			break
		}
	}

	v := 0
	for range sel.bits {
		v = v<<1 | bit()
	}
	v += sel.offset

	// A.2 step 4: a negative zero is OOB, not the number zero.
	if sign == 1 {
		if v == 0 {
			return 0, false
		}
		return -v, true
	}
	return v, true
}

// decodeIAID is the symbol ID decoding procedure of T.88 A.3: codeLen decisions
// read down a binary tree of contexts, with the running prefix as the index.
//
// The context array is 1<<(codeLen+1) entries and belongs to the region or
// dictionary that owns it, not to this call: the tree is adaptive across every
// symbol the region places, which is where its compression comes from.
//
// codeLen == 0 reads NOTHING and returns 0. See symCodeLen.
func decodeIAID(d *decoder, cx contexts, codeLen int) int {
	prev := 1
	for range codeLen {
		prev = prev<<1 | d.decode(cx, prev)
	}
	return prev - 1<<codeLen
}
