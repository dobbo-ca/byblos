package render

// Stage 4d (byb-8b9.4): bare CFF (Type1C) glyph outlines.
//
// SPIKE OUTCOME, recorded per the bead: x/image/font/sfnt DOES rasterise bare
// CFF once the blob is wrapped as the 'CFF ' table of a minimal OTTO
// container -- LoadGlyph returns cubic segments (SegmentOpCubeTo) in raw font
// units, exactly like the 4c TrueType path -- so this stage is a
// wrapper-builder plus a pre-Parse work gate, NOT a Type 2 charstring
// interpreter. The tables sfnt requires and bare CFF lacks are synthesised
// below (cffWrapSFNT): head/hhea/hmtx/maxp/post are inert (the PDF /Widths
// drive every advance, so hmtx is one zero metric), OS/2 is written at
// version 2 so sfnt.Parse's below-2 fallback never loads a glyph, and the
// cmap (format 6 over codes 0..255) is built from the CFF's OWN charset and
// encoding -- the builtin-encoding stance, 4d's analogue of 4c's Latin-1
// one: an /Encoding-aware mapping still waits for a caller that can supply
// it across the Font seam.
//
// WHY A PRE-PARSE WORK GATE (glyf.go's refuse-before-allocating discipline):
// sfnt's Type 2 interpreter caps subr NESTING at 10 and each stream at 64 KB,
// but nothing bounds the call TREE -- ten subrs each calling the next a few
// hundred times is a sub-KB font that executes ~200^9 charstring bytes
// without ever tripping sfnt's depth wall. Measured, gate bypassed: one
// LoadGlyph on that shape truncated to a chain of 3/4/5 subrs took 1.4 ms /
// 153 ms / 27 s, ~x150 per level, no error -- the full chain of 10 is
// unreachable CPU-years inside ONE glyph load. So before sfnt sees the font,
// cffWalkGlyph re-runs the CONTROL FLOW of every reachable glyph's
// charstring -- literal widths, subr calls under the standard bias, hint
// counting only so hintmask data bytes are skipped as data (hints affect
// grid-fitting, not outlines, so they are never executed) -- mirroring
// sfnt's psInterpreter byte for byte on the path sfnt would execute, and
// charging one unit per executed byte against maxCharstringWork. Only the
// glyphs the synthetic cmap can reach need walking, so the whole gate is at
// most 256 x maxCharstringWork steps. A font that trips it degrades to
// widths-only, exactly like an unparsable TrueType; the same bound also caps
// LoadGlyph's segment memory, since sfnt emits at most one segment per
// executed operator. Where the walk sees a byte sequence sfnt would REFUSE
// (a reserved operator, a bad subr index, stack over/underflow), it stops
// and admits: sfnt's own error lands at that exact byte at load time, so
// the work up to it -- already charged -- is all that font can ever cost.
//
// CID-KEYED CFF (byb-8b9.8, a Top DICT /ROS): the codes arrive as 2-byte
// CIDs through a Type0 font's Identity CMap (Font.Type0/showText carry the
// seam), CID->GID is the charset read as a CID map (cid2gid), and sfnt's own
// interpreter already resolves FDArray/FDSelect-partitioned private dicts --
// so the additions here are the FDArray/FDSelect parse the GATE needs to
// walk each glyph with the local subrs sfnt will actually run it under
// (cffFDSelect mirrors sfnt's lookup exactly; a divergence would let a bomb
// hide behind a crafted range table), plus a whole-font walk cap: with every
// glyph reachable, the 256-glyph bound above becomes 256 x maxCharstringWork
// TOTAL across the CharStrings INDEX, the same ceiling. Embedded CMap
// streams and predefined CJK CMaps stay behind the seam (the caller must not
// mark them Type0), and Identity-V renders with horizontal metrics -- both
// deferred on the epic's population data; see Font in text.go.

import (
	"math"
	"strconv"
)

// maxCharstringWork bounds the charstring bytes ONE glyph's outline may
// execute, subr expansion included -- sfnt's own per-stream cap is 64 KB, so
// this admits any glyph a single unamplified stream could describe while
// bounding what subr fan-out can amplify it to. A var so tests can lower it;
// production code never writes it.
var maxCharstringWork int64 = 1 << 16

