package byblos

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
)

// This file is the RED stage of byb-b3's QuantizePNG. QuantizePNG does not
// exist yet, so nothing here compiles -- that is the point.
//
// Signature (design spec, byb-b3):
//
//	func QuantizePNG(img image.Image, colors int) ([]byte, error)

// --- degenerate cases, decided on the bead -----------------------------

func TestQuantizePNGNilImageErrors(t *testing.T) {
	if _, err := QuantizePNG(nil, 16); err == nil {
		t.Fatal("QuantizePNG(nil, 16): want error, got nil")
	}
}

func TestQuantizePNGEmptyBoundsErrors(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := QuantizePNG(empty, 16); err == nil {
		t.Fatal("QuantizePNG on an empty-bounds image: want error, got nil")
	}
}

func TestQuantizePNGColorsBelowTwoErrors(t *testing.T) {
	for _, n := range []int{-1, 0, 1} {
		if _, err := QuantizePNG(corpus.Scanpage(), n); err == nil {
			t.Errorf("QuantizePNG(_, %d): want error, got nil", n)
		}
	}
}

func TestQuantizePNGColorsAbove256Errors(t *testing.T) {
	if _, err := QuantizePNG(corpus.Scanpage(), 257); err == nil {
		t.Fatal("QuantizePNG(_, 257): want error, got nil")
	}
}

func TestQuantizePNGColorsAtBoundsSucceeds(t *testing.T) {
	for _, n := range []int{2, 256} {
		if _, err := QuantizePNG(corpus.Photo(), n); err != nil {
			t.Errorf("QuantizePNG(_, %d): want no error, got %v", n, err)
		}
	}
}

func TestQuantizePNGNonOpaqueInputErrors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{100, 100, 100, 128})
		}
	}
	if _, err := QuantizePNG(img, 16); err == nil {
		t.Fatal("QuantizePNG on a non-opaque image: want error, got nil")
	}
}

// --- core behaviour ------------------------------------------------------

// decodePalette decodes b as a PNG and requires it to be palette-indexed,
// returning the *image.Paletted so callers can inspect Palette directly.
func decodePalette(t *testing.T, b []byte) *image.Paletted {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("decoding QuantizePNG output as PNG: %v", err)
	}
	pi, ok := img.(*image.Paletted)
	if !ok {
		t.Fatalf("QuantizePNG output decoded as %T; want *image.Paletted", img)
	}
	return pi
}

func TestQuantizePNGOutputIsValidPalettedPNG(t *testing.T) {
	out, err := QuantizePNG(corpus.Photo(), 64)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	if len(pi.Palette) > 64 {
		t.Errorf("palette has %d entries; want <= 64", len(pi.Palette))
	}
	if pi.Bounds().Dx() != corpus.ImageW || pi.Bounds().Dy() != corpus.ImageH {
		t.Errorf("output bounds %v; want %dx%d", pi.Bounds(), corpus.ImageW, corpus.ImageH)
	}
}

// TestQuantizePNGPaletteSizeRespected checks the palette length actually
// tracks the requested budget rather than always saturating at 256 --
// pinned separately from the bound-checking test above because a stub that
// ignores `colors` and always emits 256 entries would otherwise slip past
// "<= colors" trivially at colors=256 but be caught here at a small N on an
// image with many more than N natural colours.
func TestQuantizePNGPaletteSizeRespected(t *testing.T) {
	out, err := QuantizePNG(corpus.Photo(), 8)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	if len(pi.Palette) > 8 {
		t.Fatalf("palette has %d entries; want <= 8", len(pi.Palette))
	}
}

