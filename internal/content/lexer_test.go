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

// samples returns n bytes that contain no "EI" and end in a non-whitespace byte,
// so that a scan for a whitespace-preceded EI cannot terminate inside them.
func samples(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('A' + i%16) // 0x41..0x50, no 'E' followed by 'I'
	}
	return string(b)
}

// ISO 32000-1 8.9.7 puts EI after the sample data. It does not require a
// whitespace byte in between, and four govdocs1 documents (050142, 050289,
// 900366, 900581) are written without one. For an unfiltered image the sample
// count is fully determined by /W, /H, /BPC and the /CS component count, so the
// end of the data is a computed offset rather than a delimiter to search for.
func TestLexerInlineImageEndsAtComputedLengthWithNoWhitespaceBeforeEI(t *testing.T) {
	// 4 wide, 2 high, 8 bits, 1 component = 8 sample bytes.
	src := "q\nBI\n/W 4 /H 2 /BPC 8 /CS /G\nID " + samples(8) + "EI\nQ"
	got := lex(t, src)
	if len(got) != 3 {
		t.Fatalf("got %d tokens, want 3: %+v", len(got), got)
	}
	if got[1].Kind != KindInlineImage {
		t.Errorf("token 1 kind = %v; want KindInlineImage", got[1].Kind)
	}
	if got[2].Kind != KindKeyword || string(got[2].Text) != "Q" {
		t.Errorf("token 2 = {%v %q}; want keyword Q", got[2].Kind, got[2].Text)
	}
}

// The shape measured on 050289.pdf page 25: the first image's data ends without
// whitespace, so a scan runs past its EI and stops at a LATER image's EI. Three
// images were reported as one, with no error, and the walk resumed in the wrong
// place. An under-count is worse than a refusal because nothing announces it.
func TestLexerInlineImagesAreNotMergedAtALaterImagesEI(t *testing.T) {
	// First image ends on a sample byte; the second ends on a space, which is
	// the only whitespace-preceded EI in the stream.
	src := "BI /W 8 /H 2 /BPC 8 /CS /G ID " + samples(16) + "EI " +
		"BI /W 4 /H 2 /BPC 8 /CS /G ID " + samples(7) + " EI Q"
	got := lex(t, src)
	var inline int
	for _, tok := range got {
		if tok.Kind == KindInlineImage {
			inline++
		}
	}
	if inline != 2 {
		t.Fatalf("got %d inline images, want 2 (tokens: %+v)", inline, got)
	}
	if len(got) != 3 {
		t.Fatalf("got %d tokens, want 3: %+v", len(got), got)
	}
}

// The component count of the colour space is a multiplier on the sample length,
// so a wrong one puts EI at the wrong offset. Each case here supplies exactly
// the bytes its dictionary implies and no whitespace-preceded EI anywhere, so a
// wrong count cannot be rescued by the fallback scan: it becomes an error.
func TestLexerInlineImageComputesLengthPerColourSpace(t *testing.T) {
	for _, tc := range []struct {
		name, dict string
		bytes      int
	}{
		{"gray", "/W 4 /H 2 /BPC 8 /CS /G", 8},
		{"rgb", "/W 4 /H 2 /BPC 8 /CS /RGB", 24},
		{"cmyk", "/W 4 /H 2 /BPC 8 /CS /CMYK", 32},
		{"device names in full", "/W 4 /H 2 /BPC 8 /CS /DeviceRGB", 24},
		{"indexed is one sample per pixel", "/W 5 /H 2 /BPC 4 /CS [/I /RGB 3 <AABBCC>]", 6},
		{"sub-byte rows pad to a byte", "/W 3 /H 4 /BPC 4 /CS /G", 8},
		{"image mask is one bit per sample", "/W 9 /H 2 /IM true", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "BI " + tc.dict + " ID " + samples(tc.bytes) + "EI Q"
			got := lex(t, src)
			if len(got) != 2 || got[0].Kind != KindInlineImage {
				t.Fatalf("got %d tokens %+v; want [inline image, Q]", len(got), got)
			}
		})
	}
}

// /W and /H come out of the file, so their product is not bounded by anything
// the file has to honour. Bounding each one on its own is not enough: 2e9 by
// 2e9 at 8 bits and 3 components multiplies out past int64, wraps negative,
// passes a bounds check written as an upper limit, and indexes the source
// backwards. content.Walk has no recover(), so that panic reaches whoever
// called Inspect.
func TestLexerInlineImageDoesNotOverflowOnHugeDimensions(t *testing.T) {
	for _, tc := range []struct{ name, dict string }{
		// row x height overflows: the row fits, the page does not.
		{"rows overflow int64", "/W 2000000000 /H 2000000000 /BPC 8 /CS /RGB"},
		// width x depth x components overflows before a row is even taken.
		{"one row overflows int64", "/W 2000000000 /H 2 /BPC 2000000000 /CS /CMYK"},
		// Representable, merely far longer than the stream: the length is
		// computed fine and the scan takes over because EI cannot be there.
		{"length exceeds the stream", "/W 1000000 /H 1000000 /BPC 8 /CS /G"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lex(t, "BI "+tc.dict+" ID xx EI Q")
			if len(got) != 2 {
				t.Fatalf("got %d tokens, want 2: %+v", len(got), got)
			}
			if got[0].Kind != KindInlineImage {
				t.Errorf("token 0 kind = %v; want KindInlineImage", got[0].Kind)
			}
		})
	}
}

