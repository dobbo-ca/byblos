package byblos

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dobbo-ca/byblos/internal/corpus"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// This file is the RED stage of byb-96p's QuantizeIndexed. QuantizeIndexed is
// a stub that always errors (see quantize_indexed.go), so nothing here is
// green yet -- that is the point.
//
// Signature (design spec, byb-96p):
//
//	func QuantizeIndexed(img image.Image, colors int) (EncodedImage, error)
//
// QuantizeIndexed must share QuantizePNG's median-cut/Lloyd/population-order
// core (byb-20b's win) and package the result as a PDF /Indexed,
// /FlateDecode stream -- the raw palette-mapped, PNG-predicted scanline
// bytes with a /DecodeParms Predictor of 15, not a PNG file. See byb-96p for
// the five measured leads this design rests on.

// firstImageID resolves the /Im0 image every corpus document names its
// raster, mirroring internal/pdfdoc's own unexported test helper (which this
// package cannot reach).
func firstImageID(t *testing.T, d pdfdoc.Doc, page int) int {
	t.Helper()
	p, err := d.Page(page)
	if err != nil {
		t.Fatalf("page %d: %v", page, err)
	}
	xo, ok := d.XObject(p.Scope, "Im0")
	if !ok || !xo.Image {
		t.Fatalf("page %d has no /Im0 image", page)
	}
	return xo.ID
}

// openScanCorpus opens the "scan" corpus PDF, the same fixture
// internal/pdfdoc/write_test.go's ReplaceImage tests use.
func openScanCorpus(t *testing.T) pdfdoc.Doc {
	t.Helper()
	raw, ok := corpus.ByName("scan")
	if !ok {
		t.Fatal(`no corpus document named "scan"`)
	}
	d, err := pdfdoc.Open(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("opening corpus scan: %v", err)
	}
	return d
}

// quantizePNGPalette runs QuantizePNG(img, n) and decodes its palette, as the
// reference QuantizeIndexed's Lookup and pixel data must agree with -- both
// entry points are required to share the same median-cut/Lloyd/population-
// order core.
func quantizePNGPalette(t *testing.T, img image.Image, n int) *image.Paletted {
	t.Helper()
	out, err := QuantizePNG(img, n)
	if err != nil {
		t.Fatalf("QuantizePNG(_, %d): %v", n, err)
	}
	return decodePalette(t, out)
}

// --- lead 3/7 fixture: forces Go's PNG encoder to split into >1 IDAT chunk -

// randomIndexedFixture returns a 200x200 image whose 256 distinct RGB
// colours are assigned near-uniformly at random, so the quantized index
// stream is high-entropy and does not compress away. Measured via a
// throwaway probe (scratchpad/probe4_test.go, byb-96p RED stage): running
// QuantizePNG on this exact fixture at 256 colours produces a 41075-byte PNG
// containing 2 IDAT chunks -- comfortably past image/png's 32KB
// (1<<15) IDAT-chunk buffer (image/png/writer.go: bufio.NewWriterSize(e,
// 1<<15) inside writeIDATs). QuantizeIndexed must concatenate every IDAT's
// payload, not just the first, or its output silently truncates on inputs
// like this one.
func randomIndexedFixture() image.Image {
	w, h := 200, 200
	rng := rand.New(rand.NewSource(1))
	pal := make([]color.RGBA, 256)
	for i := range pal {
		pal[i] = color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, pal[rng.Intn(256)])
		}
	}
	return img
}

// --- degenerate cases: colors and opacity contract mirrors QuantizePNG -----

func TestQuantizeIndexedNilImageErrors(t *testing.T) {
	if _, err := QuantizeIndexed(nil, 16); err == nil {
		t.Fatal("QuantizeIndexed(nil, 16): want error, got nil")
	}
}

func TestQuantizeIndexedEmptyBoundsErrors(t *testing.T) {
	empty := image.NewRGBA(image.Rect(0, 0, 0, 0))
	if _, err := QuantizeIndexed(empty, 16); err == nil {
		t.Fatal("QuantizeIndexed on an empty-bounds image: want error, got nil")
	}
}

func TestQuantizeIndexedColorsOutOfRangeErrors(t *testing.T) {
	for _, n := range []int{-1, 0, 1, 257} {
		if _, err := QuantizeIndexed(corpus.Photo(), n); err == nil {
			t.Errorf("QuantizeIndexed(_, %d): want error, got nil", n)
		}
	}
}