// TestQuantizePNGBitDepthByPaletteSize checks image/png picked the smallest
// IHDR bit depth the palette size allows -- 1/2/4/8 for palette lengths
// 2/4/16/256, verified achievable in the design's prototyping.
func TestQuantizePNGBitDepthByPaletteSize(t *testing.T) {
	for _, n := range []int{2, 4, 16, 256} {
		out, err := QuantizePNG(corpus.Photo(), n)
		if err != nil {
			t.Fatalf("QuantizePNG(_, %d): %v", n, err)
		}
		if len(out) < 25 {
			t.Fatalf("QuantizePNG(_, %d) output too short to hold an IHDR", n)
		}
		// IHDR is always the first chunk, at a fixed offset: 8-byte
		// signature + 4-byte length + 4-byte "IHDR" + 13-byte data. Bit
		// depth is data byte 8, i.e. overall offset 8+4+4+8 = 24.
		depth := out[24]
		want := map[int]byte{2: 1, 4: 2, 16: 4, 256: 8}[n]
		if depth != want {
			t.Errorf("QuantizePNG(_, %d): IHDR bit depth = %d; want %d", n, depth, want)
		}
	}
}

// TestQuantizePNGLosslessOnFewColourImage checks that an image with at most
// N distinct colours quantized to N is reproduced exactly -- Scanpage has a
// handful of flat colours by construction.
func TestQuantizePNGLosslessOnFewColourImage(t *testing.T) {
	out, err := QuantizePNG(corpus.Scanpage(), 256)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	psnr := psnrRGB(corpus.Scanpage(), pi)
	if !math.IsInf(psnr, 1) {
		t.Errorf("QuantizePNG(Scanpage, 256) PSNR = %v dB; want +Inf (lossless)", psnr)
	}
}

func TestQuantizePNGDeterministic(t *testing.T) {
	a, err := QuantizePNG(corpus.Photo(), 32)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	b, err := QuantizePNG(corpus.Photo(), 32)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("QuantizePNG produced different bytes across two calls on the same input")
	}
}

// --- byb-20b: palette population order -----------------------------------
//
// pngquant emits its palette in descending pixel-population order: index 0
// is the most-used colour, index 1 the next, and so on. QuantizePNG instead
// emits whatever order median-cut's box splitting happens to produce, which
// has no relationship to population. On a scanned page this is not a
// cosmetic difference -- it changes the compressed size. Scanpage's paper
// fill is both the dominant colour AND, when it lands away from index 0,
// stops being byte 0x00 on every scanline. PNG's Paeth/Sub/Up filters and
// the deflate/LZ77 stage that follows both work on byte streams: a
// dominant-population run at index 0 is a long run of 0x00 that deflate can
// match across row boundaries (through the filter-type byte, which is also
// 0x00 for filter type "None"/predicted-zero rows), while the same run at
// index 1 breaks at every row boundary because the intervening filter byte
// and any partial byte padding no longer line up as more zeros. Measured on
// this corpus: byblos's longest single-byte run on Scanpage is 150 bytes;
// pngquant's is 10,448.
//
// This test does not care WHERE a colour lands, only that population is
// non-increasing as the index increases -- that is the externally
// observable property pngquant guarantees and byte-run-length depends on.

// paletteIndexCounts decodes b, requires it to be a paletted PNG, and
// returns the pixel count for each palette index (length len(Palette)).
func paletteIndexCounts(t *testing.T, b []byte) []int {
	t.Helper()
	pi := decodePalette(t, b)
	counts := make([]int, len(pi.Palette))
	bnd := pi.Bounds()
	for y := bnd.Min.Y; y < bnd.Max.Y; y++ {
		for x := bnd.Min.X; x < bnd.Max.X; x++ {
			counts[pi.ColorIndexAt(x, y)]++
		}
	}
	return counts
}

func TestQuantizePNGPaletteInPopulationOrder(t *testing.T) {
	images := map[string]image.Image{
		"Scanpage": corpus.Scanpage(),
		"Photo":    corpus.Photo(),
		"Gradient": corpus.Gradient(),
		"Scanjpeg": corpus.Scanjpeg(),
	}
	for name, img := range images {
		for _, n := range []int{8, 16, 32, 64, 128, 256} {
			t.Run(fmt.Sprintf("%s/%d", name, n), func(t *testing.T) {
				out, err := QuantizePNG(img, n)
				if err != nil {
					t.Fatalf("QuantizePNG: %v", err)
				}
				counts := paletteIndexCounts(t, out)
				for i := 1; i < len(counts); i++ {
					if counts[i] > counts[i-1] {
						t.Fatalf("palette index %d has population %d, greater than index %d's %d -- "+
							"palette is not in descending-population order: %v",
							i, counts[i], i-1, counts[i-1], counts)
					}
				}
			})
		}
	}
}

