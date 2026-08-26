// This file is package byblos_test, not package byblos, and that is
// load-bearing rather than stylistic: byb-547's acceptance is "a caller
// OUTSIDE the module renders page 1 using only exported byblos API". An
// in-package test can reach unexported identifiers, so it cannot prove that.
// Every other test file in this package is `package byblos`; arch_test.go's
// TestOnlyPdfdocImportsPdfcpu already anticipated this file existing, by
// listing XTestImports alongside Imports and TestImports.
package byblos_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos"
)

// bornDigitalPDF builds a one-page document with a 4x4 truecolour image
// covering a 4x4 point page, using ONLY exported byblos API -- EmbedPNG and
// BuildPDF. Building the fixture through the public surface keeps this file
// honest: a helper that reached into the package to construct a fixture would
// undermine the point of the external test package.
//
// The page is 4x4 points carrying a 4x4 pixel image, so at a 4px long edge the
// scale is exactly 1 and every device pixel centre maps to exactly one source
// pixel. All 16 pixels are distinct, so a flip, a transpose or an off-by-one
// cannot pass by luck.
func bornDigitalPDF(t *testing.T) []byte {
	t.Helper()
	src := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := 0; i < 16; i++ {
		src.Set(i%4, i/4, color.RGBA{uint8(i * 16), uint8(255 - i*16), uint8(i*8 + 40), 255})
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, src); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	img, _, err := byblos.EmbedPNG(pngBuf.Bytes())
	if err != nil {
		t.Fatalf("EmbedPNG: %v", err)
	}
	var pdf bytes.Buffer
	if err := byblos.BuildPDF(&pdf, []byblos.BuildPage{{Image: img, WidthPt: 4, HeightPt: 4}}); err != nil {
		t.Fatalf("BuildPDF: %v", err)
	}
	return pdf.Bytes()
}

// TestRenderPageIsReachableFromOutsideThePackage is byb-547's acceptance
// clause, stated as nearly literally as a test can state it: page 1, a 400px
// long edge, nothing but exported API.
//
// It asserts the SHAPE of the raster rather than its content, because content
// is what TestRenderPageAgreesWithExtractPageRaster below pins and what
// internal/render's pdftoppm oracles pin in detail. What this one exists to
// catch is the regression byb-547 was filed over: the renderer being present
// in the module and unreachable from outside it. If RenderPage stops being
// exported, or stops being callable without an internal import, this file does
// not compile -- which is the assertion.
func TestRenderPageIsReachableFromOutsideThePackage(t *testing.T) {
	pdf := bornDigitalPDF(t)

	img, err := byblos.RenderPage(bytes.NewReader(pdf), 1, 400)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}
	b := img.Bounds()
	t.Logf("RenderPage(page 1, 400px long edge) -> %dx%d", b.Dx(), b.Dy())
	if b.Dx() != 400 || b.Dy() != 400 {
		t.Errorf("raster is %dx%d; want 400x400 for a square page at a 400px long edge", b.Dx(), b.Dy())
	}
	// A canvas of the right size that nothing painted on is the failure this
	// would otherwise pass: the renderer starts from opaque white.
	var nonWhite int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, g, bl, _ := img.At(x, y).RGBA(); r != 0xffff || g != 0xffff || bl != 0xffff {
				nonWhite++
			}
		}
	}
	t.Logf("non-white pixels: %d of %d", nonWhite, b.Dx()*b.Dy())
	if nonWhite == 0 {
		t.Error("every pixel is white: the page's image was not drawn")
	}
}

// TestRenderPageAgreesWithExtractPageRaster is byb-8b9.2's second acceptance
// clause, MOVED HERE FROM internal/render/oracle_test.go by byb-547 and
// strengthened in the move.
//
// It used to call the unexported render.Page against byblos.ExtractPageRaster,
// which made internal/render's test binary import the root package -- and that
// is exactly the import cycle that appeared the moment the root package gained
// a render.go importing internal/render. Rewriting it against the two EXPORTED
// entry points removes the cycle and tests something better: that the tool a
// caller actually reaches for agrees with the extract path, not that an
// internal function does.
//
// The claim: rendering a one-image page at 1:1 reproduces the extracted raster
// pixel for pixel. It is what stops a later stage wiring the renderer into the
// extract path with a different orientation or placement convention.
func TestRenderPageAgreesWithExtractPageRaster(t *testing.T) {
	pdf := bornDigitalPDF(t)

	pr, err := byblos.ExtractPageRaster(bytes.NewReader(pdf), 1)
	if err != nil {
		t.Fatalf("ExtractPageRaster: %v", err)
	}
	// A 4x4 point page at a 4px long edge is scale 1, the same 1:1 the
	// extracted raster is stored at.
	got, err := byblos.RenderPage(bytes.NewReader(pdf), 1, 4)
	if err != nil {
		t.Fatalf("RenderPage: %v", err)
	}

	eb, gb := pr.Image.Bounds(), got.Bounds()
	if eb.Dx() != gb.Dx() || eb.Dy() != gb.Dy() {
		t.Fatalf("size: ExtractPageRaster %v vs RenderPage %v", eb, gb)
	}
	for y := 0; y < eb.Dy(); y++ {
		for x := 0; x < eb.Dx(); x++ {
			er, eg, ebl, _ := pr.Image.At(eb.Min.X+x, eb.Min.Y+y).RGBA()
			rr, rg, rb, _ := got.At(gb.Min.X+x, gb.Min.Y+y).RGBA()
			if er != rr || eg != rg || ebl != rb {
				t.Fatalf("pixel (%d,%d): extract (%d,%d,%d) vs render (%d,%d,%d)",
					x, y, er>>8, eg>>8, ebl>>8, rr>>8, rg>>8, rb>>8)
			}
		}
	}
}