// cffProgram is the parsed skeleton of a bare CFF: enough structure to gate
// interpreter work and map codes to glyphs. Outline semantics stay in sfnt.
type cffProgram struct {
	charstrings    [][]byte
	gsubrs, lsubrs [][]byte
	code2gid       [256]uint16
	upem           uint16
	// CID-keyed only (byb-8b9.8): cid2gid is the charset read as a CID map,
	// and fdSubrs/fdSel replace lsubrs, partitioned by FDSelect.
	cid2gid map[uint16]uint16
	fdSubrs [][][]byte
	fdSel   []int16
}

// cffIndex reads the INDEX at p[pos:]: count, offSize, count+1 one-based
// offsets, then the data. nil items with ok=true is the empty INDEX (a bare
// zero count).
func cffIndex(p []byte, pos int) (items [][]byte, next int, ok bool) {
	// len(p)-pos, not pos+n: a huge pos must not wrap the sum negative and
	// slip past the bound (a real-encoded DICT operand can reach 2^63).
	if pos < 0 || len(p)-pos < 2 {
		return nil, 0, false
	}
	count := int(be.Uint16(p[pos:]))
	if count == 0 {
		return nil, pos + 2, true
	}
	if len(p)-pos < 3 {
		return nil, 0, false
	}
	offSize := int(p[pos+2])
	if offSize < 1 || offSize > 4 {
		return nil, 0, false
	}
	offPos, dataPos := pos+3, pos+3+(count+1)*offSize
	if dataPos > len(p) {
		return nil, 0, false
	}
	readOff := func(i int) int64 {
		v := int64(0)
		for _, b := range p[offPos+i*offSize : offPos+(i+1)*offSize] {
			v = v<<8 | int64(b)
		}
		return v
	}
	prev := readOff(0)
	if prev != 1 {
		return nil, 0, false
	}
	for i := 1; i <= count; i++ {
		off := readOff(i)
		if off < prev || int64(dataPos)-1+off > int64(len(p)) {
			return nil, 0, false
		}
		items = append(items, p[int64(dataPos)-1+prev:int64(dataPos)-1+off])
		prev = off
	}
	return items, dataPos - 1 + int(prev), true
}

// cffDict parses a DICT into operator -> operands (a 12-escaped operator x is
// keyed 1200+x). Unknown operators are kept -- only malformed operands (or a
// truncated escape) refuse.
func cffDict(d []byte) (map[int][]float64, bool) {
	dict := map[int][]float64{}
	var args []float64
	for i := 0; i < len(d); {
		b := d[i]
		switch {
		case b == 28:
			if i+3 > len(d) {
				return nil, false
			}
			args = append(args, float64(int16(be.Uint16(d[i+1:]))))
			i += 3
		case b == 29:
			if i+5 > len(d) {
				return nil, false
			}
			args = append(args, float64(int32(be.Uint32(d[i+1:]))))
			i += 5
		case b == 30: // real: nibble-encoded decimal
			s := make([]byte, 0, 16)
			i++
			for done := false; !done; {
				if i >= len(d) || len(s) > 64 {
					return nil, false
				}
				for _, nib := range [2]byte{d[i] >> 4, d[i] & 0x0f} {
					switch {
					case nib <= 9:
						s = append(s, '0'+nib)
					case nib == 0x0a:
						s = append(s, '.')
					case nib == 0x0b:
						s = append(s, 'E')
					case nib == 0x0c:
						s = append(s, 'E', '-')
					case nib == 0x0e:
						s = append(s, '-')
					case nib == 0x0f:
						done = true
					default:
						return nil, false
					}
					if done {
						break
					}
				}
				i++
			}
			// strconv accepts the exact grammar the nibbles produce
			// ("-2.25", "0.140541E-3"); a bare or doubled marker errors.
			v, err := strconv.ParseFloat(string(s), 64)
			if err != nil {
				return nil, false
			}
			args = append(args, v)
		case b >= 32 && b <= 246:
			args = append(args, float64(int(b)-139))
			i++
		case b >= 247 && b <= 250:
			if i+2 > len(d) {
				return nil, false
			}
			args = append(args, float64(int(b-247)*256+int(d[i+1])+108))
			i += 2
		case b >= 251 && b <= 254:
			if i+2 > len(d) {
				return nil, false
			}
			args = append(args, float64(-int(b-251)*256-int(d[i+1])-108))
			i += 2
		case b == 12:
			if i+2 > len(d) {
				return nil, false
			}
			dict[1200+int(d[i+1])] = args
			args = nil
			i += 2
		case b <= 21:
			dict[int(b)] = args
			args = nil
			i++
		default:
			return nil, false // 22..27, 31: reserved
		}
		if len(args) > 48 {
			return nil, false
		}
	}
	return dict, true
}

