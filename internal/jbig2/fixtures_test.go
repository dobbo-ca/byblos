package jbig2

import (
	"bytes"
	"io"
	"testing"

	"golang.org/x/image/ccitt"
)

// figureH6MMR is the MMR-coded region data of T.88 Annex H.1 segment 4 (file
// offset 0x00D0, doc p. 130), which encodes the same 54x44 bitmap that Annex
// H.1 segment 11 encodes arithmetically. Decoding it recovers the exact fixture
// the arithmetic conformance vector expects, straight from the spec.
var figureH6MMR = []byte{
	0x26, 0xA0, 0x71, 0xCE, 0xA7,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xF8, 0xF0,
}

// figureH6 returns T.88 Figure H.6: a 54x44 rectangle with a 2-pixel border.
func figureH6() *Bitmap {
	b := NewBitmap(54, 44)
	for y := 0; y < 44; y++ {
		for x := 0; x < 54; x++ {
			if y < 2 || y >= 42 || x < 2 || x >= 52 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// TestFigureH6MatchesSpecMMR cross-checks the hand-written figureH6 against the
// spec's own MMR encoding of the same figure. If x/image/ccitt ever changes the
// meaning of Invert or the Group4 sub-format this test fails rather than
// silently validating the wrong fixture; in that case fix the decode options,
// and only if that is impossible fall back to figureH6 alone and note in the
// commit message that the cross-check was lost.
func TestFigureH6MatchesSpecMMR(t *testing.T) {
	// ccitt yields 1 = white by default; Invert makes 1 = black, matching both
	// JBIG2 and this package's convention.
	r := ccitt.NewReader(bytes.NewReader(figureH6MMR), ccitt.MSB, ccitt.Group4,
		54, 44, &ccitt.Options{Invert: true})
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("MMR decode of T.88 H.1 segment 4 failed: %v", err)
	}
	want := figureH6()
	if len(got) != len(want.Pix) {
		t.Fatalf("MMR decode produced %d bytes; want %d", len(got), len(want.Pix))
	}
	if !bytes.Equal(got, want.Pix) {
		t.Fatalf("MMR-decoded Figure H.6 differs from the hand-built fixture\ngot  % 02X\nwant % 02X",
			got, want.Pix)
	}
}

// noiseBitmap is a deterministic pseudo-random bitmap: the worst case for an
// adaptive arithmetic coder, and the case most likely to expose a carry or
// byte-stuffing bug.
func noiseBitmap(w, h int, seed uint32) *Bitmap {
	b := NewBitmap(w, h)
	s := seed
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			s = s*1664525 + 1013904223
			if s>>29&1 == 1 {
				b.Set(x, y, 1)
			}
		}
	}
	return b
}

// textPageBitmap synthesises a page that behaves like a scan of text: wide
// white margins, blank inter-line gaps, and rows of small ink blocks standing
// in for glyphs. Deterministic, so sizes are comparable across runs.
func textPageBitmap(w, h int) *Bitmap {
	b := NewBitmap(w, h)
	s := uint32(20260727)
	next := func(n int) int {
		s = s*1664525 + 1013904223
		return int(s>>16) % n
	}
	const (
		marginX  = 40
		marginY  = 60
		lineStep = 26
		glyphW   = 6
		glyphH   = 11
		glyphGap = 2
		wordGap  = 7
	)
	for top := marginY; top+glyphH < h-marginY; top += lineStep {
		x := marginX
		for x < w-marginX-glyphW {
			word := 2 + next(7)
			for i := 0; i < word && x < w-marginX-glyphW; i++ {
				for gy := 0; gy < glyphH; gy++ {
					for gx := 0; gx < glyphW; gx++ {
						// Hollow the middle so glyphs are strokes, not blocks.
						if gy == 0 || gy == glyphH-1 || gx == 0 || gx == glyphW-1 || (gy+gx)%5 == 0 {
							b.Set(x+gx, top+gy, 1)
						}
					}
				}
				x += glyphW + glyphGap
			}
			x += wordGap
		}
	}
	return b
}

// fixtureBitmaps is the corpus every downstream test iterates over. It spans
// the structural cases that break naive implementations: non-byte-aligned
// widths, single-pixel dimensions, all-background, all-ink, and pure noise.
func fixtureBitmaps() map[string]*Bitmap {
	all := NewBitmap(200, 120)
	for y := 0; y < 120; y++ {
		for x := 0; x < 200; x++ {
			all.Set(x, y, 1)
		}
	}
	return map[string]*Bitmap{
		"border": figureH6(),
		"empty":  NewBitmap(200, 120),
		"full":   all,
		"noise":  noiseBitmap(101, 73, 12345),
		"odd":    noiseBitmap(13, 11, 99),
		"single": NewBitmap(1, 1),
		"column": noiseBitmap(1, 500, 7),
		"row":    noiseBitmap(500, 1, 11),
		"text":   textPageBitmap(640, 480),
	}
}