// TestRenderPageRefusesANonPositiveLongEdge pins the one argument RenderPage
// validates itself rather than passing down.
//
// THE ASSERTION IS ON THE MESSAGE, AND IT HAS TO BE. An earlier version of
// this test checked only err != nil and SURVIVED mutating the guard out --
// vacuously, because render.rasterSize refuses the resulting scale (0, or
// negative) on its own, so both versions error and both passed. The guard buys
// no correctness; what it buys is that the message names longEdgePx, the thing
// the caller actually passed, rather than a "scale" that is not a parameter of
// this function. Asserting that is what makes mutating the guard redden this
// test and no other. Same shape as TestRenderPageNamesTheCropBoxNotAScale.
func TestRenderPageRefusesANonPositiveLongEdge(t *testing.T) {
	pdf := bornDigitalPDF(t)
	for _, px := range []int{0, -1} {
		_, err := byblos.RenderPage(bytes.NewReader(pdf), 1, px)
		if err == nil {
			t.Errorf("RenderPage(long edge %d): want an error, got nil", px)
			continue
		}
		t.Logf("long edge %d -> %v", px, err)
		if !strings.Contains(err.Error(), "long edge") {
			t.Errorf("RenderPage(long edge %d) = %q; want it to name the long edge the caller "+
				"passed, not an internal scale they never chose", px, err)
		}
	}
}

// countingReader is a bytes.Reader that records whether anything read it.
type countingReader struct {
	*bytes.Reader
	reads int
}

func (c *countingReader) Read(p []byte) (int, error) { c.reads++; return c.Reader.Read(p) }

// TestRenderPageContextIsCancellable pins that the ctx argument reaches the
// renderer, and that an ALREADY-cancelled context costs nothing.
//
// THE SECOND HALF IS WHAT MAKES THIS TEST NON-VACUOUS. An earlier version
// asserted only that a cancelled context produced context.Canceled, and it
// SURVIVED deleting RenderPageContext's own up-front checkContext -- because
// internal/render's Page checks ctx too, so the error came back either way and
// the test passed on both. The up-front check buys one thing: refusing before
// pdfdoc.Open reads the document at all, which is the difference between a
// cancelled call returning immediately and one that first parses a large PDF
// it will throw away. That is the same idiom BuildPDFContext follows
// (build.go, checkContext before any work), and the read counter below is what
// pins it -- so removing the check reddens this test and no other.
func TestRenderPageContextIsCancellable(t *testing.T) {
	pdf := bornDigitalPDF(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &countingReader{Reader: bytes.NewReader(pdf)}
	_, err := byblos.RenderPageContext(ctx, r, 1, 400)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RenderPageContext with a cancelled context: err = %v, want it to wrap context.Canceled", err)
	}
	t.Logf("reads of the document on the cancelled path: %d", r.reads)
	if r.reads != 0 {
		t.Errorf("a cancelled context read the document %d times; want 0 -- the ctx check "+
			"must come before pdfdoc.Open, or a cancelled caller still pays for the parse", r.reads)
	}
}

// rawPDF assembles a one-page document with the given /MediaBox and a correct
// xref. BuildPDF cannot make this fixture -- it refuses a page box outside
// [0.001, 1e6] points, which is the whole point of the box below.
func rawPDF(mediaBox string) []byte {
	content := "0 0 0 rg"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox " + mediaBox + " /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.7\n")
	offs := make([]int, len(objs)+1)
	for i, o := range objs {
		offs[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	x := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offs[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, x)
	return b.Bytes()
}

// TestRenderPageNamesTheCropBoxNotAScale defends a guard that would otherwise
// be inert, and says so rather than pretending otherwise.
//
// A degenerate crop box is ALREADY refused without RenderPage's own check:
// internal/render's rasterSize rejects the resulting scale. Measured with the
// guard mutated out, all three boxes below still error --
//
//	[0 0 0 0]     -> "render: scale +Inf is not a positive finite number"
//	[0 0 -5 -5]   -> "render: scale -80 is not a positive finite number"
//	[10 10 10 10] -> "render: scale +Inf is not a positive finite number"
//
// -- so the guard buys no correctness. What it buys is the MESSAGE, and that
// is worth a test because the fallback names a "scale" that is not a parameter
// of this function: the caller passed a long edge in pixels and never chose a
// scale at all. This test asserts what the guard actually earns, so mutating
// it reddens exactly this test and nothing else.
func TestRenderPageNamesTheCropBoxNotAScale(t *testing.T) {
	for _, box := range []string{"[0 0 0 0]", "[0 0 -5 -5]", "[10 10 10 10]"} {
		_, err := byblos.RenderPage(bytes.NewReader(rawPDF(box)), 1, 400)
		if err == nil {
			t.Errorf("MediaBox %s: want an error, got nil", box)
			continue
		}
		t.Logf("MediaBox %-14s -> %v", box, err)
		if !strings.Contains(err.Error(), "degenerate crop box") {
			t.Errorf("MediaBox %s: error is %q; want it to name the crop box. The caller "+
				"passed a long edge in pixels, so an error naming a \"scale\" names something "+
				"they never supplied", box, err)
		}
	}
}

// TestRenderPageRejectsAPageOutOfRange pins that a bad page number is an
// error from the document, not a panic or an empty raster.
func TestRenderPageRejectsAPageOutOfRange(t *testing.T) {
	pdf := bornDigitalPDF(t)
	if _, err := byblos.RenderPage(bytes.NewReader(pdf), 2, 400); err == nil {
		t.Error("RenderPage(page 2) on a one-page document: want an error, got nil")
	}
}