// cffStdEncHigh is the above-ASCII half of the CFF Standard Encoding
// (5176.CFF.pdf Appendix B): code -> SID. Codes 32..126 are SIDs 1..95 in
// order and live in cffStandardSID's arithmetic instead.
var cffStdEncHigh = map[byte]uint16{
	161: 96, 162: 97, 163: 98, 164: 99, 165: 100, 166: 101, 167: 102,
	168: 103, 169: 104, 170: 105, 171: 106, 172: 107, 173: 108, 174: 109,
	175: 110, 177: 111, 178: 112, 179: 113, 180: 114, 182: 115, 183: 116,
	184: 117, 185: 118, 186: 119, 187: 120, 188: 121, 189: 122, 191: 123,
	193: 124, 194: 125, 195: 126, 196: 127, 197: 128, 198: 129, 199: 130,
	200: 131, 202: 132, 203: 133, 205: 134, 206: 135, 207: 136, 208: 137,
	225: 138, 227: 139, 232: 140, 233: 141, 234: 142, 235: 143, 241: 144,
	245: 145, 248: 146, 249: 147, 250: 148, 251: 149,
}

// cffStandardSID maps a byte code to its Standard Encoding SID (0 = .notdef).
func cffStandardSID(code byte) uint16 {
	if code >= 32 && code <= 126 {
		return uint16(code) - 31
	}
	return cffStdEncHigh[code]
}

// cffCharsetSIDs returns the SID of every glyph. offset 0 is the predefined
// ISOAdobe charset, whose SID IS the glyph index; the Expert charsets (1, 2)
// and unknown formats refuse -- better widths-only than wrong glyphs.
func cffCharsetSIDs(p []byte, offset, numGlyphs int) []uint16 {
	sids := make([]uint16, numGlyphs)
	if offset == 0 {
		for i := range sids {
			sids[i] = uint16(i)
		}
		return sids
	}
	if offset == 1 || offset == 2 || offset >= len(p) {
		return nil
	}
	d, gid := p[offset:], 1
	switch d[0] {
	case 0:
		for i := 1; gid < numGlyphs; gid++ {
			if i+2 > len(d) {
				return nil
			}
			sids[gid] = be.Uint16(d[i:])
			i += 2
		}
	case 1, 2:
		rangeLen := 3 // format 1: sid u16, nLeft u8
		if d[0] == 2 {
			rangeLen = 4 // format 2: nLeft u16
		}
		for i := 1; gid < numGlyphs; i += rangeLen {
			if i+rangeLen > len(d) {
				return nil
			}
			first, nLeft := be.Uint16(d[i:]), int(d[i+2])
			if rangeLen == 4 {
				nLeft = int(be.Uint16(d[i+2:]))
			}
			for k := 0; k <= nLeft && gid < numGlyphs; k++ {
				sids[gid] = first + uint16(k)
				gid++
			}
		}
	default:
		return nil
	}
	return sids
}

