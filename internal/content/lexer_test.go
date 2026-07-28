package content

import (
	"errors"
	"io"
	"testing"
)

// lex drains a lexer, failing the test on any error other than io.EOF.
func lex(t *testing.T, src string) []Token {
	t.Helper()
	l := NewLexer([]byte(src))
	var out []Token
	for {
		tok, err := l.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("Next() error = %v after %d tokens", err, len(out))
		}
		out = append(out, tok)
	}
}

func TestLexerOperatorSequence(t *testing.T) {
	got := lex(t, "q 612 0 0 792 0 0 cm /Im0 Do Q")
	want := []struct {
		kind Kind
		num  float64
		text string
	}{
		{KindKeyword, 0, "q"},
		{KindNumber, 612, ""},
		{KindNumber, 0, ""},
		{KindNumber, 0, ""},
		{KindNumber, 792, ""},
		{KindNumber, 0, ""},
		{KindNumber, 0, ""},
		{KindKeyword, 0, "cm"},
		{KindName, 0, "Im0"},
		{KindKeyword, 0, "Do"},
		{KindKeyword, 0, "Q"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Num != w.num || string(got[i].Text) != w.text {
			t.Errorf("token %d = {%v %v %q}; want {%v %v %q}",
				i, got[i].Kind, got[i].Num, got[i].Text, w.kind, w.num, w.text)
		}
	}
}

func TestLexerNumbers(t *testing.T) {
	got := lex(t, "-3 .5 4. +2 0.000")
	want := []float64{-3, 0.5, 4, 2, 0}
	if len(got) != len(want) {
		t.Fatalf("got %d tokens, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Kind != KindNumber || got[i].Num != w {
			t.Errorf("token %d = {%v %v}; want number %v", i, got[i].Kind, got[i].Num, w)
		}
	}
}

// Real-world content streams contain malformed reals. Viewers take the longest
// valid prefix rather than abandoning the page; so do we.
func TestLexerMalformedRealTakesLongestValidPrefix(t *testing.T) {
	got := lex(t, "1.-2 cm")
	if len(got) != 2 || got[0].Kind != KindNumber || got[0].Num != 1 {
		t.Fatalf("got %+v; want [number 1, keyword cm]", got)
	}
}

func TestLexerLiteralStringEscapes(t *testing.T) {
	got := lex(t, `(a\(b\)c\n\101\\z) Tj`)
	if len(got) != 2 || got[0].Kind != KindString {
		t.Fatalf("got %+v; want [string, keyword]", got)
	}
	if want := "a(b)c\nA\\z"; string(got[0].Text) != want {
		t.Errorf("string = %q; want %q", got[0].Text, want)
	}
}

func TestLexerLiteralStringNestedParens(t *testing.T) {
	got := lex(t, "((nested) ok) Tj")
	if len(got) != 2 || string(got[0].Text) != "(nested) ok" {
		t.Fatalf("got %+v; want the nested parens preserved", got)
	}
}

// A backslash before a newline is a line continuation and contributes nothing.
func TestLexerLiteralStringLineContinuation(t *testing.T) {
	got := lex(t, "(ab\\\ncd) Tj")
	if len(got) != 2 || string(got[0].Text) != "abcd" {
		t.Fatalf("string = %q; want \"abcd\"", got[0].Text)
	}
}

// An odd number of hex digits is padded with a trailing zero (ISO 32000-1
// section 7.3.4.3).
func TestLexerHexStringOddDigitCount(t *testing.T) {
	got := lex(t, "<4869 7> Tj")
	if len(got) != 2 || got[0].Kind != KindString {
		t.Fatalf("got %+v; want [string, keyword]", got)
	}
	if want := []byte{0x48, 0x69, 0x70}; string(got[0].Text) != string(want) {
		t.Errorf("string = % 02x; want % 02x", got[0].Text, want)
	}
}

func TestLexerDictAndArrayDelimiters(t *testing.T) {
	got := lex(t, "<< /A [1 2] >> BDC")
	kinds := []Kind{KindDictOpen, KindName, KindArrayOpen, KindNumber, KindNumber, KindArrayClose, KindDictClose, KindKeyword}
	if len(got) != len(kinds) {
		t.Fatalf("got %d tokens, want %d: %+v", len(got), len(kinds), got)
	}
	for i, k := range kinds {
		if got[i].Kind != k {
			t.Errorf("token %d kind = %v; want %v", i, got[i].Kind, k)
		}
	}
}

func TestLexerNameHexEscape(t *testing.T) {
	got := lex(t, "/A#20B Do")
	if len(got) != 2 || got[0].Kind != KindName || string(got[0].Text) != "A B" {
		t.Fatalf("got %+v; want name \"A B\"", got)
	}
}

func TestLexerSkipsComments(t *testing.T) {
	got := lex(t, "% a comment with ( unbalanced\nq % trailing\nQ")
	if len(got) != 2 || string(got[0].Text) != "q" || string(got[1].Text) != "Q" {
		t.Fatalf("got %+v; want [q, Q]", got)
	}
}

// An inline image's binary payload must never be tokenized: it can contain any
// byte sequence, including things that look like operators.
func TestLexerInlineImageIsOneToken(t *testing.T) {
	src := "BI /W 2 /H 2 /BPC 8 /CS /G ID \x00q Q(\xff EI Q"
	got := lex(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(got), got)
	}
	if got[0].Kind != KindInlineImage {
		t.Errorf("token 0 kind = %v; want KindInlineImage", got[0].Kind)
	}
	if got[1].Kind != KindKeyword || string(got[1].Text) != "Q" {
		t.Errorf("token 1 = {%v %q}; want keyword Q", got[1].Kind, got[1].Text)
	}
}

func TestLexerErrors(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"unterminated literal string", "(abc"},
		{"unterminated hex string", "<4869"},
		{"stray close paren", ") Tj"},
		{"inline image without EI", "BI /W 1 ID \x00\x01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := NewLexer([]byte(tc.src))
			for {
				_, err := l.Next()
				if errors.Is(err, io.EOF) {
					t.Fatal("reached EOF without an error")
				}
				if err != nil {
					return
				}
			}
		})
	}
}

// The lexer must always make progress, or a malformed stream becomes a hang.
func TestLexerAlwaysAdvances(t *testing.T) {
	l := NewLexer([]byte("q\x00\x00 Q"))
	prev := -1
	for i := 0; i < 100; i++ {
		_, err := l.Next()
		if err != nil {
			return
		}
		if l.pos <= prev {
			t.Fatalf("lexer did not advance past offset %d", prev)
		}
		prev = l.pos
	}
}

func FuzzLexer(f *testing.F) {
	f.Add("q 612 0 0 792 0 0 cm /Im0 Do Q")
	f.Add("BT (a) Tj ET")
	f.Add("BI /W 1 ID \x00 EI")
	f.Add("<< /A [1 2] >> BDC")
	f.Fuzz(func(t *testing.T, s string) {
		l := NewLexer([]byte(s))
		for i := 0; i <= len(s)+1; i++ {
			if _, err := l.Next(); err != nil {
				return
			}
		}
		t.Fatalf("lexer produced more tokens than input bytes for %q", s)
	})
}
