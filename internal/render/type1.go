package render

// Stage 4e (byb-8b9.5): classic Type 1 glyph outlines -- /FontFile programs,
// PFB-wrapped or raw PFA/binary, with eexec encryption.
//
// LADDER NOTE, recorded per the bead: nothing already in go.mod parses Type 1.
// x/image/font/sfnt speaks TrueType and CFF-in-sfnt containers only, and
// pdfcpu never touches glyph outlines. So this file is a minimal parser plus a
// Type 1 charstring interpreter for the OUTLINE subset: hsbw/sbw, the
// move/line/curve families, closepath, callsubr/return, endchar, div, and the
// flex OtherSubrs protocol (whose seven collected points ARE the two cubics,
// so flex collapses to its curve points). Hints (hstem, vstem, vstem3,
// hstem3, dotsection) affect grid-fitting, not outlines, and are skipped.
// seac (accented composites) is DEFERRED: the composite glyph skips cleanly
// and TestType1SeacDeferred pins that; wiring the base+accent recursion waits
// for a document that needs it.
//
// BUDGETS (the package's refuse-before-expanding discipline): decryption and
// every parse scan below are linear in the input -- no length field is
// trusted past the remaining bytes. The only expansion is the interpreter
// itself, so it charges one unit per executed charstring byte as it runs:
// maxCharstringWork bounds one glyph's call tree (a subr chain bomb abandons
// the glyph in bounded time), a depth-10 call stack bounds recursion, and
// fillT1Glyph charges the executed total against maxFillWork per SHOW, so a
// stream re-showing one expensive glyph stays bounded per Page exactly like
// the 4d gate total. Outline points then charge the 4a path budget as usual.

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/dobbo-ca/byblos/internal/content"
)

// t1Font is a parsed classic Type 1: decrypted charstrings by glyph name, the
// decrypted Subrs, the builtin encoding, and the FontMatrix as a upem.
type t1Font struct {
	glyphs map[string][]byte
	subrs  [][]byte
	enc    [256]string
	upem   float64
}

// t1Decrypt runs the Type 1 decryption (Adobe Type 1 spec sections 7.2-7.3)
// and discards the first skip plaintext bytes. Output is bounded by input.
func t1Decrypt(data []byte, r uint16, skip int) []byte {
	if len(data) < skip {
		return nil
	}
	out := make([]byte, 0, len(data)-skip)
	for i, c := range data {
		p := c ^ byte(r>>8)
		r = (uint16(c)+r)*52845 + 22719
		if i >= skip {
			out = append(out, p)
		}
	}
	return out
}

func t1IsHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}

// t1HexDecode decodes ASCII-hex until the first byte that is neither a hex
// digit nor whitespace -- the PFA convention, where the 512-zero trailer
// simply decodes into tail garbage the section parsers never reach.
func t1HexDecode(d []byte) []byte {
	out := make([]byte, 0, len(d)/2)
	hi := -1
	for _, b := range d {
		var v int
		switch {
		case b >= '0' && b <= '9':
			v = int(b - '0')
		case b >= 'a' && b <= 'f':
			v = int(b-'a') + 10
		case b >= 'A' && b <= 'F':
			v = int(b-'A') + 10
		case b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f':
			continue
		default:
			return out
		}
		if hi < 0 {
			hi = v
		} else {
			out = append(out, byte(hi<<4|v))
			hi = -1
		}
	}
	return out
}