func TestQuantizeIndexedColorsAtBoundsSucceeds(t *testing.T) {
	for _, n := range []int{2, 256} {
		if _, err := QuantizeIndexed(corpus.Photo(), n); err != nil {
			t.Errorf("QuantizeIndexed(_, %d): want no error, got %v", n, err)
		}
	}
}

func TestQuantizeIndexedNonOpaqueInputErrors(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.SetNRGBA(x, y, color.NRGBA{100, 100, 100, 128})
		}
	}
	if _, err := QuantizeIndexed(img, 16); err == nil {
		t.Fatal("QuantizeIndexed on a non-opaque image: want error, got nil")
	}
}

// --- test 1: the acceptance test that matters most --------------------

// TestQuantizeIndexedRoundTripPixelsSurvivePdfimages is the acceptance test
// byb-96p names: QuantizeIndexed's output, run through pdfdoc.ReplaceImage
// into a real PDF, must read back through an independent PDF reader
// (poppler's pdfimages) to exactly the pixels QuantizePNG would have
// produced for the same input and colour count.
func TestQuantizeIndexedRoundTripPixelsSurvivePdfimages(t *testing.T) {
	requireTool(t, "pdfimages")

	img := corpus.Photo()
	const n = 64

	ref := quantizePNGPalette(t, img, n)

	enc, err := QuantizeIndexed(img, n)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}

	d := openScanCorpus(t)
	id := firstImageID(t, d, 1)
	if err := d.ReplaceImage(id, enc); err != nil {
		t.Fatalf("ReplaceImage(QuantizeIndexed output): %v", err)
	}
	var out bytes.Buffer
	if err := d.Write(&out); err != nil {
		t.Fatalf("write: %v", err)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, out.Bytes(), 0o600); err != nil {
		t.Fatalf("writing pdf: %v", err)
	}
	cmd := exec.Command("pdfimages", "-png", src, filepath.Join(dir, "img"))
	if cout, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdfimages: %v: %s", err, cout)
	}
	pngs, err := filepath.Glob(filepath.Join(dir, "img-*.png"))
	if err != nil {
		t.Fatalf("globbing pdfimages output: %v", err)
	}
	if len(pngs) != 1 {
		t.Fatalf("pdfimages extracted %d images; want 1", len(pngs))
	}
	f, err := os.Open(pngs[0])
	if err != nil {
		t.Fatalf("opening %s: %v", pngs[0], err)
	}
	defer f.Close()
	got, err := png.Decode(f)
	if err != nil {
		t.Fatalf("decoding pdfimages output: %v", err)
	}

	gb, rb := got.Bounds(), ref.Bounds()
	if gb.Dx() != rb.Dx() || gb.Dy() != rb.Dy() {
		t.Fatalf("pdfimages extracted %v; want a %dx%d image", gb, rb.Dx(), rb.Dy())
	}
	for y := 0; y < rb.Dy(); y++ {
		for x := 0; x < rb.Dx(); x++ {
			wr, wg, wb, wa := ref.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
			gr, gg, gbl, ga := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			if wr != gr || wg != gg || wb != gbl || wa != ga {
				t.Fatalf("pixel (%d,%d) differs after the PDF round trip: got (%d,%d,%d,%d), want (%d,%d,%d,%d) "+
					"(QuantizePNG's answer for the same input and colour count)",
					x, y, gr>>8, gg>>8, gbl>>8, ga>>8, wr>>8, wg>>8, wb>>8, wa>>8)
			}
		}
	}
}

// --- test 1b: every bit depth, not just BPC=8, survives a real reader ------