// cffCode2GID builds the code-to-glyph table from the font's builtin
// encoding. offset 0 is the Standard Encoding, resolved through the charset
// (code -> SID -> GID); offset 1 is Expert, refused like the Expert charsets;
// otherwise a custom format 0 or 1 table maps codes onto glyph order
// directly, with the optional supplement resolving through SIDs again.
func cffCode2GID(p []byte, offset int, sids []uint16) *[256]uint16 {
	sid2gid := map[uint16]uint16{}
	for gid := len(sids) - 1; gid >= 1; gid-- { // first glyph wins
		sid2gid[sids[gid]] = uint16(gid)
	}
	var codes [256]uint16
	if offset == 0 {
		for c := range codes {
			codes[c] = sid2gid[cffStandardSID(byte(c))]
		}
		return &codes
	}
	if offset == 1 || offset >= len(p) {
		return nil
	}
	d, i := p[offset:], 1
	switch d[0] & 0x7f {
	case 0:
		if len(d) < 2 {
			return nil
		}
		n := int(d[1])
		if i += 1; i+n > len(d) {
			return nil
		}
		for gid := 1; gid <= n; gid++ {
			codes[d[i+gid-1]] = uint16(gid)
		}
		i += n
	case 1:
		if len(d) < 2 {
			return nil
		}
		nRanges := int(d[1])
		if i += 1; i+2*nRanges > len(d) {
			return nil
		}
		gid := 1
		for r := 0; r < nRanges; r++ {
			first, nLeft := int(d[i+2*r]), int(d[i+2*r+1])
			for k := 0; k <= nLeft; k, gid = k+1, gid+1 {
				if first+k > 255 || gid > 0xffff {
					return nil
				}
				if gid < len(sids) {
					codes[first+k] = uint16(gid)
				}
			}
		}
		i += 2 * nRanges
	default:
		return nil
	}
	if d[0]&0x80 != 0 { // supplements: code u8, SID u16
		if i >= len(d) {
			return nil
		}
		n := int(d[i])
		i++
		if i+3*n > len(d) {
			return nil
		}
		for s := 0; s < n; s++ {
			codes[d[i+3*s]] = sid2gid[be.Uint16(d[i+3*s+1:])]
		}
	}
	return &codes
}

// parseCFF parses the container structure of a bare CFF, plain or CID-keyed.
// nil means the program cannot safely take the CFF path -- malformed, or an
// Expert charset/encoding -- and the caller degrades to widths-only.
func parseCFF(p []byte) *cffProgram {
	if len(p) < 4 || p[0] != 1 {
		return nil
	}
	_, pos, ok := cffIndex(p, int(p[2])) // Name INDEX
	if !ok {
		return nil
	}
	tops, pos, ok := cffIndex(p, pos)
	if !ok || len(tops) == 0 {
		return nil
	}
	_, pos, ok = cffIndex(p, pos) // String INDEX
	if !ok {
		return nil
	}
	gsubrs, _, ok := cffIndex(p, pos)
	if !ok {
		return nil
	}
	top, ok := cffDict(tops[0])
	if !ok {
		return nil
	}
	_, cidKeyed := top[1230] // ROS
	c := &cffProgram{gsubrs: gsubrs, upem: 1000}
	if m := top[1207]; m != nil { // FontMatrix: upem = 1/m[0]
		// Only the uniform axis-aligned [s 0 0 s 0 0] form maps onto sfnt's
		// single unitsPerEm; skew, non-uniform scale, or translation would
		// render silently at the wrong shape, so refuse to widths-only.
		if len(m) < 6 || !(m[0] > 0) || m[1] != 0 || m[2] != 0 ||
			m[3] != m[0] || m[4] != 0 || m[5] != 0 {
			return nil
		}
		u := math.Round(1 / m[0])
		if !(u >= 1 && u <= 65535) {
			return nil
		}
		c.upem = uint16(u)
	}
	intOp := func(op int) (int, bool) {
		v := top[op]
		if len(v) != 1 {
			return 0, false
		}
		if v[0] != math.Trunc(v[0]) || v[0] < 0 || v[0] > float64(len(p)) {
			return 0, false
		}
		return int(v[0]), true
	}
	csOff, ok := intOp(17)
	if !ok {
		return nil
	}
	c.charstrings, _, ok = cffIndex(p, csOff)
	if !ok || len(c.charstrings) == 0 {
		return nil
	}
	if cidKeyed {
		// CID-keyed (byb-8b9.8): FDArray font DICTs carry the Private DICTs
		// (partitioned by FDSelect) in place of the single top-level one,
		// which sfnt ignores here and so does this parse. Both operators are
		// required -- sfnt's Parse errors without them, so refusing early
		// just skips a gate walk whose font could never render.
		fdaOff, ok := intOp(1236) // FDArray
		if !ok {
			return nil
		}
		fds, _, ok := cffIndex(p, fdaOff)
		if !ok || len(fds) == 0 {
			return nil
		}
		for _, fd := range fds {
			d, ok := cffDict(fd)
			if !ok {
				return nil
			}
			subrs, ok := cffPrivateSubrs(p, d)
			if !ok {
				return nil
			}
			c.fdSubrs = append(c.fdSubrs, subrs)
		}
		fdsOff, ok := intOp(1237) // FDSelect
		if !ok {
			return nil
		}
		if c.fdSel = cffFDSelect(p, fdsOff, len(c.charstrings), len(c.fdSubrs)); c.fdSel == nil {
			return nil
		}
	} else if c.lsubrs, ok = cffPrivateSubrs(p, top); !ok {
		return nil
	}
	charsetOff := 0
	if _, present := top[15]; present {
		if charsetOff, ok = intOp(15); !ok {
			return nil
		}
	}
	sids := cffCharsetSIDs(p, charsetOff, len(c.charstrings))
	if sids == nil {
		return nil
	}
	if cidKeyed {
		// The charset read as a CID map (its "SIDs" ARE the CIDs); offset 0
		// -- the predefined-charset slot, which does not apply to CIDFonts --
		// reads as the identity map. Lowest GID wins a duplicated CID, like
		// cffCode2GID's sid2gid.
		c.cid2gid = make(map[uint16]uint16, len(sids))
		for gid := len(sids) - 1; gid >= 1; gid-- {
			c.cid2gid[sids[gid]] = uint16(gid)
		}
		return c // CIDFonts carry no encoding table; codes arrive as CIDs
	}
	encodingOff := 0
	if _, present := top[16]; present {
		if encodingOff, ok = intOp(16); !ok {
			return nil
		}
	}
	codes := cffCode2GID(p, encodingOff, sids)
	if codes == nil {
		return nil
	}
	c.code2gid = *codes
	return c
}