// t1Sections splits a Type 1 program into its cleartext header and its
// eexec-encrypted section. PFB segments (0x80-framed, little-endian lengths)
// carry the split explicitly; a raw program is cut at the "eexec" keyword,
// with the encrypted bytes binary or ASCII-hex. Every length is checked
// against the remaining input before use.
func t1Sections(p []byte) (clear, enc []byte, ok bool) {
	if len(p) >= 2 && p[0] == 0x80 {
		for pos := 0; pos+2 <= len(p); {
			if p[pos] != 0x80 {
				return nil, nil, false
			}
			t := p[pos+1]
			if t == 3 {
				break
			}
			if pos+6 > len(p) {
				return nil, nil, false
			}
			n := int(p[pos+2]) | int(p[pos+3])<<8 | int(p[pos+4])<<16 | int(p[pos+5])<<24
			if n < 0 || n > len(p)-pos-6 {
				return nil, nil, false
			}
			seg := p[pos+6 : pos+6+n]
			switch t {
			case 1:
				if enc == nil { // ascii after binary is the trailer: ignored
					clear = append(clear, seg...)
				}
			case 2:
				enc = append(enc, seg...)
			default:
				return nil, nil, false
			}
			pos += 6 + n
		}
		return clear, enc, len(enc) >= 4
	}
	if !bytes.HasPrefix(p, []byte("%!")) {
		return nil, nil, false
	}
	i := bytes.Index(p, []byte("eexec"))
	if i < 0 {
		return nil, nil, false
	}
	clear = p[:i]
	rest := p[i+5:]
	for len(rest) > 0 && (rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\r' || rest[0] == '\n' || rest[0] == '\f') {
		rest = rest[1:]
	}
	if len(rest) < 4 {
		return nil, nil, false
	}
	if t1IsHex(rest[0]) && t1IsHex(rest[1]) && t1IsHex(rest[2]) && t1IsHex(rest[3]) {
		rest = t1HexDecode(rest)
	}
	return clear, rest, len(rest) >= 4
}

// t1Scan is a whitespace-delimited token scanner over the decrypted private
// section. Every method advances or returns nil, so loops over it terminate.
type t1Scan struct {
	d   []byte
	pos int
}

func t1Space(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f' || b == 0
}

func (s *t1Scan) token() []byte {
	for s.pos < len(s.d) && t1Space(s.d[s.pos]) {
		s.pos++
	}
	start := s.pos
	for s.pos < len(s.d) && !t1Space(s.d[s.pos]) {
		s.pos++
	}
	if s.pos == start {
		return nil
	}
	return s.d[start:s.pos]
}

func (s *t1Scan) int() (int, bool) {
	v, err := strconv.Atoi(string(s.token()))
	return v, err == nil
}

// binary reads the n RD-delimited bytes that follow the current token: one
// separator byte, then exactly n bytes.
func (s *t1Scan) binary(n int) []byte {
	if n < 0 || n > len(s.d)-s.pos-1 {
		return nil
	}
	b := s.d[s.pos+1 : s.pos+1+n]
	s.pos += 1 + n
	return b
}

// t1LenIV reads /lenIV from the decrypted private section; 4 when absent
// (the spec default), -1 when present but unusable.
func t1LenIV(d []byte) int {
	i := bytes.Index(d, []byte("/lenIV"))
	if i < 0 {
		return 4
	}
	s := &t1Scan{d: d, pos: i + len("/lenIV")}
	if v, ok := s.int(); ok && v >= 0 && v <= 16 {
		return v
	}
	return -1
}

// t1ParseSubrs reads the /Subrs array: "dup <index> <len> RD <bytes> NP"
// entries, each charstring-decrypted. A malformed entry stops the scan; what
// parsed so far is kept, and a missing subr abandons only the glyphs that
// call it.
func t1ParseSubrs(d []byte, lenIV int) [][]byte {
	i := bytes.Index(d, []byte("/Subrs"))
	if i < 0 {
		return nil
	}
	s := &t1Scan{d: d, pos: i + len("/Subrs")}
	count, ok := s.int()
	if !ok || count <= 0 || count > len(d) {
		return nil
	}
	subrs := make([][]byte, count)
	for seen := 0; seen < count; {
		tok := s.token()
		if tok == nil || string(tok) == "ND" || string(tok) == "|-" || string(tok) == "end" {
			break
		}
		if string(tok) != "dup" {
			continue
		}
		idx, ok1 := s.int()
		n, ok2 := s.int()
		rd := s.token()
		if !ok1 || !ok2 || rd == nil {
			break
		}
		bin := s.binary(n)
		if bin == nil {
			break
		}
		if idx >= 0 && idx < count {
			subrs[idx] = t1Decrypt(bin, 4330, lenIV)
		}
		seen++
	}
	return subrs
}