// TestQuantizeIndexedRoundTripAllBitDepthsSurviveQpdf extends test 1's
// acceptance check to the BPC 1/2/4 paths, which the pdfimages round trip
// above never exercises (it only runs at n=64, BPC 8). pdfimages itself is
// not usable as the oracle here: poppler emits a palette-discarding
// monochrome PNG for any 1-bit image, so a 2-colour case would report a
// spurious pixel mismatch even though the PDF bytes are correct (measured
// during byb-96p review). qpdf's own /FlateDecode+Predictor implementation
// has no such limitation -- `--filtered-stream-data` returns the fully
// decoded, predictor-removed sample bytes, bit-packed exactly as the PDF
// spec lays out a sub-8-bit /Indexed image row (MSB first, each row padded
// to a byte boundary) -- so this test unpacks those samples itself and
// resolves each one through /Indexed's own colour table.
//
// IT RESOLVES COLOURS, NOT INDICES (byb-2s4). Until byb-2s4 this test
// compared the unpacked sample values against QuantizePNG's
// *image.Paletted.Pix and stopped there, which made it blind to the half of
// an /Indexed image that actually determines what a reader paints: reversing
// the PLTE entries handed to ColorSpace.Lookup leaves every index identical
// and every colour wrong, and this test stayed green through it (measured
// on this branch: all four subtests PASS with the palette reversed). That
// mattered more here than anywhere else in the file, because this is the
// ONLY real-reader coverage BPC 1/2/4 has -- the pdfimages test above cannot
// run at those depths, for the reason in the previous paragraph. So the
// comparison now goes index -> enc.ColorSpace.Lookup -> RGB, against the RGB
// QuantizePNG puts at the same pixel. That is what a PDF consumer computes,
// and it fails on a wrong palette, a wrong index, or a wrong HiVal.
//
// Deliberately NOT also asserting the raw index values: a change that
// permuted the palette AND the index stream consistently would keep every
// painted colour correct, which is a relabelling, not a defect -- and the
// specific relabelling byblos cares about (byb-20b's descending-population
// order) is pinned by TestQuantizeIndexedPopulationTiesBreakByRGB and
// TestQuantizePNGPaletteInPopulationOrder, on the property itself rather
// than on a byte-for-byte match with a sibling function.
func TestQuantizeIndexedRoundTripAllBitDepthsSurviveQpdf(t *testing.T) {
	requireTool(t, "qpdf")

	img := corpus.Photo()
	for _, n := range []int{2, 4, 16, 256} {
		t.Run(fmt.Sprintf("colors=%d", n), func(t *testing.T) {
			ref := quantizePNGPalette(t, img, n)

			enc, err := QuantizeIndexed(img, n)
			if err != nil {
				t.Fatalf("QuantizeIndexed: %v", err)
			}
			// Guard before the deref below (byb-2s4): a nil DecodeParms is
			// a plausible regression, and letting it panic takes the whole
			// test binary down -- every other test in the package with it --
			// instead of failing this subtest with something CI output can
			// be read for. Mirrors TestQuantizeIndexedDataIsNotAPNGFile.
			if enc.DecodeParms == nil {
				t.Fatal("DecodeParms is nil; an /Indexed FlateDecode stream needs Predictor/Columns/BitsPerComponent to be decodable at all")
			}

			d := openScanCorpus(t)
			id := firstImageID(t, d, 1)
			if err := d.ReplaceImage(id, enc); err != nil {
				t.Fatalf("ReplaceImage(QuantizeIndexed output): %v", err)
			}
			var out bytes.Buffer
			if err := d.Write(&out); err != nil {
				t.Fatalf("write: %v", err)
			}

			dir := t.TempDir()
			src := filepath.Join(dir, "in.pdf")
			if err := os.WriteFile(src, out.Bytes(), 0o600); err != nil {
				t.Fatalf("writing pdf: %v", err)
			}
			cmd := exec.Command("qpdf", fmt.Sprintf("--show-object=%d", id), "--filtered-stream-data", src)
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("qpdf: %v: %s", err, raw)
			}

			bpc := enc.DecodeParms.BitsPerComponent
			rowBytes := (enc.Width*bpc + 7) / 8
			if want := rowBytes * enc.Height; len(raw) != want {
				t.Fatalf("qpdf decoded %d bytes; want %d (%d rows of %d packed bytes at %d bpc)",
					len(raw), want, enc.Height, rowBytes, bpc)
			}

			rb := ref.Bounds()
			for y := 0; y < rb.Dy(); y++ {
				row := raw[y*rowBytes : (y+1)*rowBytes]
				for x := 0; x < rb.Dx(); x++ {
					var idx byte
					switch bpc {
					case 8:
						idx = row[x]
					default:
						samplesPerByte := 8 / bpc
						b := row[x/samplesPerByte]
						shift := uint(8 - bpc*(x%samplesPerByte+1))
						idx = (b >> shift) & (1<<uint(bpc) - 1)
					}
					if int(idx) > enc.ColorSpace.HiVal {
						t.Fatalf("sample (%d,%d) = %d, past HiVal %d -- a reader has no colour to paint for it",
							x, y, idx, enc.ColorSpace.HiVal)
					}
					gr := enc.ColorSpace.Lookup[int(idx)*3]
					gg := enc.ColorSpace.Lookup[int(idx)*3+1]
					gb := enc.ColorSpace.Lookup[int(idx)*3+2]
					wr16, wg16, wb16, _ := ref.At(rb.Min.X+x, rb.Min.Y+y).RGBA()
					wr, wg, wb := uint8(wr16>>8), uint8(wg16>>8), uint8(wb16>>8)
					if gr != wr || gg != wg || gb != wb {
						t.Fatalf("colour (%d,%d) = (%d,%d,%d) via index %d, want (%d,%d,%d) "+
							"(the RGB QuantizePNG paints at the same pixel for the same input and colour count)",
							x, y, gr, gg, gb, idx, wr, wg, wb)
					}
				}
			}
		})
	}
}

