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

// inlineImage consumes BI ... ID <binary> EI as a single token. The dictionary
// and sample data are not decoded: the walker only needs to know that an inline
// image is present. The payload is skipped verbatim because it may contain any
// byte sequence, including text that looks like operators.
func (l *Lexer) inlineImage(start int) (Token, error) {
	if !l.seekKeyword("ID") {
		return Token{}, fmt.Errorf("content: inline image at offset %d has no ID", start)
	}
	if l.pos < len(l.src) && isWhite(l.src[l.pos]) {
		l.pos++ // exactly one whitespace byte separates ID from the samples
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
