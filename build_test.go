package byblos

// Tests for BuildPDF (byb-c3o): constructing a PDF from images, the missing
// half of design spec G1 (img2pdf). See docs/superpowers/specs/2026-07-27-byblos-design.md
// for the accepted API shape, placement math and the reasoning behind each
// test below.
//
// RED STAGE: BuildPDF, BuildPage and the EncodedImage/ColorSpace/DecodeParms
// aliases do not exist yet. This file is expected to fail to compile until
// build.go is written.

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/jbig2"
	"github.com/dobbo-ca/byblos/internal/pdfdoc"
)

// --- fixture helpers ---------------------------------------------------

// grayPattern returns deterministic, non-uniform 8-bit grey samples, so a
// pixel round trip test cannot be satisfied by a constant-fill shortcut. The
// shape mirrors internal/corpus.grayPixels, reimplemented here because that
// one is unexported and corpus exists to build test PDFs, not source pixels
// for one.
func grayPattern(w, h, seed int) []byte {
	px := make([]byte, w*h)
	for i := range px {
		px[i] = byte((i*7 + seed*31) % 251)
	}
	return px
}

func flateEncode(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(payload); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

// flateGrayImage is a lossless DeviceGray/FlateDecode raster: the shape T1,
// T5, T7, T8 and T9's comparison images use.
func flateGrayImage(t *testing.T, w, h, seed int) EncodedImage {
	return EncodedImage{
		Width:      w,
		Height:     h,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, grayPattern(w, h, seed)),
	}
}

// quadrantImage is w x h with the top-left quadrant black and everything else
// white, in image pixel space (origin top-left, y increasing downward — the
// convention bitmap.go documents for byblos.Bitmap and the one image.Image
// uses). It exists for T3: the one test that can catch an unnecessary y-flip.
func quadrantImage(t *testing.T, w, h int) EncodedImage {
	px := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(255)
			if x < w/2 && y < h/2 {
				v = 0
			}
			px[y*w+x] = v
		}
	}
	return EncodedImage{
		Width:      w,
		Height:     h,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "FlateDecode",
		Data:       flateEncode(t, px),
	}
}

// jpegImage returns a DCTDecode EncodedImage plus the canonical decoded image
// that same JPEG data means: comparisons should be made against this decode,
// never against the pre-encode samples, because JPEG is lossy and asserting
// bit-exactness against the source pixels would be asserting something the
// codec does not promise.
func jpegImage(t *testing.T, w, h int) (EncodedImage, image.Image) {
	t.Helper()
	src := image.NewGray(image.Rect(0, 0, w, h))
	px := grayPattern(w, h, 11)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			src.SetGray(x, y, color.Gray{Y: px[y*w+x]})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("jpeg.Decode (canonical): %v", err)
	}
	return EncodedImage{
		Width:      w,
		Height:     h,
		BPC:        8,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "DCTDecode",
		Data:       buf.Bytes(),
	}, decoded
}

// jbig2Image returns a JBIG2Decode EncodedImage whose payload is real
// internal/jbig2 output, not hand-typed bytes: T9 checks the builder carries
// exactly these bytes, unmodified.
func jbig2Image(t *testing.T, w, h int) (EncodedImage, []byte) {
	t.Helper()
	bm := jbig2.NewBitmap(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/4+y/4)%2 == 0 {
				bm.Set(x, y, 1)
			}
		}
	}
	payload := jbig2.EncodeGenericRegion(bm, true)
	return EncodedImage{
		Width:      w,
		Height:     h,
		BPC:        1,
		ColorSpace: ColorSpace{Name: "DeviceGray"},
		Filter:     "JBIG2Decode",
		Data:       payload,
	}, payload
}