// TestQuantizePNGPopulationTiesBreakByRGB pins the tie-break byb-20b
// requires: when two palette entries have EXACTLY equal population (so
// population order alone does not determine their relative index),
// pngquant's own tie-break -- and the one byb-20b adopts -- is ascending
// RGB, (r,g,b) compared AS A TUPLE: R first, then G, then B.
//
// This is not a redundant restatement of the non-increasing check above.
// The TieAtR case's colours and population split (blue/red tied at 10
// pixels each, green trailing at 5) were chosen by PROBING main: an
// earlier, differently-coloured tie between two equal-population entries
// happened to come out in ascending-RGB order anyway, because median cut's
// split-axis sort and lexicographic RGB order coincide whenever the box's
// single greatest-range axis is also the first axis the two colours differ
// on. That would have made a naive version of this test pass on main for
// the wrong reason. The colours below were picked so median cut's own
// box-splitting geometry (greatest-range axis is blue, R.B=250 vs R.B=10,
// range 240; R differs too but less, range 190, so blue-channel range wins
// the axis choice) orders the tied pair as red-then-blue, while ascending
// RGB tuple order (compare R first: 10 < 200) requires blue-then-red -- so
// on main, this test is expected to fail on the tied pair specifically, not
// merely on the population-monotonicity property test 1 above already
// covers.
//
// A tied pair that differs only at R pins nothing about G or B: a
// comparator with the G and B arms swapped, or dropped entirely, still
// passes TieAtR, since R alone already decides the order. TieAtG and
// TieAtB close that hole: their pairs share R (TieAtB also shares G) and
// differ oppositely on the later channel(s) than on the deciding one, so an
// implementation that compares the wrong channel sorts them backwards.
//
// Those three do NOT pin the ORDER of the R and G arms, which is a distinct
// property and was measured escaping: each of them holds one of the two
// equal (TieAtR ties G, TieAtG ties R, TieAtB ties both), so R and G always
// agree about the result and a comparator consulting G first agrees with
// (R,G,B) on all three. RAndGDisagree is the only shape that separates them.
// In every case green stays a clear, unambiguous third at population 5 so
// the test does not also depend on the non-increasing fix to make sense.
func TestQuantizePNGPopulationTiesBreakByRGB(t *testing.T) {
	green := color.RGBA{100, 200, 100, 255} // pop 5, unambiguous third in every case

	cases := []struct {
		name          string
		first, second color.RGBA // first must sort before second under ascending (R,G,B) tuple order
	}{
		{
			name:   "TieAtR",
			first:  color.RGBA{10, 10, 250, 255}, // R=10
			second: color.RGBA{200, 10, 10, 255}, // R=200
		},
		{
			name:   "TieAtG",
			first:  color.RGBA{50, 10, 250, 255}, // R=50, G=10
			second: color.RGBA{50, 200, 10, 255}, // R=50, G=200; B is backwards (250 > 10) so B-first would flip this
		},
		{
			name:   "TieAtB",
			first:  color.RGBA{50, 50, 10, 255},  // R=50, G=50, B=10
			second: color.RGBA{50, 50, 250, 255}, // R=50, G=50, B=250
		},
		{
			// R says first < second; G says the opposite. Whichever arm the
			// comparator consults first decides, and they decide differently,
			// so this is what pins R BEFORE G rather than merely "R and G are
			// both consulted".
			name:   "RAndGDisagree",
			first:  color.RGBA{10, 200, 100, 255}, // R=10 (lower), G=200 (higher)
			second: color.RGBA{200, 10, 100, 255}, // R=200 (higher), G=10 (lower)
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewRGBA(image.Rect(0, 0, 25, 1))
			i := 0
			for x := 0; x < 25; x++ {
				switch {
				case i < 10:
					img.SetRGBA(x, 0, tc.first)
				case i < 20:
					img.SetRGBA(x, 0, tc.second)
				default:
					img.SetRGBA(x, 0, green)
				}
				i++
			}

			out, err := QuantizePNG(img, 3)
			if err != nil {
				t.Fatalf("QuantizePNG: %v", err)
			}
			pi := decodePalette(t, out)
			if len(pi.Palette) != 3 {
				t.Fatalf("palette has %d entries; want 3 (image has exactly 3 distinct colours)", len(pi.Palette))
			}
			counts := paletteIndexCounts(t, out)
			if counts[0] != 10 || counts[1] != 10 || counts[2] != 5 {
				t.Fatalf("palette index populations = %v; want [10 10 5]", counts)
			}

			rgbAt := func(idx int) (uint8, uint8, uint8) {
				c := color.RGBAModel.Convert(pi.Palette[idx]).(color.RGBA)
				return c.R, c.G, c.B
			}
			if r, g, b := rgbAt(0); r != tc.first.R || g != tc.first.G || b != tc.first.B {
				t.Errorf("palette[0] = (%d,%d,%d); want (%d,%d,%d) -- equal-population tie must break by ascending RGB",
					r, g, b, tc.first.R, tc.first.G, tc.first.B)
			}
			if r, g, b := rgbAt(1); r != tc.second.R || g != tc.second.G || b != tc.second.B {
				t.Errorf("palette[1] = (%d,%d,%d); want (%d,%d,%d) -- equal-population tie must break by ascending RGB",
					r, g, b, tc.second.R, tc.second.G, tc.second.B)
			}
			if r, g, b := rgbAt(2); r != green.R || g != green.G || b != green.B {
				t.Errorf("palette[2] = (%d,%d,%d); want green (%d,%d,%d), the lone third-rank colour",
					r, g, b, green.R, green.G, green.B)
			}
		})
	}
}

