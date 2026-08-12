package jbig2

// byb-9v0: the symbol dictionary and text region decoder, against a stream a
// real encoder wrote and a reference decoder's answer for it.
//
// THE FIXTURE IS NOT HAND-BUILT AND THAT IS THE POINT. Every other decode test
// in this package feeds the decoder something this package's own encoder
// produced, which proves the two directions agree and proves nothing about
// whether either agrees with the format. Symbol mode has no encoder here to
// disagree with -- byblos writes generic regions only -- so the only way to know
// this decoder reads T.88 rather than a plausible misreading of it is to decode
// somebody else's bytes and compare against somebody else's decoder.
//
// PROVENANCE OF testdata/jbig2/symbol-*, reproducible with jbig2enc 0.29 and
// jbig2dec 0.20 from Homebrew:
//
//	jbig2 -s -p -b sym page.png     # symbol mode, PDF-embedded organization
//	  -> sym.sym   = symbol-globals.jb2, one type-0 symbol dictionary, page 0
//	  -> sym.0000  = symbol-page.jb2, a page information segment and one
//	                 type-6 immediate text region referring to segment 0
//	jbig2dec -e -t pbm -o ref.pbm sym.sym sym.0000
//	  -> symbol-page.pbm
//
// page.png is 640x480 of a twelve-glyph alphabet repeated 695 times, which is
// what makes an encoder choose symbol mode at all. The encoder's own
// classification is lossless on it -- the PBM carries the same 45,104 ink pixels
// as the source PNG -- so the golden is the source image as well as the
// reference decode.
//
// THE SHAPE OF THIS FIXTURE IS THE SHAPE byb-9v0 EXISTS FOR. The dictionary is
// in the globals stream and the page stream holds only a text region: the 263
// corpus pages whose first unsupported segment is a text region are exactly
// this, and they are undecodable from the image stream alone however complete
// the decoder is.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "jbig2", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// readPBM parses a binary PBM (P4). In PBM a 1 bit is black, which is also this
// package's ink convention, so the rows transfer without inversion.
func readPBM(t *testing.T, b []byte) *Bitmap {
	t.Helper()
	fields := make([]string, 0, 3)
	i := 0
	for len(fields) < 3 {
		for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
			i++
		}
		if i < len(b) && b[i] == '#' {
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		}
		start := i
		for i < len(b) && b[i] != ' ' && b[i] != '\n' && b[i] != '\t' && b[i] != '\r' {
			i++
		}
		if start == i {
			t.Fatalf("PBM header ran out after %d fields", len(fields))
		}
		fields = append(fields, string(b[start:i]))
	}
	i++ // the single whitespace byte after the height
	if fields[0] != "P4" {
		t.Fatalf("PBM magic is %q; want P4", fields[0])
	}
	w, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("PBM width %q: %v", fields[1], err)
	}
	h, err := strconv.Atoi(fields[2])
	if err != nil {
		t.Fatalf("PBM height %q: %v", fields[2], err)
	}
	bm := NewBitmap(w, h)
	if want := bm.Stride * h; len(b)-i < want {
		t.Fatalf("PBM carries %d bytes of raster; want %d for %dx%d", len(b)-i, want, w, h)
	}
	copy(bm.Pix, b[i:])
	return bm
}

func inkCount(b *Bitmap) int {
	n := 0
	for _, v := range b.Pix {
		for ; v != 0; v &= v - 1 {
			n++
		}
	}
	return n
}

// diff reports where two bitmaps of the same size differ, as a count and the
// first few coordinates. A symbol-mode decoder that is wrong is usually wrong in
// a STRUCTURED way -- one glyph substituted, or every glyph after the first
// shifted -- so the coordinates say more than the count does.
func diff(t *testing.T, got, want *Bitmap) {
	t.Helper()
	if got.W != want.W || got.H != want.H {
		t.Fatalf("decoded %dx%d; want %dx%d", got.W, got.H, want.W, want.H)
	}
	var n int
	var first []string
	for y := 0; y < want.H; y++ {
		for x := 0; x < want.W; x++ {
			if got.Get(x, y) != want.Get(x, y) {
				n++
				if len(first) < 8 {
					first = append(first, fmt.Sprintf("(%d,%d) got %d want %d",
						x, y, got.Get(x, y), want.Get(x, y)))
				}
			}
		}
	}
	if n != 0 {
		t.Errorf("%d of %d pixels differ from jbig2dec's decode (%d ink vs %d); first: %v",
			n, want.W*want.H, inkCount(got), inkCount(want), first)
	}
}