// --- test 2: the ColorSpace is a valid Indexed space --------------------

// TestQuantizeIndexedColorSpaceIsValidIndexed checks the shape of
// ColorSpace, and -- since EncodedImage.validate() and ColorSpace.object()
// are unexported in internal/pdfdoc and this package cannot call them
// directly -- exercises them the only way it can: ReplaceImage calls both
// and refuses anything that fails either.
func TestQuantizeIndexedColorSpaceIsValidIndexed(t *testing.T) {
	img := corpus.Photo()
	const n = 64
	ref := quantizePNGPalette(t, img, n)
	wantHiVal := len(ref.Palette) - 1

	enc, err := QuantizeIndexed(img, n)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}
	if enc.ColorSpace.Name != "Indexed" {
		t.Errorf(`ColorSpace.Name = %q, want "Indexed"`, enc.ColorSpace.Name)
	}
	if enc.ColorSpace.Base != "DeviceRGB" {
		t.Errorf(`ColorSpace.Base = %q, want "DeviceRGB"`, enc.ColorSpace.Base)
	}
	if enc.ColorSpace.HiVal != wantHiVal {
		t.Errorf("ColorSpace.HiVal = %d, want %d (QuantizePNG's palette has %d entries)",
			enc.ColorSpace.HiVal, wantHiVal, len(ref.Palette))
	}
	if want := (wantHiVal + 1) * 3; len(enc.ColorSpace.Lookup) != want {
		t.Errorf("len(Lookup) = %d, want %d ((HiVal+1)*3 for a DeviceRGB base)", len(enc.ColorSpace.Lookup), want)
	}

	d := openScanCorpus(t)
	id := firstImageID(t, d, 1)
	if err := d.ReplaceImage(id, enc); err != nil {
		t.Errorf("ReplaceImage rejected QuantizeIndexed's output (validate()/ColorSpace.object() failed): %v", err)
	}
}

// --- test 3: byb-20b's population-order permutation must survive -----------

// TestQuantizeIndexedLookupMatchesQuantizePNGPalette asserts the Lookup bytes
// are IDENTICAL, entry for entry, to QuantizePNG's PLTE for the same input
// and colour count -- which byb-20b already pins as population-descending
// with an ascending-RGB tie-break. This fails if QuantizeIndexed computes its
// own, differently-ordered palette, or forgets the reorder step entirely.
func TestQuantizeIndexedLookupMatchesQuantizePNGPalette(t *testing.T) {
	images := map[string]image.Image{
		"Scanpage": corpus.Scanpage(),
		"Photo":    corpus.Photo(),
		"Gradient": corpus.Gradient(),
		"Scanjpeg": corpus.Scanjpeg(),
	}
	for name, img := range images {
		for _, n := range []int{8, 16, 32, 64, 128, 256} {
			t.Run(fmt.Sprintf("%s/%d", name, n), func(t *testing.T) {
				ref := quantizePNGPalette(t, img, n)
				enc, err := QuantizeIndexed(img, n)
				if err != nil {
					t.Fatalf("QuantizeIndexed: %v", err)
				}
				if enc.ColorSpace.HiVal+1 != len(ref.Palette) {
					t.Fatalf("Lookup has %d entries; QuantizePNG's palette has %d", enc.ColorSpace.HiVal+1, len(ref.Palette))
				}
				for i, c := range ref.Palette {
					r, g, b, _ := c.RGBA()
					wr, wg, wb := uint8(r>>8), uint8(g>>8), uint8(b>>8)
					gr, gg, gb := enc.ColorSpace.Lookup[i*3], enc.ColorSpace.Lookup[i*3+1], enc.ColorSpace.Lookup[i*3+2]
					if gr != wr || gg != wg || gb != wb {
						t.Fatalf("Lookup[%d] = (%d,%d,%d); want (%d,%d,%d) -- QuantizePNG's palette at the same index",
							i, gr, gg, gb, wr, wg, wb)
					}
				}
			})
		}
	}
}