// t1ParseCharstrings reads the /CharStrings dict: "/<name> <len> RD <bytes>
// ND" entries until "end", each charstring-decrypted.
func t1ParseCharstrings(d []byte, lenIV int) map[string][]byte {
	i := bytes.Index(d, []byte("/CharStrings"))
	if i < 0 {
		return nil
	}
	s := &t1Scan{d: d, pos: i + len("/CharStrings")}
	glyphs := map[string][]byte{}
	for {
		tok := s.token()
		if tok == nil || string(tok) == "end" {
			break
		}
		if tok[0] != '/' || len(tok) < 2 {
			continue
		}
		name := string(tok[1:])
		n, ok := s.int()
		rd := s.token()
		if !ok || rd == nil {
			break
		}
		bin := s.binary(n)
		if bin == nil {
			break
		}
		glyphs[name] = t1Decrypt(bin, 4330, lenIV)
	}
	return glyphs
}

// t1StdNames is the Standard Encoding glyph name per SID -- the same SID
// space cffStandardSID maps codes into (Appendix A of the CFF spec lists
// these as standard strings 0..149, which over this range coincide with the
// Type 1 StandardEncoding names).
var t1StdNames = strings.Fields(`.notdef
	space exclam quotedbl numbersign dollar percent ampersand quoteright
	parenleft parenright asterisk plus comma hyphen period slash
	zero one two three four five six seven eight nine
	colon semicolon less equal greater question at
	A B C D E F G H I J K L M N O P Q R S T U V W X Y Z
	bracketleft backslash bracketright asciicircum underscore quoteleft
	a b c d e f g h i j k l m n o p q r s t u v w x y z
	braceleft bar braceright asciitilde
	exclamdown cent sterling fraction yen florin section currency quotesingle
	quotedblleft guillemotleft guilsinglleft guilsinglright fi fl
	endash dagger daggerdbl periodcentered paragraph bullet
	quotesinglbase quotedblbase quotedblright guillemotright ellipsis
	perthousand questiondown grave acute circumflex tilde macron breve
	dotaccent dieresis ring cedilla hungarumlaut ogonek caron emdash
	AE ordfeminine Lslash Oslash OE ordmasculine ae dotlessi lslash oslash
	oe germandbls`)

// t1StdEncoding is the code -> glyph-name table of the Standard Encoding.
func t1StdEncoding() [256]string {
	var enc [256]string
	for c := range enc {
		if sid := cffStandardSID(byte(c)); sid != 0 {
			enc[c] = t1StdNames[sid]
		}
	}
	return enc
}

// t1Encoding reads the builtin encoding from the cleartext header: the
// predefined StandardEncoding, or "dup <code> /<name> put" entries. This is
// the font's OWN encoding -- 4e's analogue of 4d's builtin-encoding stance;
// a PDF /Encoding-aware mapping still waits for a caller that can supply one
// across the Font seam.
func t1Encoding(clear []byte) [256]string {
	i := bytes.Index(clear, []byte("/Encoding"))
	if i < 0 {
		return t1StdEncoding()
	}
	seg := clear[i+len("/Encoding"):]
	if j := bytes.Index(seg, []byte("StandardEncoding")); j >= 0 && j < 8 {
		return t1StdEncoding()
	}
	var enc [256]string
	s := &t1Scan{d: seg}
	for {
		tok := s.token()
		if tok == nil || string(tok) == "readonly" || string(tok) == "def" {
			break
		}
		if string(tok) != "dup" {
			continue
		}
		code, ok := s.int()
		name := s.token()
		if !ok || len(name) < 2 || name[0] != '/' {
			break
		}
		if string(s.token()) != "put" {
			break
		}
		if code >= 0 && code < 256 {
			enc[code] = string(name[1:])
		}
	}
	return enc
}

// t1UPEM reads /FontMatrix from the cleartext. Like parseCFF, only the
// uniform axis-aligned [s 0 0 s 0 0] form maps onto a single upem; anything
// else returns 0 and the font degrades to widths-only rather than rendering
// at the wrong shape. Absent, the Type 1 default 0.001 gives upem 1000.
func t1UPEM(clear []byte) float64 {
	i := bytes.Index(clear, []byte("/FontMatrix"))
	if i < 0 {
		return 1000
	}
	seg := clear[i+len("/FontMatrix"):]
	j := bytes.IndexByte(seg, '[')
	k := bytes.IndexByte(seg, ']')
	if j < 0 || k <= j || k > j+128 {
		return 0
	}
	fields := strings.Fields(string(seg[j+1 : k]))
	if len(fields) != 6 {
		return 0
	}
	var m [6]float64
	for n, f := range fields {
		v, err := strconv.ParseFloat(f, 64)
		if err != nil {
			return 0
		}
		m[n] = v
	}
	if !(m[0] > 0) || m[1] != 0 || m[2] != 0 || m[3] != m[0] || m[4] != 0 || m[5] != 0 {
		return 0
	}
	u := math.Round(1 / m[0])
	if !(u >= 1 && u <= 65535) {
		return 0
	}
	return u
}