// The whole of byb-9v0 in one assertion: a symbol dictionary in a globals stream
// and a text region in a page stream must decode to exactly what jbig2dec
// decodes them to.
func TestDecodeSymbolModeMatchesJBIG2Dec(t *testing.T) {
	globals := readFixture(t, "symbol-globals.jb2")
	page := readFixture(t, "symbol-page.jb2")
	want := readPBM(t, readFixture(t, "symbol-page.pbm"))

	got, err := DecodeEmbeddedStreamWithGlobals(globals, page)
	if err != nil {
		t.Fatalf("DecodeEmbeddedStreamWithGlobals() error = %v", err)
	}
	diff(t, got, want)
}

// Without the globals the same page stream must FAIL, and fail saying why. This
// is the assertion that stops the plumbing from being quietly optional: a
// decoder that treated a missing dictionary as an empty one would return a blank
// 640x480 page and no error, which is a raster that is wrong without looking
// wrong -- the outcome this package's doc comment calls strictly worse than an
// error.
func TestTextRegionWithoutItsGlobalsIsAnErrorNotABlankPage(t *testing.T) {
	page := readFixture(t, "symbol-page.jb2")
	got, err := DecodeEmbeddedStream(page)
	if err == nil {
		t.Fatalf("DecodeEmbeddedStream() returned a %dx%d bitmap with %d ink pixels from a text region "+
			"whose symbol dictionary is in a stream it was not given", got.W, got.H, inkCount(got))
	}
}

// The golden is a claim about what jbig2dec says, so it is re-asked whenever
// jbig2dec is installed. Without this the fixture could drift from the tool it
// was taken from and the test above would keep agreeing with a stale answer.
func TestSymbolGoldenStillMatchesJBIG2Dec(t *testing.T) {
	if _, err := exec.LookPath("jbig2dec"); err != nil {
		t.Skipf("jbig2dec not installed (brew install jbig2dec): %v", err)
	}
	dir := t.TempDir()
	g := filepath.Join(dir, "globals.jb2")
	p := filepath.Join(dir, "page.jb2")
	out := filepath.Join(dir, "out.pbm")
	if err := os.WriteFile(g, readFixture(t, "symbol-globals.jb2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, readFixture(t, "symbol-page.jb2"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("jbig2dec", "-e", "-t", "pbm", "-o", out, g, p)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("jbig2dec: %v: %s", err, b)
	}
	fresh, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if golden := readFixture(t, "symbol-page.pbm"); !bytes.Equal(fresh, golden) {
		t.Errorf("jbig2dec now decodes the fixture to %d bytes of PBM and the committed golden is %d; "+
			"re-take the golden and check what changed before trusting it", len(fresh), len(golden))
	}
}

// The two legal symbol-mode variants byblos declines, mutated into the fixture's
// own headers so that everything else about the stream stays valid.
//
// Both must come back as ErrUnsupportedFeature rather than as damage or as a
// raster: an archive acting on the difference between "byblos is not enough" and
// "the bytes are broken" gets the wrong answer otherwise (jbig2.go).
func TestSymbolModeRefusesTheVariantsItDoesNotCode(t *testing.T) {
	globals := readFixture(t, "symbol-globals.jb2")
	page := readFixture(t, "symbol-page.jb2")

	// The symbol dictionary segment header is 11 bytes, so its flags word is at
	// bytes 11 and 12 of the globals stream. Bit 0 is SDHUFF and bit 1 SDREFAGG.
	if globals[11] != 0 || globals[12] != 0 {
		t.Fatalf("fixture symbol dictionary flags are %02X %02X; want 00 00", globals[11], globals[12])
	}
	// The text region is the second segment of the page stream: 11 bytes of page
	// information header, 19 of its data, 11 of the text region header, then 17
	// of region information, so the text region flags word is at 58 and 59.
	// Bit 0 of the low byte is SBHUFF and bit 1 SBREFINE.
	const textFlagsAt = 11 + 19 + 12 + 17 + 1
	if page[textFlagsAt-1] != 0 || page[textFlagsAt] != 0 {
		t.Fatalf("fixture text region flags are %02X %02X; want 00 00",
			page[textFlagsAt-1], page[textFlagsAt])
	}

	for _, c := range []struct {
		name          string
		globals, page []byte
	}{
		{"sdhuff", setByte(globals, 12, 0x01), page},
		{"sdrefagg", setByte(globals, 12, 0x02), page},
		{"sbhuff", globals, setByte(page, textFlagsAt, 0x01)},
		{"sbrefine", globals, setByte(page, textFlagsAt, 0x02)},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := DecodeEmbeddedStreamWithGlobals(c.globals, c.page)
			if err == nil {
				t.Fatalf("decoded a %dx%d bitmap; want ErrUnsupportedFeature", got.W, got.H)
			}
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("error = %v; want ErrUnsupportedFeature", err)
			}
		})
	}
}