// cffPrivateSubrs reads dict's Private operand pair (size then offset) and
// returns that Private DICT's local subrs: nil with ok for no Private at all
// or one without Subrs, refusal for malformed operands wherever they hide.
func cffPrivateSubrs(p []byte, dict map[int][]float64) ([][]byte, bool) {
	v, present := dict[18]
	if !present {
		return nil, true
	}
	if len(v) != 2 {
		return nil, false
	}
	size, off := v[0], v[1]
	if size != math.Trunc(size) || off != math.Trunc(off) ||
		size < 0 || off < 0 || off+size > float64(len(p)) {
		return nil, false
	}
	priv, ok := cffDict(p[int(off):int(off+size)])
	if !ok {
		return nil, false
	}
	s := priv[19]
	if s == nil { // no Subrs
		return nil, true
	}
	// Subrs is relative to the Private DICT. Bound BEFORE converting: the
	// float sum cannot overflow (both terms are <= len(p) once checked),
	// where int(s[0]) of a 2^63-ish operand would, and hand cffIndex a
	// wrapped position.
	if len(s) != 1 || s[0] != math.Trunc(s[0]) || s[0] < 0 ||
		off+s[0] > float64(len(p)) {
		return nil, false
	}
	subrs, _, ok := cffIndex(p, int(off)+int(s[0]))
	if !ok {
		return nil, false
	}
	return subrs, true
}