// parseType1 parses a classic Type 1 program. nil means the program is not a
// Type 1 this stage can safely interpret and the caller degrades to
// widths-only, exactly like an unparsable TrueType or CFF.
func parseType1(p []byte) *t1Font {
	clear, enc, ok := t1Sections(p)
	if !ok || !bytes.HasPrefix(clear, []byte("%!")) {
		return nil
	}
	upem := t1UPEM(clear)
	if upem == 0 {
		return nil
	}
	priv := t1Decrypt(enc, 55665, 4)
	lenIV := t1LenIV(priv)
	if lenIV < 0 {
		return nil
	}
	glyphs := t1ParseCharstrings(priv, lenIV)
	if len(glyphs) == 0 {
		return nil
	}
	return &t1Font{
		glyphs: glyphs,
		subrs:  t1ParseSubrs(priv, lenIV),
		enc:    t1Encoding(clear),
		upem:   upem,
	}
}

// errT1Skip abandons ONE glyph -- malformed charstring, deferred seac, a
// tripped per-glyph budget, or a non-finite device coordinate -- without
// failing the page. Path and fill budget errors are different errors and
// propagate.
var errT1Skip = errors.New("render: type 1 glyph skipped")

// t1Sink receives outline commands in font units, y UP (Type 1 charstring
// space; the flip TrueType needs does not apply).
type t1Sink struct {
	move  func(x, y float64) error
	line  func(x, y float64) error
	curve func(x1, y1, x2, y2, x3, y3 float64) error
}