// TestQuantizeIndexedPopulationTiesBreakByRGB pins byb-20b's tie-break
// directly on the produced /Lookup bytes, independently of QuantizePNG's
// output -- mirroring TestQuantizePNGPopulationTiesBreakByRGB's fixtures (see
// there for why these specific colours and populations, rather than the
// merely-non-increasing check above, are what pin ascending-(R,G,B) as the
// tie-break and not some other rule). This test fails if the tie-break is
// removed, reversed, or the wrong channel order is consulted.
func TestQuantizeIndexedPopulationTiesBreakByRGB(t *testing.T) {
	green := color.RGBA{100, 200, 100, 255} // pop 5, unambiguous third in every case

	cases := []struct {
		name          string
		first, second color.RGBA // first must sort before second under ascending (R,G,B) tuple order
	}{
		{name: "TieAtR", first: color.RGBA{10, 10, 250, 255}, second: color.RGBA{200, 10, 10, 255}},
		{name: "TieAtG", first: color.RGBA{50, 10, 250, 255}, second: color.RGBA{50, 200, 10, 255}},
		{name: "TieAtB", first: color.RGBA{50, 50, 10, 255}, second: color.RGBA{50, 50, 250, 255}},
		{name: "RAndGDisagree", first: color.RGBA{10, 200, 100, 255}, second: color.RGBA{200, 10, 100, 255}},
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

			enc, err := QuantizeIndexed(img, 3)
			if err != nil {
				t.Fatalf("QuantizeIndexed: %v", err)
			}
			if enc.ColorSpace.HiVal != 2 {
				t.Fatalf("HiVal = %d, want 2 (image has exactly 3 distinct colours)", enc.ColorSpace.HiVal)
			}
			if len(enc.ColorSpace.Lookup) != 9 {
				t.Fatalf("Lookup is %d bytes, want 9 (3 entries * 3 bytes)", len(enc.ColorSpace.Lookup))
			}
			at := func(idx int) (uint8, uint8, uint8) {
				return enc.ColorSpace.Lookup[idx*3], enc.ColorSpace.Lookup[idx*3+1], enc.ColorSpace.Lookup[idx*3+2]
			}
			if r, g, b := at(0); r != tc.first.R || g != tc.first.G || b != tc.first.B {
				t.Errorf("Lookup[0] = (%d,%d,%d); want (%d,%d,%d) -- equal-population tie must break by ascending RGB",
					r, g, b, tc.first.R, tc.first.G, tc.first.B)
			}
			if r, g, b := at(1); r != tc.second.R || g != tc.second.G || b != tc.second.B {
				t.Errorf("Lookup[1] = (%d,%d,%d); want (%d,%d,%d) -- equal-population tie must break by ascending RGB",
					r, g, b, tc.second.R, tc.second.G, tc.second.B)
			}
			if r, g, b := at(2); r != green.R || g != green.G || b != green.B {
				t.Errorf("Lookup[2] = (%d,%d,%d); want green (%d,%d,%d), the lone third-rank colour",
					r, g, b, green.R, green.G, green.B)
			}
		})
	}
}

// --- test 4: Data is a raw predictor-prefixed FlateDecode payload, not PNG -

// TestQuantizeIndexedDataIsNotAPNGFile checks the container shape byb-96p
// exists to fix: Data must be the bare zlib/deflate stream a PDF
// /FlateDecode filter expects, not a complete PNG file with its own chunk
// framing (which no PDF filter understands -- see write.go's EncodedImage.Data
// doc comment).
func TestQuantizeIndexedDataIsNotAPNGFile(t *testing.T) {
	enc, err := QuantizeIndexed(corpus.Photo(), 64)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}
	pngSig := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	if len(enc.Data) >= len(pngSig) && bytes.Equal(enc.Data[:len(pngSig)], pngSig) {
		t.Fatal("Data starts with the PNG signature; want a raw FlateDecode payload (no PNG chunk framing)")
	}
	if enc.Filter != "FlateDecode" {
		t.Errorf(`Filter = %q, want "FlateDecode"`, enc.Filter)
	}
	if enc.DecodeParms == nil {
		t.Fatal("DecodeParms is nil")
	}

	r, err := zlib.NewReader(bytes.NewReader(enc.Data))
	if err != nil {
		t.Fatalf("Data does not begin a valid zlib stream: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflating Data: %v", err)
	}

	rowBytes := (enc.DecodeParms.Columns*enc.DecodeParms.BitsPerComponent + 7) / 8
	want := (rowBytes + 1) * enc.Height // +1: every row carries a PNG filter-type byte
	if len(raw) != want {
		t.Fatalf("inflated Data is %d bytes; want %d (%d rows of a 1-byte filter tag + %d sample bytes)",
			len(raw), want, enc.Height, rowBytes)
	}
	for row := 0; row*(rowBytes+1) < len(raw); row++ {
		ft := raw[row*(rowBytes+1)]
		if ft > 4 {
			t.Fatalf("row %d's filter-type byte is %d; want a valid PNG predictor filter type 0..4", row, ft)
		}
	}
}