// cffFDSelect resolves every glyph's font DICT index exactly as sfnt's
// fdSelect.lookup will at that glyph's first callsubr (postscript.go): format
// 0 reads a byte per glyph, format 3 binary-searches [xlo,xhi) ranges in
// table order WITHOUT assuming they are sorted. Mirroring the search rather
// than validating ranges matters: the gate must agree with sfnt on WHICH
// local subrs each glyph runs, or a crafted table could point the walk at
// benign subrs while sfnt executes a bomb. -1 marks a glyph whose lookup
// sfnt will refuse (a miss, or an FD past the FDArray): every callsubr in it
// errors right there, so the walk runs it with no local subrs at all --
// callgsubr still resolves, exactly as in sfnt. nil refuses the font: an
// out-of-bounds, truncated, or unknown-format table.
func cffFDSelect(p []byte, offset, numGlyphs, numFDs int) []int16 {
	if offset <= 0 || offset >= len(p) {
		return nil
	}
	d := p[offset:]
	sel := make([]int16, numGlyphs)
	fd := func(b byte) int16 {
		if int(b) < numFDs {
			return int16(b)
		}
		return -1
	}
	switch d[0] {
	case 0:
		if len(d) < 1+numGlyphs {
			return nil
		}
		for gid := range sel {
			sel[gid] = fd(d[1+gid])
		}
	case 3:
		if len(d) < 3 {
			return nil
		}
		nRanges := int(be.Uint16(d[1:]))
		if len(d) < 3+3*nRanges+2 {
			return nil
		}
		for gid := range sel {
			sel[gid] = -1
			lo, hi := 0, nRanges
			for lo < hi {
				i := (lo + hi) / 2
				if xlo := int(be.Uint16(d[3+3*i:])); gid < xlo {
					hi = i
					continue
				}
				if xhi := int(be.Uint16(d[3+3*i+3:])); xhi <= gid {
					lo = i + 1
					continue
				}
				sel[gid] = fd(d[3+3*i+2])
				break
			}
		}
	default:
		return nil
	}
	return sel
}

// cffSubrBias is the Type 2 subr-number bias (5177.Type2.pdf section 4.7).
func cffSubrBias(count int) int32 {
	switch {
	case count < 1240:
		return 107
	case count < 33900:
		return 1131
	default:
		return 32768
	}
}

// cffWalkGlyph re-runs the control flow of one glyph's charstring exactly as
// sfnt's psInterpreter will, charging one unit per executed byte against
// *work (accumulated so fillGlyph can re-charge the total per SHOW).
// false means the walk tripped maxCharstringWork: the call tree is too big
// to hand sfnt. true means every byte sfnt will execute was charged and fits
// -- INCLUDING the malformed cases (reserved operator, bad subr index, stack
// over/underflow, truncated stream), where sfnt stops with an error at the
// same byte this walk stops at, so the work already counted is the total.
func (c *cffProgram) cffWalkGlyph(cs []byte, lsubrs [][]byte, work *int64) bool {
	// sfnt refuses any stream over its 64 KB maxGlyphDataLength before
	// running a byte of it; mirror that as zero-cost.
	const maxStream = 64 * 1024
	if len(cs) > maxStream {
		return true
	}
	var (
		args      []int32
		call      [][]byte
		hintBits  int32
		seenWidth bool
		ins       = cs
	)
	charge := func(n int) bool { *work += int64(n); return *work <= maxCharstringWork }
	// stem mirrors t2CStem (and hintmask's implicit vstem): consume the
	// optional leading width, then count -- never execute -- the hint pairs
	// so mask data bytes are skipped correctly. false = sfnt errors here.
	stem := func() bool {
		if !seenWidth {
			seenWidth = true
			if len(args)%2 == 1 {
				args = args[1:]
			}
		}
		if len(args)%2 != 0 {
			return false
		}
		hintBits += int32(len(args) / 2)
		args = args[:0]
		return hintBits <= 256 // sfnt's maxHintBits
	}
	for len(ins) > 0 {
		b := ins[0]
		// Operand encodings (5177.Type2.pdf 3.2), each charged as consumed.
		if b == 28 || b >= 32 {
			n := 1
			switch {
			case b == 28:
				n = 3
			case b >= 247 && b <= 254:
				n = 2
			case b == 255:
				n = 5
			}
			if len(ins) < n || len(args) == 48 { // truncation / stack overflow
				return true
			}
			var v int32
			switch {
			case b == 28:
				v = int32(int16(be.Uint16(ins[1:])))
			case b <= 246:
				v = int32(b) - 139
			case b <= 250:
				v = int32(b-247)*256 + int32(ins[1]) + 108
			case b <= 254:
				v = -int32(b-251)*256 - int32(ins[1]) - 108
			default:
				// 16.16 fixed. sfnt ROUNDS it to an integer before pushing
				// (postscript.go's parseNumber); mirroring the raw value
				// instead would let a bomb hide subr indices behind
				// 255-encoded operands this walk mispredicts.
				v = int32(be.Uint32(ins[1:]))
				v = (v >> 16) + (1 & (v >> 15))
			}
			args = append(args, v)
			ins = ins[n:]
			if !charge(n) {
				return false
			}
			continue
		}
		ins = ins[1:]
		if !charge(1) {
			return false
		}
		switch b {
		case 1, 3, 18, 23: // hstem vstem hstemhm vstemhm
			if !stem() {
				return true
			}
		case 19, 20: // hintmask cntrmask
			if len(args) != 0 {
				if !stem() {
					return true
				}
			} else if !seenWidth {
				seenWidth = true
			}
			n := int((hintBits + 7) / 8)
			if len(ins) < n {
				return true
			}
			ins = ins[n:]
			if !charge(n) {
				return false
			}
			args = args[:0]
		case 4, 21, 22: // vmoveto rmoveto hmoveto
			seenWidth = true
			args = args[:0]
		case 5, 6, 7, 8, 24, 25, 26, 27, 30, 31: // line/curve families
			args = args[:0]
		case 10, 29: // callsubr callgsubr
			subrs := lsubrs
			if b == 29 {
				subrs = c.gsubrs
			}
			if len(args) == 0 || len(subrs) == 0 || len(call) == 10 { // psCallStackSize
				return true
			}
			idx := args[len(args)-1] + cffSubrBias(len(subrs))
			args = args[:len(args)-1]
			if idx < 0 || int(idx) >= len(subrs) || len(subrs[idx]) > maxStream {
				return true
			}
			call = append(call, ins)
			ins = subrs[idx]
		case 11: // return
			if len(call) == 0 {
				return true
			}
			ins = call[len(call)-1]
			call = call[:len(call)-1]
		case 14: // endchar: ends clean, or errors on leftovers -- done either way
			return true
		case 12: // escape: sfnt implements only hflex (34) and hflex1 (36)
			if len(ins) == 0 {
				return true
			}
			e := ins[0]
			ins = ins[1:]
			if !charge(1) {
				return false
			}
			var pop int
			switch e {
			case 34:
				pop = 7
			case 36:
				pop = 9
			default:
				return true
			}
			if len(args) < pop {
				return true
			}
			args = args[:len(args)-pop]
		default: // reserved: 0 2 9 13 15 16 17
			return true
		}
	}
	return true // stream ran out: sfnt's run loop ends here too
}