// interpret executes one decrypted charstring, charging one unit per executed
// byte against *work (the caller charges the total against maxFillWork per
// show). errT1Skip means the glyph is abandoned; any other error comes from
// the sink and fails the page.
func (f *t1Font) interpret(cs []byte, sink t1Sink, work *int64) error {
	var (
		st      []float64
		call    [][]byte
		psq     []float64 // PostScript-stack stand-in feeding `pop`
		x, y    float64
		flexing bool
		flexPts []float64
		ins     = cs
	)
	charge := func(n int) bool { *work += int64(n); return *work <= maxCharstringWork }
	moveTo := func(dx, dy float64) error {
		x += dx
		y += dy
		if flexing {
			// Flex points are COLLECTED, not moved to: the seven rmoveto'd
			// points become the two cubics at othersubr 0.
			if len(flexPts) >= 14 {
				return errT1Skip
			}
			flexPts = append(flexPts, x, y)
			return nil
		}
		return sink.move(x, y)
	}
	curveTo := func(c1x, c1y, c2x, c2y, ex, ey float64) error {
		x, y = ex, ey
		return sink.curve(c1x, c1y, c2x, c2y, ex, ey)
	}
	for len(ins) > 0 {
		b := ins[0]
		if b >= 32 || b == 28 {
			// Operand encodings (Type 1 spec 6.2). 28 is not a Type 1
			// encoding; treating it as malformed keeps the byte from
			// aliasing an operator.
			if b == 28 {
				return errT1Skip
			}
			n := 1
			switch {
			case b >= 247 && b <= 254:
				n = 2
			case b == 255:
				n = 5
			}
			if len(ins) < n || len(st) >= 48 {
				return errT1Skip
			}
			var v float64
			switch {
			case b <= 246:
				v = float64(int(b) - 139)
			case b <= 250:
				v = float64(int(b-247)*256 + int(ins[1]) + 108)
			case b <= 254:
				v = float64(-int(b-251)*256 - int(ins[1]) - 108)
			default: // full 32-bit integer, unlike Type 2's 16.16
				v = float64(int32(be.Uint32(ins[1:])))
			}
			st = append(st, v)
			ins = ins[n:]
			if !charge(n) {
				return errT1Skip
			}
			continue
		}
		ins = ins[1:]
		if !charge(1) {
			return errT1Skip
		}
		switch b {
		case 1, 3: // hstem vstem: hints shape grid-fitting, never outlines
			st = st[:0]
		case 4, 21, 22: // vmoveto rmoveto hmoveto
			var dx, dy float64
			switch {
			case b == 21 && len(st) >= 2:
				dx, dy = st[0], st[1]
			case b == 22 && len(st) >= 1:
				dx = st[0]
			case b == 4 && len(st) >= 1:
				dy = st[0]
			default:
				return errT1Skip
			}
			if err := moveTo(dx, dy); err != nil {
				return err
			}
			st = st[:0]
		case 5, 6, 7: // rlineto hlineto vlineto
			switch {
			case b == 5 && len(st) >= 2:
				x += st[0]
				y += st[1]
			case b == 6 && len(st) >= 1:
				x += st[0]
			case b == 7 && len(st) >= 1:
				y += st[0]
			default:
				return errT1Skip
			}
			if err := sink.line(x, y); err != nil {
				return err
			}
			st = st[:0]
		case 8: // rrcurveto
			if len(st) < 6 {
				return errT1Skip
			}
			c1x, c1y := x+st[0], y+st[1]
			c2x, c2y := c1x+st[2], c1y+st[3]
			if err := curveTo(c1x, c1y, c2x, c2y, c2x+st[4], c2y+st[5]); err != nil {
				return err
			}
			st = st[:0]
		case 30, 31: // vhcurveto hvcurveto
			if len(st) < 4 {
				return errT1Skip
			}
			var c1x, c1y, c2x, c2y, ex, ey float64
			if b == 30 { // dy1 dx2 dy2 dx3
				c1x, c1y = x, y+st[0]
				c2x, c2y = c1x+st[1], c1y+st[2]
				ex, ey = c2x+st[3], c2y
			} else { // dx1 dx2 dy2 dy3
				c1x, c1y = x+st[0], y
				c2x, c2y = c1x+st[1], c1y+st[2]
				ex, ey = c2x, c2y+st[3]
			}
			if err := curveTo(c1x, c1y, c2x, c2y, ex, ey); err != nil {
				return err
			}
			st = st[:0]
		case 9: // closepath: the 4a filler closes subpaths implicitly, and
			// the current point is unchanged by spec.
			st = st[:0]
		case 10: // callsubr (no bias in Type 1)
			if len(st) == 0 || len(call) >= 10 {
				return errT1Skip
			}
			idx := int(st[len(st)-1])
			st = st[:len(st)-1]
			if idx < 0 || idx >= len(f.subrs) || f.subrs[idx] == nil {
				return errT1Skip
			}
			call = append(call, ins)
			ins = f.subrs[idx]
		case 11: // return
			if len(call) == 0 {
				return errT1Skip
			}
			ins = call[len(call)-1]
			call = call[:len(call)-1]
		case 13: // hsbw: the sidebearing IS the initial current point; the
			// width is ignored, PDF /Widths drive every advance.
			if len(st) < 2 {
				return errT1Skip
			}
			x, y = st[0], 0
			st = st[:0]
		case 14: // endchar
			return nil
		case 12: // escape
			if len(ins) == 0 {
				return errT1Skip
			}
			e := ins[0]
			ins = ins[1:]
			if !charge(1) {
				return errT1Skip
			}
			switch e {
			case 0, 1, 2: // dotsection vstem3 hstem3: hints, skipped
				st = st[:0]
			case 6: // seac: DEFERRED (see the package comment)
				return errT1Skip
			case 7: // sbw
				if len(st) < 4 {
					return errT1Skip
				}
				x, y = st[0], st[1]
				st = st[:0]
			case 12: // div
				if len(st) < 2 || st[len(st)-1] == 0 {
					return errT1Skip
				}
				st[len(st)-2] /= st[len(st)-1]
				st = st[:len(st)-1]
			case 16: // callothersubr: flex and hint replacement
				if len(st) < 2 {
					return errT1Skip
				}
				oth := int(st[len(st)-1])
				n := int(st[len(st)-2])
				st = st[:len(st)-2]
				if n < 0 || n > len(st) {
					return errT1Skip
				}
				args := st[len(st)-n:]
				switch oth {
				case 0: // flex end: the collected points are the two cubics
					if !flexing || len(flexPts) != 14 {
						return errT1Skip
					}
					p := flexPts
					if err := curveTo(p[2], p[3], p[4], p[5], p[6], p[7]); err != nil {
						return err
					}
					if err := curveTo(p[8], p[9], p[10], p[11], p[12], p[13]); err != nil {
						return err
					}
					flexing, flexPts = false, nil
					// The following pop/pop/setcurrentpoint re-states the end
					// point the curves already reached.
					psq = append(psq[:0], x, y)
				case 1: // flex start
					flexing, flexPts = true, nil
				case 2: // one flex point, already collected by its rmoveto
				default: // hint replacement (3) and unknowns: args feed pop
					psq = append(psq[:0], args...)
				}
				st = st[:len(st)-n]
			case 17: // pop
				if len(st) >= 48 {
					return errT1Skip
				}
				v := 0.0
				if len(psq) > 0 {
					v = psq[len(psq)-1]
					psq = psq[:len(psq)-1]
				}
				st = append(st, v)
			case 33: // setcurrentpoint: only ever follows an othersubr that
				// already left the current point correct; clearing is enough.
				st = st[:0]
			default:
				return errT1Skip
			}
		default: // reserved
			return errT1Skip
		}
	}
	// Stream ran out mid-glyph (no endchar): keep what was outlined, exactly
	// as sfnt's run loop ends a Type 2 stream.
	return nil
}