// --- test 4b: the FlateDecode payload must actually be compressed ----------

// TestQuantizeIndexedDataIsCompressed guards the win byb-20b/byb-0b8 exist to
// deliver on the path the pipeline actually uses: the deflate stage inside
// QuantizeIndexed's PNG-chunk extraction must run at a real compression
// level, not a no-op one. Bug found during byb-96p mutation review: nothing
// previously measured Data's SIZE, only its shape and pixel content, so
// swapping quantize_indexed.go's png.Encoder to png.NoCompression passed
// every other test in this file while inflating corpus.Photo() at 64 colours
// from 44,395 bytes to 480,851 (measured) -- essentially the raw, uncompressed
// raster. rawSize below is derived independently of the encoder (predictor
// byte + packed samples per row, straight from the PDF /DecodeParms this
// function itself returns), so this is not circular with quantizeCore or with
// the PNG encoder's own accounting.
func TestQuantizeIndexedDataIsCompressed(t *testing.T) {
	img := corpus.Photo()
	const n = 64

	enc, err := QuantizeIndexed(img, n)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}
	// Guard before the deref below (byb-2s4): the sibling qpdf test carried
	// the same unguarded deref and turned a nil DecodeParms into a panic
	// that took the whole test binary down. This was the file's other one.
	if enc.DecodeParms == nil {
		t.Fatal("DecodeParms is nil; there is no declared row geometry to size the uncompressed raster from")
	}

	rowBytes := (enc.DecodeParms.Columns*enc.DecodeParms.BitsPerComponent + 7) / 8
	rawSize := (rowBytes + 1) * enc.Height // +1: PNG filter-type byte per row
	if len(enc.Data) > rawSize/2 {
		t.Fatalf("len(Data) = %d, want well under half of the %d-byte uncompressed raster "+
			"(deflate does not appear to be compressing the predicted scanlines)", len(enc.Data), rawSize)
	}
}

// --- test 5: DecodeParms, pinned to measured values -------------------------

// TestQuantizeIndexedDecodeParms pins /DecodeParms to what a probe (see
// scratchpad/probe.go, byb-96p RED stage) actually measured Go's image/png
// encoder choosing for these palette sizes: bit depth 1/2/4/8 for palette
// lengths (2]/(2,4]/(4,16]/(16,256], not always 8. corpus.Photo() has far
// more than 256 distinct colours, so median cut saturates to exactly
// `colors` entries at every N below, which is what lets this test assert an
// exact depth per N rather than "whatever the palette happened to need".
func TestQuantizeIndexedDecodeParms(t *testing.T) {
	img := corpus.Photo()
	cases := []struct {
		colors  int
		wantBPC int
	}{
		{2, 1},
		{4, 2},
		{16, 4},
		{17, 8},
		{256, 8},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.colors), func(t *testing.T) {
			enc, err := QuantizeIndexed(img, tc.colors)
			if err != nil {
				t.Fatalf("QuantizeIndexed(_, %d): %v", tc.colors, err)
			}
			if enc.DecodeParms == nil {
				t.Fatal("DecodeParms is nil")
			}
			p := *enc.DecodeParms
			if p.Predictor != 15 {
				t.Errorf("Predictor = %d, want 15 (PNG \"optimum\", ISO 32000-1 table 8)", p.Predictor)
			}
			if p.Colors != 1 {
				t.Errorf("Colors = %d, want 1 (an Indexed image is always one component)", p.Colors)
			}
			if p.BitsPerComponent != tc.wantBPC {
				t.Errorf("BitsPerComponent = %d, want %d", p.BitsPerComponent, tc.wantBPC)
			}
			if p.Columns != enc.Width {
				t.Errorf("Columns = %d, want %d (the image width)", p.Columns, enc.Width)
			}
			if enc.BPC != tc.wantBPC {
				t.Errorf("EncodedImage.BPC = %d, want %d (must agree with DecodeParms.BitsPerComponent)", enc.BPC, tc.wantBPC)
			}
		})
	}
}

// --- test 6: colors/opacity contract already covered above ------------------
//
// See TestQuantizeIndexedColorsOutOfRangeErrors, TestQuantizeIndexedColorsAtBoundsSucceeds,
// and TestQuantizeIndexedNonOpaqueInputErrors near the top of this file.