// imageDictString returns the image XObject dictionary text (including the
// enclosing << >>) for the object whose /Filter is filter, by locating the
// marker directly rather than parsing the PDF. Used to assert on dictionary
// entries that Inspect/ExtractPageRaster do not surface, such as
// /ColorSpace or /DecodeParms on a codec byblos cannot decode (JBIG2).
func imageDictString(t *testing.T, pdf []byte, filter string) string {
	t.Helper()
	marker := []byte("/Filter /" + filter)
	idx := bytes.Index(pdf, marker)
	if idx < 0 {
		t.Fatalf("no object with /Filter /%s found in output", filter)
	}
	start := bytes.LastIndex(pdf[:idx], []byte("<<"))
	end := bytes.Index(pdf[idx:], []byte(">>"))
	if start < 0 || end < 0 {
		t.Fatalf("could not locate dict bounds around /Filter /%s", filter)
	}
	return string(pdf[start : idx+end+2])
}

func buildOrFatal(t *testing.T, pages []BuildPage) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := BuildPDF(&buf, pages); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	return buf.Bytes()
}

// --- T1: round trip through Inspect/ExtractPageRaster -----------------

// A do-nothing BuildPDF (Open fails), a wrong MediaBox, a wrong `cm`, wrong
// /Width or /Height, or a broken xref all fail this: either Inspect or
// ExtractPageRaster errors, or the reported geometry or pixels are wrong.
func TestBuildPDFRoundTripsThroughExtract(t *testing.T) {
	const w, h, dpi = 40, 60, 300
	img := flateGrayImage(t, w, h, 1)
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: dpi}})

	pages, err := Inspect(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("Inspect returned %d pages; want 1", len(pages))
	}
	wantBounds := image.Rect(0, 0, 10, 14) // 40*72/300, 60*72/300, rounded
	if pages[0].Bounds != wantBounds {
		t.Errorf("page bounds = %v; want %v", pages[0].Bounds, wantBounds)
	}

	pr, err := ExtractPageRaster(bytes.NewReader(out), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	if pr.Bounds != pr.Page {
		t.Errorf("raster bounds %v != page bounds %v", pr.Bounds, pr.Page)
	}
	if !pr.CoversPage() {
		t.Error("CoversPage() = false for a raster placed to fill the derived page box")
	}
	want := grayPattern(w, h, 1)
	b := pr.Image.Bounds()
	if b.Dx() != w || b.Dy() != h {
		t.Fatalf("raster is %dx%d; want %dx%d", b.Dx(), b.Dy(), w, h)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, _, _ := pr.Image.At(x, y).RGBA()
			if got := byte(r >> 8); got != want[y*w+x] {
				t.Fatalf("pixel (%d,%d) = %d; want %d", x, y, got, want[y*w+x])
			}
		}
	}
}

// --- T2: fit-centered placement on an explicit page box ----------------

// Catches stretch-to-fill (bounds would equal the whole page and CoversPage
// would be true), fill-and-crop, forgetting to centre (tx == 0), or a sign
// error in tx/ty.
func TestBuildPDFPlacementIsFitCentered(t *testing.T) {
	const w, h = 40, 60
	const pageW, pageH = 612, 792
	img := flateGrayImage(t, w, h, 1)
	out := buildOrFatal(t, []BuildPage{{Image: img, WidthPt: pageW, HeightPt: pageH}})

	pr, err := ExtractPageRaster(bytes.NewReader(out), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	wantBounds := image.Rect(42, 0, 570, 792)
	wantPage := image.Rect(0, 0, 612, 792)
	if pr.Bounds != wantBounds {
		t.Errorf("bounds = %v; want %v", pr.Bounds, wantBounds)
	}
	if pr.Page != wantPage {
		t.Errorf("page = %v; want %v", pr.Page, wantPage)
	}
	if pr.CoversPage() {
		t.Error("CoversPage() = true; a letterboxed placement must not cover the page")
	}
}

// --- T3: orientation is top-down, not flipped ---------------------------

// The one test in this file no in-process round trip can substitute for:
// ExtractPageRaster returns the stored raster, not a render, so an
// unnecessary y-flip in the placement matrix is invisible to T1 and T2. Only
// an independent renderer catches it.
func TestBuildPDFOrientationIsTopDown(t *testing.T) {
	pdftoppm, err := exec.LookPath("pdftoppm")
	if err != nil {
		t.Skip("pdftoppm not installed (poppler)")
	}
	const w, h, dpi = 100, 200, 100
	img := quadrantImage(t, w, h)
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: dpi}})

	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, out, 0o600); err != nil {
		t.Fatalf("writing pdf: %v", err)
	}
	cmd := exec.Command(pdftoppm, "-r", "100", "-png", "-singlefile", src, filepath.Join(dir, "page"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdftoppm: %v: %s", err, out)
	}
	f, err := os.Open(filepath.Join(dir, "page.png"))
	if err != nil {
		t.Fatalf("opening rendered page: %v", err)
	}
	defer f.Close()
	rendered, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decoding rendered page: %v", err)
	}
	b := rendered.Bounds()
	gray := func(x, y int) byte {
		r, _, _, _ := rendered.At(x, y).RGBA()
		return byte(r >> 8)
	}
	tl := gray(b.Min.X, b.Min.Y)
	tr := gray(b.Max.X-1, b.Min.Y)
	bl := gray(b.Min.X, b.Max.Y-1)
	br := gray(b.Max.X-1, b.Max.Y-1)
	if tl > 128 {
		t.Errorf("top-left corner rendered %d; want black (the built image's black quadrant)", tl)
	}
	if tr < 128 || bl < 128 || br < 128 {
		t.Errorf("a corner outside the black quadrant rendered dark: TR=%d BL=%d BR=%d", tr, bl, br)
	}
}