// fillT1Glyph rasterises one Type 1 glyph through the same path machinery as
// fillGlyph: interpreter work and flattened points charge the budgets, and
// the fill is the 4a scanline filler under the nonzero rule. Any failure
// short of a budget skips the glyph cleanly.
func (r *renderer) fillT1Glyph(gs *gstate, f *textFont, code byte) error {
	t1 := f.t1
	cs := t1.glyphs[t1.enc[code]]
	if cs == nil {
		return nil
	}
	// Type 1 charstring space is y-up already, so unlike glyphMatrix there is
	// no flip: scale by 1/upem into text space, then Trm.
	params := content.Matrix{gs.fontSize * gs.hscale, 0, 0, gs.fontSize, 0, gs.rise}
	m := content.Matrix{1 / t1.upem, 0, 0, 1 / t1.upem, 0, 0}.
		Mul(params).Mul(gs.tm).Mul(gs.ctm)
	var pth path
	defer pth.reset(r)
	dev := func(fx, fy float64) (point, error) {
		x, y := m.Apply(fx, fy)
		// Same single finite check as fillGlyph, at the only place hostile
		// text parameters meet the scanline filler.
		if math.IsNaN(x) || math.IsNaN(y) || math.IsInf(x, 0) || math.IsInf(y, 0) {
			return point{}, errT1Skip
		}
		return point{x, y}, nil
	}
	sink := t1Sink{
		move: func(fx, fy float64) error {
			p, err := dev(fx, fy)
			if err != nil {
				return err
			}
			return pth.moveTo(r, p)
		},
		line: func(fx, fy float64) error {
			p, err := dev(fx, fy)
			if err != nil {
				return err
			}
			return pth.lineTo(r, p)
		},
		curve: func(x1, y1, x2, y2, x3, y3 float64) error {
			c1, err := dev(x1, y1)
			if err != nil {
				return err
			}
			c2, err := dev(x2, y2)
			if err != nil {
				return err
			}
			end, err := dev(x3, y3)
			if err != nil {
				return err
			}
			return pth.curveTo(r, c1, c2, end)
		},
	}
	var work int64
	err := f.t1.interpret(cs, sink, &work)
	// Charge the interpreter's executed bytes per SHOW, like the 4d gate
	// total: a stream re-showing one expensive glyph stays bounded per Page.
	r.fillWork += work
	if r.fillWork > maxFillWork {
		return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
	}
	if errors.Is(err, errT1Skip) {
		return nil
	}
	if err != nil {
		return err
	}
	n := int64(pathPoints(pth.subs))
	r.fillWork += n
	if r.fillWork > maxFillWork {
		return fmt.Errorf("render: fill work exceeds %d edge-scanline units", maxFillWork)
	}
	return r.fillSubpaths(pth.subs, false, r.deviceClip(gs.clip), gs.fill.rgba)
}