// --- test 7: multiple IDAT chunks must not be truncated ---------------------

// TestQuantizeIndexedMultipleIDATNotTruncated uses randomIndexedFixture (see
// its doc comment) to force Go's internal PNG encoding step past one IDAT
// chunk, then checks the inflated Data is the FULL expected length. A
// truncated concatenation -- keeping only the first IDAT's payload -- yields
// either a zlib error (an incomplete deflate stream almost never happens to
// end on a valid block boundary) or a short inflated result; either fails
// this test.
func TestQuantizeIndexedMultipleIDATNotTruncated(t *testing.T) {
	img := randomIndexedFixture()

	enc, err := QuantizeIndexed(img, 256)
	if err != nil {
		t.Fatalf("QuantizeIndexed: %v", err)
	}
	if enc.DecodeParms == nil {
		t.Fatal("DecodeParms is nil")
	}

	r, err := zlib.NewReader(bytes.NewReader(enc.Data))
	if err != nil {
		t.Fatalf("Data does not begin a valid zlib stream (a truncated multi-IDAT concatenation is expected to fail here): %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflating Data (a truncated multi-IDAT concatenation is expected to fail here): %v", err)
	}

	rowBytes := (enc.DecodeParms.Columns*enc.DecodeParms.BitsPerComponent + 7) / 8
	want := (rowBytes + 1) * enc.Height
	if len(raw) != want {
		t.Fatalf("inflated Data is %d bytes; want %d -- looks truncated to fewer than the source's IDAT chunks",
			len(raw), want)
	}
}

// --- test 8: the painted raster, decoded without any oracle ----------------

// paethPredictor lives in build_test.go, which is this same package. byb-5jy
// (#34) and byb-2s4 (#40) each added an equivalent copy in a different file, so
// the two branches never conflicted textually and the merge broke the build.

// decodeIndexedRaster paints enc the way a PDF consumer does and returns the
// resulting RGB image: inflate Data, undo the /Predictor 15 (PNG) row filters,
// unpack the bit-packed samples MSB-first, and resolve each sample through
// /Indexed's colour table.
//
// It deliberately borrows nothing from image/png. QuantizeIndexed BUILDS its
// stream with image/png, so a check that read it back with image/png would be
// asking the encoder to mark its own work; the predictor arithmetic, the
// sub-byte packing order and the palette lookup are exactly the three things a
// reader has to get right from /DecodeParms and /ColorSpace alone, and all
// three are reimplemented here from the spec.
//
// The one filter-type subtlety: an /Indexed image is a single component of
// 1/2/4/8 bits, so PNG's filter offset (bpp, "bytes per complete pixel,
// rounding up to one") is 1 at every depth this function sees.
func decodeIndexedRaster(t *testing.T, enc EncodedImage) image.Image {
	t.Helper()
	if enc.DecodeParms == nil {
		t.Fatal("DecodeParms is nil; an /Indexed FlateDecode stream is not decodable without Predictor/Columns/BitsPerComponent")
	}
	if enc.DecodeParms.Predictor != 15 {
		t.Fatalf("Predictor = %d; this decoder implements the PNG predictors (15) only", enc.DecodeParms.Predictor)
	}
	bpc := enc.DecodeParms.BitsPerComponent
	switch bpc {
	case 1, 2, 4, 8:
	default:
		t.Fatalf("BitsPerComponent = %d; want 1, 2, 4 or 8", bpc)
	}

	r, err := zlib.NewReader(bytes.NewReader(enc.Data))
	if err != nil {
		t.Fatalf("Data does not begin a valid zlib stream: %v", err)
	}
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("inflating Data: %v", err)
	}

	rowBytes := (enc.Width*bpc + 7) / 8
	if want := (rowBytes + 1) * enc.Height; len(raw) != want {
		t.Fatalf("inflated Data is %d bytes; want %d (%d rows of a 1-byte filter tag + %d packed sample bytes)",
			len(raw), want, enc.Height, rowBytes)
	}

	const bpp = 1 // one component of <=8 bits, rounded up to one byte
	out := image.NewRGBA(image.Rect(0, 0, enc.Width, enc.Height))
	prev := make([]byte, rowBytes)
	cur := make([]byte, rowBytes)
	mask := byte(1<<uint(bpc) - 1)
	samplesPerByte := 8 / bpc
	for y := 0; y < enc.Height; y++ {
		off := y * (rowBytes + 1)
		ft := raw[off]
		copy(cur, raw[off+1:off+1+rowBytes])
		for i := 0; i < rowBytes; i++ {
			var a, c byte
			if i >= bpp {
				a, c = cur[i-bpp], prev[i-bpp]
			}
			b := prev[i]
			switch ft {
			case 0: // None
			case 1: // Sub
				cur[i] += a
			case 2: // Up
				cur[i] += b
			case 3: // Average
				cur[i] += byte((int(a) + int(b)) / 2)
			case 4: // Paeth
				cur[i] += paethPredictor(a, b, c)
			default:
				t.Fatalf("row %d has filter type %d; want 0..4", y, ft)
			}
		}
		for x := 0; x < enc.Width; x++ {
			idx := (cur[x/samplesPerByte] >> uint(8-bpc*(x%samplesPerByte+1))) & mask
			if int(idx) > enc.ColorSpace.HiVal {
				t.Fatalf("sample (%d,%d) = %d, past HiVal %d -- a reader has no colour to paint for it",
					x, y, idx, enc.ColorSpace.HiVal)
			}
			if lo := (int(idx) + 1) * 3; lo > len(enc.ColorSpace.Lookup) {
				t.Fatalf("sample (%d,%d) = %d needs Lookup[%d:%d], but Lookup is only %d bytes",
					x, y, idx, lo-3, lo, len(enc.ColorSpace.Lookup))
			}
			out.SetRGBA(x, y, color.RGBA{
				enc.ColorSpace.Lookup[int(idx)*3],
				enc.ColorSpace.Lookup[int(idx)*3+1],
				enc.ColorSpace.Lookup[int(idx)*3+2],
				255,
			})
		}
		prev, cur = cur, prev
	}
	return out
}