// --- T4: MediaBox DPI derivation matches pdfimages --------------------

// Catches a MediaBox derived with 25.4 instead of 72 (the inches-vs-points
// trap), or DPI being ignored outright.
func TestBuildPDFDPIMatchesPdfimages(t *testing.T) {
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages not installed (poppler)")
	}
	for _, dpi := range []float64{300, 150} {
		t.Run(strconv.Itoa(int(dpi)), func(t *testing.T) {
			img := flateGrayImage(t, 40, 60, 1)
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: dpi}})
			x, y := pdfimagesPPI(t, out)
			if x != int(dpi) || y != int(dpi) {
				t.Errorf("pdfimages -list reports x-ppi=%d y-ppi=%d; want %d %d", x, y, int(dpi), int(dpi))
			}
		})
	}
}

// pdfimagesPPI runs `pdfimages -list` over pdf and returns the x-ppi/y-ppi
// columns of its one image row.
func pdfimagesPPI(t *testing.T, pdf []byte) (int, int) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, pdf, 0o600); err != nil {
		t.Fatalf("writing pdf: %v", err)
	}
	out, err := exec.Command("pdfimages", "-list", src).CombinedOutput()
	if err != nil {
		t.Fatalf("pdfimages -list: %v: %s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 3 {
		t.Fatalf("pdfimages -list produced no image row:\n%s", out)
	}
	fields := strings.Fields(lines[2])
	if len(fields) < 14 {
		t.Fatalf("pdfimages -list row has %d fields, want at least 14:\n%s", len(fields), lines[2])
	}
	x, err := strconv.Atoi(fields[12])
	if err != nil {
		t.Fatalf("parsing x-ppi %q: %v", fields[12], err)
	}
	y, err := strconv.Atoi(fields[13])
	if err != nil {
		t.Fatalf("parsing y-ppi %q: %v", fields[13], err)
	}
	return x, y
}

// --- T5: multi-page wiring ----------------------------------------------