// TestQuantizePNGQualityUnaffectedByPaletteOrder pins the exact PSNR
// QuantizePNG(corpus.Scanjpeg(), 64) produces on main, BEFORE byb-20b's
// palette-population reordering lands. Reordering a palette (relabelling
// which index holds which already-chosen colour) must be a pure
// permutation: every pixel keeps the same nearest-palette colour it had
// before, just addressed by a different index, so PSNR -- a function of
// per-pixel colour error only, blind to which index carries which colour --
// cannot move by even a rounding error. Pinning the number (rather than
// re-deriving it after the fix) is what makes this load-bearing: a change
// that also perturbed WHICH colours are chosen, or moved a pixel to a
// different nearest colour, would silently pass a self-referential
// "recompute and compare" check but must fail this one.
//
// Scanjpeg is used rather than Scanpage because it is the realistic B3
// input the bead's cost analysis is about (JPEG chroma-subsampled scan
// output), so this is also the corpus image most likely to expose a
// palette-choice regression, not just an index-relabelling one.
func TestQuantizePNGQualityUnaffectedByPaletteOrder(t *testing.T) {
	out, err := QuantizePNG(corpus.Scanjpeg(), 64)
	if err != nil {
		t.Fatalf("QuantizePNG: %v", err)
	}
	pi := decodePalette(t, out)
	got := psnrRGB(corpus.Scanjpeg(), pi)
	const want = 55.224754 // measured on main (5fbf37d) via QuantizePNG(corpus.Scanjpeg(), 64)
	if math.Abs(got-want) > 5e-7 {
		t.Errorf("QuantizePNG(Scanjpeg, 64) PSNR = %.6f dB; want %.6f dB (pinned from main, byb-20b must not change it)", got, want)
	}
}