// TestQuantizeIndexedPSNRLadderPinned is the /Indexed path's half of byb-tq2's
// quality pin, and the only check anywhere that resolves QuantizeIndexed's own
// sample stream to painted colours without an oracle. The sibling oracle-free
// tests either read Lookup on its own (TestQuantizeIndexedLookupMatchesQuantizePNGPalette,
// TestQuantizeIndexedPopulationTiesBreakByRGB) or inflate Data and check only
// its length and filter tags (TestQuantizeIndexedDataIsNotAPNGFile); none of
// them puts the two halves back together.
//
// Two holes close here at once, and neither is hypothetical:
//
//   - byb-tq2 measured that no QuantizeIndexed test caught the halved Lloyd
//     budget. Today QuantizeIndexed and QuantizePNG share quantizeCore, so the
//     ladder pin next door covers both by construction -- but "by
//     construction" is exactly the assumption that stops holding the day the
//     indexed path grows its own palette handling, and nothing would have said
//     so. This runs the same pinned numbers through QuantizeIndexed's OWN
//     output bytes.
//   - byb-2s4 measured that at BPC 1/2/4 no reader verifies the colours in the
//     oracle-free build. The qpdf round trip above is the only real-reader
//     coverage at those depths and it is skipped without qpdf; what remained
//     was TestQuantizeIndexedLookupMatchesQuantizePNGPalette, which compares
//     the two entry points against each other and therefore stays green for
//     any mutation inside the core they share.
//
// The comparison is against the SOURCE image, not against QuantizePNG, so it
// is not a sibling-agreement check: it is the same absolute ladder
// TestQuantizePNGPSNRLadderPinned asserts, reached through inflate ->
// unpredict -> unpack -> palette lookup (decodeIndexedRaster, which
// reimplements all four from the spec). A wrong palette, a wrong bit depth, a
// wrong predictor or a worse quantizer each move the number.
//
// The ladder's colour counts of 2 and 4 are what put BPC 1 and BPC 2 under
// this check; 8 and 16 give BPC 4, and everything above gives BPC 8.
func TestQuantizeIndexedPSNRLadderPinned(t *testing.T) {
	for _, pt := range quantizeLadder {
		t.Run(fmt.Sprintf("%s/%d", pt.image, pt.colors), func(t *testing.T) {
			img := ladderImage(t, pt.image)
			enc, err := QuantizeIndexed(img, pt.colors)
			if err != nil {
				t.Fatalf("QuantizeIndexed(%s, %d): %v", pt.image, pt.colors, err)
			}
			got := psnrRGB(img, decodeIndexedRaster(t, enc))
			if math.Abs(got-pt.psnr) > 5e-7 {
				t.Errorf("QuantizeIndexed(%s, %d) painted PSNR = %.9f dB; want %.9f dB (delta %+.9f) -- "+
					"the /Indexed raster a reader paints is not the one this ladder was measured from",
					pt.image, pt.colors, got, pt.psnr, got-pt.psnr)
			}
		})
	}
}