// Catches a /Pages tree whose /Kids and /Count are right but whose
// /Contents or /XObject on every page point at the same object: correct
// structure, wrong content, invisible to a single-page test.
func TestBuildPDFMultiPage(t *testing.T) {
	fills := []byte{0, 97, 194}
	var pages []BuildPage
	for _, f := range fills {
		px := bytes.Repeat([]byte{f}, 4*4)
		pages = append(pages, BuildPage{
			Image: EncodedImage{
				Width: 4, Height: 4, BPC: 8,
				ColorSpace: ColorSpace{Name: "DeviceGray"},
				Filter:     "FlateDecode",
				Data:       flateEncode(t, px),
			},
			DPI: 300,
		})
	}
	out := buildOrFatal(t, pages)

	got, err := Inspect(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(got) != len(fills) {
		t.Fatalf("Inspect returned %d pages; want %d", len(got), len(fills))
	}
	for i, want := range fills {
		pr, err := ExtractPageRaster(bytes.NewReader(out), i+1)
		if err != nil {
			t.Fatalf("page %d: ExtractPageRaster: %v", i+1, err)
		}
		r, _, _, _ := pr.Image.At(0, 0).RGBA()
		if got := byte(r >> 8); got != want {
			t.Errorf("page %d: pixel(0,0) = %d; want %d", i+1, got, want)
		}
	}
}

// --- T6: unsupported input is rejected, not silently written -----------

// Any of these slipping through would produce a file whose /Filter names a
// codec byblos cannot cross-check, or a MediaBox no reader can use.
func TestBuildPDFRejectsUnsupportedInput(t *testing.T) {
	validGray := flateGrayImage(t, 8, 8, 1)

	cases := []struct {
		name  string
		pages []BuildPage
	}{
		{"ccitt", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 1,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "CCITTFaxDecode",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"jpx", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceRGB"},
			Filter:     "JPXDecode",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"lzw", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "LZWDecode",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"no-filter", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"jbig2-wrong-bpc", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "JBIG2Decode",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"dct-cmyk", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceCMYK"},
			Filter:     "DCTDecode",
			Data:       []byte{0x00},
		}, DPI: 300}}},
		{"no-pages", nil},
		{"nan-box", []BuildPage{{Image: validGray, WidthPt: math.NaN(), HeightPt: 100}}},
		{"inf-box", []BuildPage{{Image: validGray, WidthPt: math.Inf(1), HeightPt: 100}}},
		{"negative-box", []BuildPage{{Image: validGray, WidthPt: -1, HeightPt: 100}}},
		{"no-dpi-no-box", []BuildPage{{Image: validGray}}},
		// tiny-box and huge-box: finite, positive page boxes that formatNum's
		// six-decimal-place rounding cannot represent as anything but 0 or
		// +Inf. See internal/pdfbuild's minCoord/maxCoord.
		{"tiny-box", []BuildPage{{Image: validGray, WidthPt: 1e-7, HeightPt: 1e-7}}},
		{"huge-box", []BuildPage{{Image: validGray, WidthPt: 1e303, HeightPt: 1e303}}},
		// degenerate-aspect: an in-range page box and an in-range image size
		// whose fit-centered scale still rounds one placed dimension to 0 —
		// caught only by pdfbuild's post-scale check, not by validating the
		// box or the image dimensions in isolation.
		{"degenerate-aspect", []BuildPage{{Image: EncodedImage{
			Width: 100000, Height: 1, BPC: 8,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "FlateDecode",
			Data:       flateEncode(t, make([]byte, 100000)),
		}, WidthPt: 1, HeightPt: 1000}}},
		// flate-bpc16: byblos has no producer for 16-bit-per-component data,
		// and internal/pdfdoc's reader panics reading it back, so pdfbuild
		// must reject it rather than write a file its own reader crashes on.
		{"flate-bpc16", []BuildPage{{Image: EncodedImage{
			Width: 8, Height: 8, BPC: 16,
			ColorSpace: ColorSpace{Name: "DeviceGray"},
			Filter:     "FlateDecode",
			Data:       flateEncode(t, make([]byte, 128)),
		}, DPI: 300}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := BuildPDF(&buf, tc.pages); err == nil {
				t.Error("BuildPDF returned nil error; want a rejection")
			}
		})
	}
}

// --- T7: determinism -----------------------------------------------------

// Catches map-ordered dictionary emission or a wall-clock value (a
// /CreationDate, an /ID) sneaking into the output.
func TestBuildPDFIsDeterministic(t *testing.T) {
	img := flateGrayImage(t, 20, 20, 3)
	pages := []BuildPage{{Image: img, DPI: 300}}
	a := buildOrFatal(t, pages)
	b := buildOrFatal(t, pages)
	if !bytes.Equal(a, b) {
		t.Error("two builds from identical input produced different bytes")
	}
}

