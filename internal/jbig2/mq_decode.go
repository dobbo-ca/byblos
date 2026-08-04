package jbig2

// decoder is the MQ arithmetic decoder of T.88 Annex E.3, the exact inverse of
// encoder (mq.go). It shares qeTable and contexts with the encoder, so the two
// directions can never drift apart on the probability estimates or on the state
// packing (index<<1 | mps).
//
// c is the 32-bit code register with CHIGH in bits 16-31, which is why every
// comparison against Qe is written against c>>16 and every subtraction against
// Qe<<16. Bits shifted out of bit 31 are consumed and discarded; uint32 does
// that for free, as T.88's own 32-bit register does.
type decoder struct {
	c  uint32 // code register, CHIGH in bits 16-31
	a  uint32 // interval register, 16-bit
	ct int    // bits remaining in the current byte
	in []byte
	bp int
}

// at returns the coded byte at i, or 0xFF for any index outside in.
//
// T.88 E.3.4 ends the coded data at a marker -- 0xFF followed by a byte above
// 0x8F -- and from there feeds 1 bits forever, because FLUSH (E.2.9) guarantees
// the last real decision is already fully determined by the bytes present. A
// stream truncated before its 0xFF 0xAC terminator therefore reads as if the
// marker were there rather than panicking, which is what keeps a damaged
// embedded stream a decode error instead of a crash.
func (d *decoder) at(i int) byte {
	if i < 0 || i >= len(d.in) {
		return 0xFF
	}
	return d.in[i]
}

// newDecoder performs INITDEC (T.88 E.3.5, Figure E.20).
func newDecoder(in []byte) *decoder {
	d := &decoder{in: in}
	d.c = uint32(d.at(0)) << 16
	d.bytein()
	d.c <<= 7
	d.ct -= 7
	d.a = 0x8000
	return d
}

// bytein is BYTEIN (T.88 E.3.4, Figure E.19). The 0xFF branch is the mirror of
// the encoder's stuff(): seven bits after a 0xFF, so the decoder consumes the
// stuffing the encoder inserted rather than reading it as data.
func (d *decoder) bytein() {
	if d.at(d.bp) == 0xFF {
		if d.at(d.bp+1) > 0x8F {
			// A marker. bp does not advance again, so every subsequent call
			// takes this branch and shifts in 1 bits.
			d.c += 0xFF00
			d.ct = 8
			return
		}
		d.bp++
		d.c += uint32(d.at(d.bp)) << 9
		d.ct = 7
		return
	}
	d.bp++
	d.c += uint32(d.at(d.bp)) << 8
	d.ct = 8
}

// renorm is RENORMD (T.88 E.3.3, Figure E.18). Note the order against the
// encoder's renorm: the decoder tests CT before shifting, the encoder after.
func (d *decoder) renorm() {
	for {
		if d.ct == 0 {
			d.bytein()
		}
		d.a <<= 1
		d.c <<= 1
		d.ct--
		if d.a&0x8000 != 0 {
			return
		}
	}
}

// decode returns the decision coded in context i. This is DECODE (T.88 E.3.2,
// Figure E.17) with MPS_EXCHANGE (Figure E.16) and LPS_EXCHANGE (Figure E.15)
// inlined into their two call sites.
//
// The MPS-without-renormalisation path returns before touching cx, which is
// what makes the common case a subtract, a compare and a return.
func (d *decoder) decode(cx contexts, i int) int {
	st := cx[i]
	q := qeTable[st>>1]
	mps := st & 1

	d.a -= q.qe
	if d.c>>16 < q.qe {
		// The code register landed in the LPS sub-interval, which sits at the
		// bottom: LPS_EXCHANGE. The conditional exchange means this is not
		// necessarily an LPS decision.
		var bit uint8
		if d.a < q.qe {
			bit = mps
			cx[i] = q.nmps<<1 | mps
		} else {
			bit = 1 - mps
			if q.swch == 1 {
				mps = 1 - mps
			}
			cx[i] = q.nlps<<1 | mps
		}
		d.a = q.qe
		d.renorm()
		return int(bit)
	}

	d.c -= q.qe << 16
	if d.a&0x8000 != 0 {
		return int(mps)
	}

	// MPS_EXCHANGE.
	var bit uint8
	if d.a < q.qe {
		bit = 1 - mps
		if q.swch == 1 {
			mps = 1 - mps
		}
		cx[i] = q.nlps<<1 | mps
	} else {
		bit = mps
		cx[i] = q.nmps<<1 | mps
	}
	d.renorm()
	return int(bit)
}