// cffGateWork walks every reachable glyph -- the synthetic cmap's 256 codes,
// or for a CID-keyed font all of them -- and returns the work charged per
// GID; nil means a glyph tripped maxCharstringWork or a CID font tripped the
// whole-font cap. GID 0 is never reached: fillGID skips it, and the
// version-2 OS/2 table keeps sfnt.Parse from loading any glyph at all. A
// nonempty charstring always charges at least one unit, so work[gid] != 0
// doubles as the walked marker (an empty charstring re-walks for free).
func (c *cffProgram) cffGateWork() []int64 {
	work := make([]int64, len(c.charstrings))
	if c.cid2gid != nil {
		// CID-keyed: EVERY glyph is reachable (any two-byte code is a CID),
		// so the whole CharStrings INDEX is walked -- each glyph with the
		// local subrs of ITS font DICT, resolved exactly as sfnt will -- and
		// the per-glyph budget alone would let glyph count multiply the
		// walk's own cost, so the font as a whole is capped at the 256-code
		// gate's ceiling. A legitimate CJK font fits with room (tens of
		// thousands of glyphs at typical ~10^2-byte charstrings); one past
		// it degrades to widths-only like any other refusal.
		var total int64
		for gid := 1; gid < len(c.charstrings); gid++ {
			var lsubrs [][]byte
			if fd := c.fdSel[gid]; fd >= 0 {
				lsubrs = c.fdSubrs[fd]
			}
			if !c.cffWalkGlyph(c.charstrings[gid], lsubrs, &work[gid]) {
				return nil
			}
			if total += work[gid]; total > 256*maxCharstringWork {
				return nil
			}
		}
		return work
	}
	for _, gid := range c.code2gid {
		if gid == 0 || int(gid) >= len(c.charstrings) || work[gid] != 0 {
			continue
		}
		if !c.cffWalkGlyph(c.charstrings[int(gid)], c.lsubrs, &work[gid]) {
			return nil
		}
	}
	return work
}