// --- T8: structural validation ------------------------------------------

// Weak on purpose: pdfcpu checks structure (xref offsets, /Length, /Count),
// not pixel content. It is included because a structurally broken file is a
// distinct failure mode from a geometrically or photometrically wrong one,
// not because it substitutes for T1-T5 or T9-T10.
func TestBuiltPDFValidates(t *testing.T) {
	flate := flateGrayImage(t, 20, 20, 1)
	dct, _ := jpegImage(t, 20, 20)
	jbig2Img, _ := jbig2Image(t, 24, 24)

	for name, img := range map[string]EncodedImage{"flate": flate, "dct": dct, "jbig2": jbig2Img} {
		t.Run(name, func(t *testing.T) {
			out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 300}})
			if err := pdfdoc.Validate(bytes.NewReader(out)); err != nil {
				t.Errorf("pdfdoc.Validate: %v", err)
			}
		})
	}
}

// --- T9: JBIG2 is carried verbatim ---------------------------------------

// Catches a builder that re-compresses, re-frames, or otherwise touches an
// opaque JBIG2 payload, or gets /BitsPerComponent wrong for it.
func TestBuildPDFCarriesJBIG2Verbatim(t *testing.T) {
	const w, h = 24, 24
	img, payload := jbig2Image(t, w, h)
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 300}})

	if !bytes.Contains(out, payload) {
		t.Error("the built PDF does not contain the JBIG2 payload byte-for-byte")
	}

	// Inspect/ExtractPageRaster never decode a JBIG2 stream (byblos has no
	// JBIG2 reader), so neither one would notice a wrong /ColorSpace on this
	// dict. Check the raw dictionary text directly: ISO 32000-1 7.4.7 says
	// JBIG2Decode carries 1-bit DeviceGray, and nothing else could have come
	// from a real JBIG2 region.
	dict := imageDictString(t, out, "JBIG2Decode")
	if !strings.Contains(dict, "/ColorSpace /DeviceGray") {
		t.Errorf("JBIG2 image dict = %s; want /ColorSpace /DeviceGray", dict)
	}

	pages, err := Inspect(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(pages) != 1 || len(pages[0].Images) != 1 {
		t.Fatalf("Inspect() = %+v; want one page with one image", pages)
	}
	ref := pages[0].Images[0]
	if !ref.Bitonal {
		t.Error("Bitonal = false; want true for a 1-bpc JBIG2 image")
	}
	if ref.Width != w || ref.Height != h {
		t.Errorf("dims = %dx%d; want %dx%d", ref.Width, ref.Height, w, h)
	}

	if _, err := ExtractPageRaster(bytes.NewReader(out), 1); !errIsUnsupportedCodec(err) {
		t.Errorf("ExtractPageRaster error = %v; want ErrUnsupportedImageCodec", err)
	}
}

func errIsUnsupportedCodec(err error) bool {
	return err != nil && strings.Contains(err.Error(), ErrUnsupportedImageCodec.Error())
}

// --- T9b: /DecodeParms is Flate-only -------------------------------------

// /DecodeParms's keys (ISO 32000-1 Table 12) are the PNG predictor
// parameters, meaningful only for FlateDecode. A caller that sets
// EncodedImage.DecodeParms on a JBIG2 or DCT image must not see it emitted:
// JBIG2Decode's only legal parameter is /JBIG2Globals and DCTDecode takes
// none of these keys.
func TestBuildPDFOmitsDecodeParmsForNonFlate(t *testing.T) {
	img, _ := jbig2Image(t, 8, 8)
	img.DecodeParms = &DecodeParms{Predictor: 15, Colors: 1, BitsPerComponent: 8, Columns: 8}
	out := buildOrFatal(t, []BuildPage{{Image: img, DPI: 300}})

	dict := imageDictString(t, out, "JBIG2Decode")
	if strings.Contains(dict, "/DecodeParms") {
		t.Errorf("JBIG2 image dict = %s; must not carry Flate's /DecodeParms", dict)
	}
}

// --- T10: pixels match an independent decoder ---------------------------

