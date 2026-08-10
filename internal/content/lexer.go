// Package content lexes and walks PDF content streams.
//
// pdfcpu decodes content streams but does not tokenize them, so Byblos needs
// its own operator-level parser. It is the only way to tell a clean
// page-covering scan from a page carrying an overlay (design spec section 2):
// an image count alone cannot, because an overlay commonly lives inside a Form
// XObject that the page's image count never sees.
//
// Syntax follows ISO 32000-1:2008 section 7.2 (lexical conventions) and
// section 8.2 (content streams).
package content

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
)

// Kind classifies a content-stream token.
type Kind uint8

const (
	KindNumber Kind = iota
	KindString
	KindName
	KindArrayOpen
	KindArrayClose
	KindDictOpen
	KindDictClose
	KindKeyword     // an operator, or true/false/null
	KindInlineImage // an entire BI ... ID ... EI sequence
)

// Token is one lexed item. Num is meaningful for KindNumber; Text holds the
// name text (without the leading slash), the decoded string bytes, or the
// keyword text. Text aliases the source buffer for keywords, and is freshly
// allocated for names and strings.
type Token struct {
	Kind Kind
	Num  float64
	Text []byte
}

// Lexer tokenizes a decoded content stream.
type Lexer struct {
	src []byte
	pos int
}

func NewLexer(src []byte) *Lexer { return &Lexer{src: src} }

func isWhite(c byte) bool {
	return c == 0 || c == '\t' || c == '\n' || c == '\f' || c == '\r' || c == ' '
}

func isDelim(c byte) bool {
	switch c {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	}
	return false
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

func (l *Lexer) skipSpace() {
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isWhite(c) {
			l.pos++
			continue
		}
		if c == '%' {
			for l.pos < len(l.src) && l.src[l.pos] != '\n' && l.src[l.pos] != '\r' {
				l.pos++
			}
			continue
		}
		return
	}
}

// Next returns the next token, or io.EOF when the stream is exhausted. Every
// successful call advances the read position, so a caller cannot loop forever.
func (l *Lexer) Next() (Token, error) {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return Token{}, io.EOF
	}
	c := l.src[l.pos]
	switch {
	case c == '[':
		l.pos++
		return Token{Kind: KindArrayOpen}, nil
	case c == ']':
		l.pos++
		return Token{Kind: KindArrayClose}, nil
	case c == '<':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '<' {
			l.pos += 2
			return Token{Kind: KindDictOpen}, nil
		}
		return l.hexString()
	case c == '>':
		if l.pos+1 < len(l.src) && l.src[l.pos+1] == '>' {
			l.pos += 2
			return Token{Kind: KindDictClose}, nil
		}
		return Token{}, fmt.Errorf("content: stray '>' at offset %d", l.pos)
	case c == '(':
		return l.literalString()
	case c == ')':
		return Token{}, fmt.Errorf("content: stray ')' at offset %d", l.pos)
	case c == '/':
		return l.name()
	case c == '+' || c == '-' || c == '.' || (c >= '0' && c <= '9'):
		return l.number()
	case c == '{' || c == '}':
		// Type 4 function braces never occur in a page content stream; report
		// them as keywords so the walker can treat the page as unclassifiable.
		l.pos++
		return Token{Kind: KindKeyword, Text: l.src[l.pos-1 : l.pos]}, nil
	default:
		return l.keyword()
	}
}

func (l *Lexer) keyword() (Token, error) {
	start := l.pos
	for l.pos < len(l.src) && !isWhite(l.src[l.pos]) && !isDelim(l.src[l.pos]) {
		l.pos++
	}
	if l.pos == start {
		l.pos++ // guarantee progress
		return Token{}, fmt.Errorf("content: unexpected byte %q at offset %d", l.src[start], start)
	}
	kw := l.src[start:l.pos]
	if string(kw) == "BI" {
		return l.inlineImage(start)
	}
	return Token{Kind: KindKeyword, Text: kw}, nil
}

func (l *Lexer) number() (Token, error) {
	start := l.pos
	if c := l.src[l.pos]; c == '+' || c == '-' {
		l.pos++
	}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' {
			l.pos++
			continue
		}
		break
	}
	text := string(l.src[start:l.pos])
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		v = parsePrefixFloat(text)
	}
	return Token{Kind: KindNumber, Num: v}, nil
}