// cffWrapSFNT wraps the raw CFF blob as the 'CFF ' table of a minimal OTTO
// container carrying exactly the tables sfnt.Parse requires. head carries the
// upem; hhea/hmtx declare one zero metric (PDF /Widths drive advances); the
// version-2 OS/2 stops Parse's metrics fallback from loading glyphs; the
// format-6 cmap IS the builtin encoding, one entry per byte code.
func cffWrapSFNT(cff []byte, numGlyphs int, upem uint16, code2gid *[256]uint16) []byte {
	head := make([]byte, 54)
	be.PutUint16(head[18:], upem)

	maxp := be.AppendUint16(be.AppendUint32(nil, 0x00005000), uint16(numGlyphs))

	hhea := make([]byte, 36)
	hhea[35] = 1 // numberOfHMetrics = 1

	hmtx := make([]byte, 4)

	var sub []byte
	sub = be.AppendUint16(sub, 6) // format 6
	sub = be.AppendUint16(sub, uint16(10+2*len(code2gid)))
	sub = be.AppendUint16(sub, 0) // language
	sub = be.AppendUint16(sub, 0) // firstCode
	sub = be.AppendUint16(sub, uint16(len(code2gid)))
	for _, g := range code2gid {
		sub = be.AppendUint16(sub, g)
	}
	var cmap []byte
	cmap = be.AppendUint16(cmap, 0) // version
	cmap = be.AppendUint16(cmap, 1) // one subtable: Windows Unicode BMP
	cmap = be.AppendUint16(cmap, 3)
	cmap = be.AppendUint16(cmap, 1)
	cmap = be.AppendUint32(cmap, 12)
	cmap = append(cmap, sub...)

	os2 := make([]byte, 96)
	os2[1] = 2 // version 2: xHeight/capHeight read from the table, no fallback

	post := make([]byte, 32)
	post[1] = 3 // version 3.0: no glyph names

	tables := []struct {
		tag  uint32
		data []byte
	}{ // ascending tag order, as sfnt checks
		{0x43464620, cff}, // "CFF "
		{0x4f532f32, os2}, // "OS/2"
		{0x636d6170, cmap},
		{0x68656164, head},
		{0x68686561, hhea},
		{0x686d7478, hmtx},
		{0x6d617870, maxp},
		{0x706f7374, post},
	}
	var b []byte
	b = be.AppendUint32(b, 0x4f54544f) // OTTO
	b = be.AppendUint16(b, uint16(len(tables)))
	b = append(b, 0, 0, 0, 0, 0, 0) // searchRange trio: unchecked
	off := 12 + 16*len(tables)
	for _, t := range tables {
		b = be.AppendUint32(b, t.tag)
		b = be.AppendUint32(b, 0) // checksum: unchecked
		b = be.AppendUint32(b, uint32(off))
		b = be.AppendUint32(b, uint32(len(t.data)))
		off += (len(t.data) + 3) &^ 3
	}
	for _, t := range tables {
		b = append(b, t.data...)
		for len(b)%4 != 0 { // tables must begin 4-aligned
			b = append(b, 0)
		}
	}
	return b
}

// cffToSFNT is the CFF entry point: a bare CFF in, an sfnt.Parse-able wrapper
// plus the gate-measured per-GID charstring work out (fillGID charges it per
// show), or nils when the program is not a CFF this path can safely hand to
// sfnt (see parseCFF and cffGateWork). cid2gid is non-nil exactly when the
// program is CID-keyed (byb-8b9.8): the charset-as-CID-map fillCIDGlyph
// resolves Type0 codes through.
func cffToSFNT(p []byte) ([]byte, []int64, map[uint16]uint16) {
	c := parseCFF(p)
	if c == nil {
		return nil, nil, nil
	}
	gwork := c.cffGateWork()
	if gwork == nil {
		return nil, nil, nil
	}
	return cffWrapSFNT(p, len(c.charstrings), c.upem, &c.code2gid), gwork, c.cid2gid
}