// Both ExtractPageRaster and this test's expectation are read off the same
// dictionary the builder wrote, for a Flate image, so that half is
// self-consistent by construction. What makes this non-vacuous is
// pdfimages: a foreign decoder reading the same file. A wrong /ColorSpace or
// /BitsPerComponent that byblos' own reader happens to interpret the same
// wrong way would still be caught here, because poppler does not share that
// bug.
//
// The DCT case cannot use an exact pixelHash comparison, and doing so is not
// a stricter test, just a wrong one: `wantDCT` is Go's image/jpeg decode of
// the JPEG built into the PDF, and `got` is poppler's (libjpeg's) decode of
// the same bytes. Measured directly (16x16, quality 90): 7 of 256 samples
// differ by exactly 1 between the two decoders — ordinary IDCT/rounding
// disagreement between independent JPEG implementations, not a defect in
// BuildPDF or in either decoder. Requiring byte-identical output there would
// make the test depend on which JPEG decoder poppler happens to link against.
// dctTolerance bounds how far that rounding noise may run per channel; the
// Flate case stays exact because that codec is lossless and any difference
// at all would mean the builder or one of the two readers is wrong.
const dctTolerance = 4

func TestBuiltPDFPixelsMatchPdfimages(t *testing.T) {
	if _, err := exec.LookPath("pdfimages"); err != nil {
		t.Skip("pdfimages not installed (poppler)")
	}
	const w, h = 16, 16

	flate := flateGrayImage(t, w, h, 5)
	wantFlate := grayPatternImage(w, h, grayPattern(w, h, 5))

	dct, wantDCT := jpegImage(t, w, h)

	compared := 0
	for name, tc := range map[string]struct {
		img       EncodedImage
		want      image.Image
		tolerance int
	}{
		"flate": {flate, wantFlate, 0},
		"dct":   {dct, wantDCT, dctTolerance},
	} {
		t.Run(name, func(t *testing.T) {
			out := buildOrFatal(t, []BuildPage{{Image: tc.img, DPI: 300}})
			got := pdfimagesPNG(t, out)
			if tc.tolerance == 0 {
				if pixelHash(got) != pixelHash(tc.want) {
					t.Errorf("%s: pdfimages pixels differ from the built image", name)
				}
			} else if d := maxChannelDiff(got, tc.want); d > tc.tolerance {
				t.Errorf("%s: pdfimages pixels differ from the built image by up to %d; want <= %d", name, d, tc.tolerance)
			}
			compared++
		})
	}
	if compared == 0 {
		t.Error("no image was compared against pdfimages; the oracle is vacuous")
	}
}

// maxChannelDiff returns the largest per-channel absolute difference between
// a and b over their shared bounds, used where an exact pixelHash comparison
// would be too strict (see dctTolerance above).
func maxChannelDiff(a, b image.Image) int {
	max := 0
	bounds := a.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			for _, d := range []int{
				int(r1>>8) - int(r2>>8),
				int(g1>>8) - int(g2>>8),
				int(b1>>8) - int(b2>>8),
			} {
				if d < 0 {
					d = -d
				}
				if d > max {
					max = d
				}
			}
		}
	}
	return max
}

// grayPatternImage wraps raw 8-bit grey samples as an image.Image for
// pixelHash comparison.
func grayPatternImage(w, h int, px []byte) image.Image {
	im := image.NewGray(image.Rect(0, 0, w, h))
	copy(im.Pix, px)
	return im
}

// pdfimagesPNG runs pdfimages over pdf and decodes its one extracted image.
func pdfimagesPNG(t *testing.T, pdf []byte) image.Image {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(src, pdf, 0o600); err != nil {
		t.Fatalf("writing pdf: %v", err)
	}
	cmd := exec.Command("pdfimages", "-png", src, filepath.Join(dir, "img"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pdfimages: %v: %s", err, out)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "img-*.png"))
	if err != nil {
		t.Fatalf("globbing pdfimages output: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("pdfimages produced %d files; want 1", len(matches))
	}
	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatalf("opening %s: %v", matches[0], err)
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decoding %s: %v", matches[0], err)
	}
	return im
}
