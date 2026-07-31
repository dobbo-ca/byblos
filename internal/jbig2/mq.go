package jbig2

// qeEntry is one row of T.88 Table E.1: the LPS probability estimate and the
// index transitions taken after an MPS or an LPS renormalisation.
type qeEntry struct {
	qe               uint32
	nmps, nlps, swch uint8
}

// qeTable is T.88 Table E.1 in full. Index 46 is the fixed 0.5 estimate.
var qeTable = [47]qeEntry{
	{0x5601, 1, 1, 1}, {0x3401, 2, 6, 0}, {0x1801, 3, 9, 0}, {0x0AC1, 4, 12, 0},
	{0x0521, 5, 29, 0}, {0x0221, 38, 33, 0}, {0x5601, 7, 6, 1}, {0x5401, 8, 14, 0},
	{0x4801, 9, 14, 0}, {0x3801, 10, 14, 0}, {0x3001, 11, 17, 0}, {0x2401, 12, 18, 0},
	{0x1C01, 13, 20, 0}, {0x1601, 29, 21, 0}, {0x5601, 15, 14, 1}, {0x5401, 16, 14, 0},
	{0x5101, 17, 15, 0}, {0x4801, 18, 16, 0}, {0x3801, 19, 17, 0}, {0x3401, 20, 18, 0},
	{0x3001, 21, 19, 0}, {0x2801, 22, 19, 0}, {0x2401, 23, 20, 0}, {0x2201, 24, 21, 0},
	{0x1C01, 25, 22, 0}, {0x1801, 26, 23, 0}, {0x1601, 27, 24, 0}, {0x1401, 28, 25, 0},
	{0x1201, 29, 26, 0}, {0x1101, 30, 27, 0}, {0x0AC1, 31, 28, 0}, {0x09C1, 32, 29, 0},
	{0x08A1, 33, 30, 0}, {0x0521, 34, 31, 0}, {0x0441, 35, 32, 0}, {0x02A1, 36, 33, 0},
	{0x0221, 37, 34, 0}, {0x0141, 38, 35, 0}, {0x0111, 39, 36, 0}, {0x0085, 40, 37, 0},
	{0x0049, 41, 38, 0}, {0x0025, 42, 39, 0}, {0x0015, 43, 40, 0}, {0x0009, 44, 41, 0},
	{0x0005, 45, 42, 0}, {0x0001, 45, 43, 0}, {0x5601, 46, 46, 0},
}

// contexts holds the adaptive state for a set of coding contexts, one byte
// each, packed as index<<1 | mps. T.88 7.4.6.4 step 2 requires these to be
// zeroed at the start of every generic region segment, so callers allocate a
// fresh slice per segment rather than reusing one.
type contexts []byte

// encoder is the MQ arithmetic encoder of T.88 Annex E.2.
//
// out carries a one-byte leading sentinel so that bp can start at the E.2.8
// "BPST - 1" position without a negative index; flush slices it off. The carry
// path in byteout increments out[bp] in place, which is exactly the "propagate
// the carry into the previously written byte" behaviour of Figure E.9.
type encoder struct {
	c   uint32 // code register
	a   uint32 // interval register, 16-bit
	ct  int    // bits remaining before the next byte is emitted
	out []byte
	bp  int
}

// newEncoder performs INITENC (T.88 E.2.8, Figure E.10).
func newEncoder() *encoder {
	// The sentinel byte is 0, never 0xFF, so CT starts at 12 rather than 13.
	return &encoder{a: 0x8000, c: 0, ct: 12, out: []byte{0}, bp: 0}
}

func (e *encoder) b() byte     { return e.out[e.bp] }
func (e *encoder) setB(v byte) { e.out[e.bp] = v }

func (e *encoder) incBP() {
	e.bp++
	if e.bp == len(e.out) {
		e.out = append(e.out, 0)
	}
}

// byteout is T.88 E.2.7, Figure E.9.
func (e *encoder) byteout() {
	if e.b() == 0xFF {
		e.stuff()
		return
	}
	if e.c < 0x8000000 {
		e.normal()
		return
	}
	// Carry out of the code register: propagate into the previous byte.
	e.setB(e.b() + 1)
	if e.b() == 0xFF {
		e.c &= 0x7FFFFFF
		e.stuff()
		return
	}
	e.normal()
}

// stuff emits 7 bits after a 0xFF byte, which is what prevents a 0xFF 0x90..0xFF
// marker sequence from ever appearing in the coded data.
func (e *encoder) stuff() {
	e.incBP()
	e.setB(byte(e.c >> 20))
	e.c &= 0xFFFFF
	e.ct = 7
}

func (e *encoder) normal() {
	e.incBP()
	e.setB(byte(e.c >> 19))
	e.c &= 0x7FFFF
	e.ct = 8
}

// renorm is RENORME (T.88 E.2.6, Figure E.8).
func (e *encoder) renorm() {
	for {
		e.a <<= 1
		e.c <<= 1
		e.ct--
		if e.ct == 0 {
			e.byteout()
		}
		if e.a&0x8000 != 0 {
			return
		}
	}
}

// encode codes decision d (0 or 1) in context i. This is CODE0/CODE1
// (T.88 E.2.3) dispatching to CODEMPS (Figure E.6) or CODELPS (Figure E.7).
func (e *encoder) encode(cx contexts, i, d int) {
	st := cx[i]
	q := qeTable[st>>1]
	mps := st & 1

	if uint8(d) == mps {
		// CODEMPS. The conditional exchange can only matter when the interval
		// needs renormalising, which is why it sits inside this branch.
		e.a -= q.qe
		if e.a&0x8000 == 0 {
			if e.a < q.qe {
				e.a = q.qe
			} else {
				e.c += q.qe
			}
			cx[i] = q.nmps<<1 | mps
			e.renorm()
			return
		}
		e.c += q.qe
		return
	}

	// CODELPS.
	e.a -= q.qe
	if e.a < q.qe {
		e.c += q.qe
	} else {
		e.a = q.qe
	}
	if q.swch == 1 {
		mps = 1 - mps
	}
	cx[i] = q.nlps<<1 | mps
	e.renorm()
}

// flush is FLUSH (T.88 E.2.9, Figure E.11) including SETBITS (Figure E.12).
// It returns the complete coded segment, always terminated by 0xFF 0xAC.
//
// The optional trailing-0x7FFF removal of E.2.10 is deliberately NOT applied.
// It saves at most two bytes per region and is the only legitimate source of
// byte-level variation between two correct encoders; omitting it is what makes
// this encoder reproduce the Annex H.1 and H.2 vectors byte for byte.
func (e *encoder) flush() []byte {
	tempc := e.c + e.a
	e.c |= 0xFFFF
	if e.c >= tempc {
		e.c -= 0x8000
	}
	e.c <<= uint(e.ct)
	e.byteout()
	e.c <<= uint(e.ct)
	e.byteout()
	if e.b() != 0xFF {
		e.incBP()
		e.setB(0xFF)
	}
	e.incBP()
	e.setB(0xAC)
	return e.out[1 : e.bp+1]
}