// parsePrefixFloat returns the value of the longest parseable prefix of s, or
// 0. Real content streams carry malformed reals such as "1.-2"; viewers keep
// rendering, and a page is not worth abandoning over one bad operand.
func parsePrefixFloat(s string) float64 {
	for i := len(s); i > 0; i-- {
		if v, err := strconv.ParseFloat(s[:i], 64); err == nil {
			return v
		}
	}
	return 0
}

func (l *Lexer) name() (Token, error) {
	l.pos++ // consume '/'
	var out []byte
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if isWhite(c) || isDelim(c) {
			break
		}
		if c == '#' && l.pos+2 < len(l.src) {
			hi, ok1 := hexVal(l.src[l.pos+1])
			lo, ok2 := hexVal(l.src[l.pos+2])
			if ok1 && ok2 {
				out = append(out, hi<<4|lo)
				l.pos += 3
				continue
			}
		}
		out = append(out, c)
		l.pos++
	}
	return Token{Kind: KindName, Text: out}, nil
}

func (l *Lexer) literalString() (Token, error) {
	start := l.pos
	l.pos++ // consume '('
	depth := 1
	var out []byte
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		l.pos++
		switch c {
		case '\\':
			if l.pos >= len(l.src) {
				return Token{}, fmt.Errorf("content: string at offset %d ends in a backslash", start)
			}
			e := l.src[l.pos]
			l.pos++
			switch e {
			case 'n':
				out = append(out, '\n')
			case 'r':
				out = append(out, '\r')
			case 't':
				out = append(out, '\t')
			case 'b':
				out = append(out, '\b')
			case 'f':
				out = append(out, '\f')
			case '(', ')', '\\':
				out = append(out, e)
			case '\r':
				if l.pos < len(l.src) && l.src[l.pos] == '\n' {
					l.pos++
				}
			case '\n':
				// line continuation: contributes nothing
			default:
				if e >= '0' && e <= '7' {
					v := int(e - '0')
					for k := 0; k < 2 && l.pos < len(l.src); k++ {
						d := l.src[l.pos]
						if d < '0' || d > '7' {
							break
						}
						v = v*8 + int(d-'0')
						l.pos++
					}
					out = append(out, byte(v))
				} else {
					out = append(out, e)
				}
			}
		case '(':
			depth++
			out = append(out, c)
		case ')':
			depth--
			if depth == 0 {
				return Token{Kind: KindString, Text: out}, nil
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return Token{}, fmt.Errorf("content: unterminated literal string at offset %d", start)
}

func (l *Lexer) hexString() (Token, error) {
	start := l.pos
	l.pos++ // consume '<'
	var out []byte
	var cur byte
	var half bool
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		l.pos++
		if c == '>' {
			if half {
				out = append(out, cur<<4) // an odd digit count pads with zero
			}
			return Token{Kind: KindString, Text: out}, nil
		}
		v, ok := hexVal(c)
		if !ok {
			continue // whitespace and stray bytes are ignored
		}
		if half {
			out = append(out, cur<<4|v)
			half = false
		} else {
			cur = v
			half = true
		}
	}
	return Token{}, fmt.Errorf("content: unterminated hex string at offset %d", start)
}

// inlineImage consumes BI ... ID <binary> EI as a single token. The sample data
// is not decoded: the walker only needs to know that an inline image is
// present. The payload is skipped verbatim because it may contain any byte
// sequence, including text that looks like operators.
//
// The end of the payload is a computed offset whenever the dictionary
// determines one, and a delimiter search only when it does not. ISO 32000-1
// 8.9.7 puts EI after the sample data but requires no whitespace in between,
// so searching for a whitespace-preceded EI both misses real terminators and
// stops at false ones inside the samples. Four govdocs1 documents (050142,
// 050289, 900366, 900581) hold 19 unfiltered inline images between them; the
// computed length lands on EI for all 19, and only one of the 19 has a
// whitespace byte before it. On 050289 page 25 the search ran past two images
// and reported three as one, with no error at all -- see byb-8ly.
func (l *Lexer) inlineImage(start int) (Token, error) {
	dict := l.pos
	if !l.seekKeyword("ID") {
		return Token{}, fmt.Errorf("content: inline image at offset %d has no ID", start)
	}
	id := l.pos - 2
	if l.pos < len(l.src) && isWhite(l.src[l.pos]) {
		l.pos++ // exactly one whitespace byte separates ID from the samples
	}
	hdr := parseInlineDict(l.src[dict:id])
	if n, ok := hdr.dataLen(); ok {
		// EI must sit exactly at the computed end. Anything else means the
		// dictionary and the payload disagree, and the search is the better
		// guess; it is what every such document got before this path existed.
		if end := int64(l.pos) + n; end+2 <= int64(len(l.src)) &&
			l.src[end] == 'E' && l.src[end+1] == 'I' {
			l.pos = int(end) + 2
			return Token{Kind: KindInlineImage}, nil
		}
	}
	if len(hdr.eod) > 0 {
		// Same contract as the computed length: the marker fixes the end, and
		// EI has to be there, or the search takes over.
		if i := bytes.Index(l.src[l.pos:], hdr.eod); i >= 0 {
			end := l.pos + i + len(hdr.eod)
			for end < len(l.src) && isWhite(l.src[end]) {
				end++
			}
			if end+2 <= len(l.src) && l.src[end] == 'E' && l.src[end+1] == 'I' {
				l.pos = end + 2
				return Token{Kind: KindInlineImage}, nil
			}
		}
	}
	for l.pos+1 < len(l.src) {
		if l.src[l.pos] == 'E' && l.src[l.pos+1] == 'I' &&
			l.pos > 0 && isWhite(l.src[l.pos-1]) &&
			(l.pos+2 == len(l.src) || isWhite(l.src[l.pos+2]) || isDelim(l.src[l.pos+2])) {
			l.pos += 2
			return Token{Kind: KindInlineImage}, nil
		}
		l.pos++
	}
	l.pos = len(l.src)
	return Token{}, fmt.Errorf("content: inline image at offset %d has no EI", start)
}

// inlineHeader is what a BI ... ID dictionary states about the sample data that
// follows it. Keys use the abbreviations of ISO 32000-1 Table 93 and the full
// names alike, because both appear in real files.
type inlineHeader struct {
	w, h, bpc, comps int
	mask, filtered   bool
	// eod is the end-of-data marker of the outermost filter, empty unless that
	// filter has one.
	eod []byte
}

// parseInlineDict reads the bytes between BI and ID. It reports what it could
// read and never fails: a dictionary this lexer cannot finish is simply one
// that determines less, and every caller has a fallback.
func parseInlineDict(dict []byte) inlineHeader {
	var h inlineHeader
	lx := NewLexer(dict)
	for {
		tok, err := lx.Next()
		if err != nil {
			break
		}
		if tok.Kind != KindName {
			continue
		}
		key := string(tok.Text)
		val, err := lx.Next()
		if err != nil {
			break
		}
		switch key {
		case "W", "Width":
			h.w, _ = inlineInt(val)
		case "H", "Height":
			h.h, _ = inlineInt(val)
		case "BPC", "BitsPerComponent":
			h.bpc, _ = inlineInt(val)
		case "IM", "ImageMask":
			h.mask = val.Kind == KindKeyword && string(val.Text) == "true"
		case "F", "Filter":
			h.filtered = true
			// The outermost filter is the one whose bytes are on the wire, and
			// in an array that is the first entry.
			h.eod = inlineFilterEOD(firstOfArrayOrSelf(lx, val))
		case "CS", "ColorSpace":
			// The family is the first element; the rest of the array holds a
			// base space and a lookup table this does not need.
			h.comps, _ = inlineComponents(firstOfArrayOrSelf(lx, val))
		}
	}
	return h
}

// firstOfArrayOrSelf returns val, or the first element of the array val opens,
// consuming the rest of that array either way.
func firstOfArrayOrSelf(lx *Lexer, val Token) Token {
	if val.Kind != KindArrayOpen {
		return val
	}
	first, err := lx.Next()
	if err != nil {
		return Token{}
	}
	skipArray(lx)
	return first
}

// inlineFilterEOD returns the end-of-data marker of a filter that has one.
// ASCII85 ends at "~>" and ASCIIHex at ">", and neither marker's bytes belong
// to its own alphabet, so the first occurrence after ID is the real end. Every
// other filter ends only where its own decoder says so, which this lexer does
// not run; those return nil and the caller searches for EI.
func inlineFilterEOD(t Token) []byte {
	if t.Kind != KindName {
		return nil
	}
	switch string(t.Text) {
	case "A85", "ASCII85Decode":
		return []byte("~>")
	case "AHx", "ASCIIHexDecode":
		return []byte(">")
	}
	return nil
}

// dataLen returns the number of sample bytes an unfiltered inline image holds.
// The second result is false whenever the length is not determined: a filtered
// image, a colour space whose component count the dictionary does not state, or
// any missing dimension. A false result is not an error.
func (h inlineHeader) dataLen() (int64, bool) {
	w, bpc, comps := h.w, h.bpc, h.comps
	if h.filtered || w <= 0 || h.h <= 0 {
		return 0, false
	}
	if h.mask {
		// An image mask is one bit per sample and declares no colour space.
		comps, bpc = 1, 1
	}
	if comps <= 0 || bpc <= 0 {
		return 0, false
	}
	// Bound each product before taking it. /W, /H and /BPC come out of the file
	// and nothing obliges them to be sensible, so a per-field limit is not
	// enough: 2e9 by 2e9 at 8 bits and 3 components passes every field check and
	// still multiplies out past int64. A wrapped length is worse than no length,
	// because it reads as an offset. maxInlineSamples is far above any content
	// stream that fits in memory and far below where int64 stops counting.
	const maxInlineSamples = 1 << 40
	if int64(w) > maxInlineSamples/(int64(bpc)*int64(comps)) {
		return 0, false
	}
	row := (int64(w)*int64(bpc)*int64(comps) + 7) / 8
	if row > maxInlineSamples/int64(h.h) {
		return 0, false
	}
	return row * int64(h.h), true
}

// inlineComponents returns the number of colour components a colour space name
// carries. Only the spaces whose component count is fixed by the name appear
// here: /ICCBased takes its count from a stream, and a name from the page's
// resources is not resolvable from the dictionary alone.
func inlineComponents(t Token) (int, bool) {
	if t.Kind != KindName {
		return 0, false
	}
	switch string(t.Text) {
	case "G", "DeviceGray", "CalGray":
		return 1, true
	case "RGB", "DeviceRGB", "CalRGB", "Lab":
		return 3, true
	case "CMYK", "DeviceCMYK":
		return 4, true
	case "I", "Indexed":
		return 1, true // one index per sample, whatever the base space is
	}
	return 0, false
}

// inlineInt reads a dimension. Anything negative or absurd is rejected rather
// than converted, so that a hostile /W cannot produce a nonsense offset.
func inlineInt(t Token) (int, bool) {
	if t.Kind != KindNumber || t.Num < 0 || t.Num > 1<<31 {
		return 0, false
	}
	return int(t.Num), true
}

// skipArray consumes tokens through the close of an array already opened.
func skipArray(lx *Lexer) {
	for depth := 1; depth > 0; {
		tok, err := lx.Next()
		if err != nil {
			return
		}
		switch tok.Kind {
		case KindArrayOpen:
			depth++
		case KindArrayClose:
			depth--
		}
	}
}

// seekKeyword advances past the next standalone occurrence of kw.
func (l *Lexer) seekKeyword(kw string) bool {
	for l.pos+len(kw) <= len(l.src) {
		if string(l.src[l.pos:l.pos+len(kw)]) == kw &&
			(l.pos == 0 || isWhite(l.src[l.pos-1]) || isDelim(l.src[l.pos-1])) &&
			(l.pos+len(kw) == len(l.src) || isWhite(l.src[l.pos+len(kw)]) || isDelim(l.src[l.pos+len(kw)])) {
			l.pos += len(kw)
			return true
		}
		l.pos++
	}
	return false
}