// A filtered image holds encoded bytes, so /W, /H and /BPC say nothing about
// how many there are and the delimiter scan stays as the fallback. The encoded
// data here carries a literal "EI" exactly where the unfiltered length would
// have put the terminator: ignoring /F ends the image early and leaves the
// lexer reading sample bytes as operators.
func TestLexerInlineImageWithFilterFallsBackToScanningForEI(t *testing.T) {
	src := "BI /W 4 /H 2 /BPC 8 /CS /G /F /AHx ID 41424344EI4546> EI Q"
	got := lex(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d tokens, want 2: %+v", len(got), got)
	}
	if got[0].Kind != KindInlineImage {
		t.Errorf("token 0 kind = %v; want KindInlineImage", got[0].Kind)
	}
}

// A filter can still fix the end of the data even though it hides the length.
// ASCII85 and ASCIIHex both carry an end-of-data marker, and neither marker's
// bytes belong to its own alphabet, so the first occurrence is the real one.
// This is the shape of 250028.pdf page 53: fourteen images, every one
// /F [/A85 /Fl], and the scan stopped on an "EI" 81 bytes into the ASCII85 of
// the first image, which is why that page was reported as a stray ')'.
func TestLexerInlineImageEndsAtTheFilterEODMarker(t *testing.T) {
	// Each payload plants a whitespace-preceded EI, and a bare EOD-looking byte
	// for the other filter, ahead of the real marker.
	for _, tc := range []struct{ name, src string }{
		{"ascii85 in an array, as 250028 writes it",
			"q BI /W 8 /H 4 /BPC 8 /CS /RGB /F [/A85 /Fl] ID 9jqo^ EI Blbd> z!!~> EI Q"},
		{"ascii85 as a bare name",
			"q BI /W 8 /H 4 /BPC 8 /CS /RGB /F /A85 ID 9jqo^ EI Blbd> z!!~> EI Q"},
		{"ascii85 spelled in full",
			"q BI /W 8 /H 4 /BPC 8 /CS /RGB /F /ASCII85Decode ID 9jqo^ EI Blbd> z!!~> EI Q"},
		{"asciihex",
			"q BI /W 8 /H 4 /BPC 8 /CS /RGB /F /AHx ID 4142 EI 4344> EI Q"},
		{"asciihex spelled in full",
			"q BI /W 8 /H 4 /BPC 8 /CS /RGB /F /ASCIIHexDecode ID 4142 EI 4344> EI Q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lex(t, tc.src)
			if len(got) != 3 {
				t.Fatalf("got %d tokens, want 3: %+v", len(got), got)
			}
			if got[1].Kind != KindInlineImage {
				t.Errorf("token 1 kind = %v; want KindInlineImage", got[1].Kind)
			}
			if got[2].Kind != KindKeyword || string(got[2].Text) != "Q" {
				t.Errorf("token 2 = {%v %q}; want keyword Q", got[2].Kind, got[2].Text)
			}
		})
	}
}

// Two ASCII85 images in a row must stay two. The first payload holds no
// whitespace-preceded EI at all, so a scan runs into the second image.
func TestLexerAdjacentFilteredInlineImagesAreNotMerged(t *testing.T) {
	src := "BI /W 4 /H 2 /CS /G /F /A85 ID 9jqo^Blbd~>EI " +
		"BI /W 4 /H 2 /CS /G /F /A85 ID F*)HD~> EI Q"
	got := lex(t, src)
	var inline int
	for _, tok := range got {
		if tok.Kind == KindInlineImage {
			inline++
		}
	}
	if inline != 2 {
		t.Fatalf("got %d inline images, want 2 (tokens: %+v)", inline, got)
	}
}

// The marker fixes the end only if EI is actually there. A file that carries
// the marker's bytes inside its data gets the search, exactly as before.
func TestLexerInlineImageFallsBackWhenTheEODMarkerIsNotFollowedByEI(t *testing.T) {
	src := "BI /W 4 /H 2 /CS /G /F /AHx ID 4142>4344 EI Q"
	got := lex(t, src)
	if len(got) != 2 || got[0].Kind != KindInlineImage {
		t.Fatalf("got %d tokens %+v; want [inline image, Q]", len(got), got)
	}
}

// A filter with no end-of-data marker leaves the scan as the only option. The
// outermost filter is the one that decides, and it is the first array entry.
func TestLexerInlineImageWithAFilterThatHasNoEODStillScans(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"flate alone", "BI /W 4 /H 2 /CS /G /F /Fl ID \x01\x02~>\x03 EI Q"},
		{"flate outermost", "BI /W 4 /H 2 /CS /G /F [/Fl /A85] ID \x01\x02~>\x03 EI Q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := lex(t, tc.src)
			if len(got) != 2 || got[0].Kind != KindInlineImage {
				t.Fatalf("got %d tokens %+v; want [inline image, Q]", len(got), got)
			}
		})
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
	f.Add("BI /W 2000000000 /H 2000000000 /BPC 8 /CS /RGB ID xx EI Q")
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
