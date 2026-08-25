package render

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/dobbo-ca/byblos/internal/content"
)

// box100 is a 100x100pt page rendered at scale 1: one device pixel per point,
// device row 0 at user y=100. userToDevRow converts for assertions.
var box100 = content.Box{LLX: 0, LLY: 0, URX: 100, URY: 100}

func render100(t *testing.T, src string) *image.RGBA {
	t.Helper()
	img, err := Page(context.Background(), []byte(src), box100, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	if got := img.Bounds(); got != image.Rect(0, 0, 100, 100) {
		t.Fatalf("bounds = %v; want 100x100", got)
	}
	return img
}

var (
	white = color.RGBA{255, 255, 255, 255}
	black = color.RGBA{0, 0, 0, 255}
	red   = color.RGBA{255, 0, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
)

func pixelAt(img *image.RGBA, x, y int) color.RGBA {
	return img.RGBAAt(x, y)
}

// assertRect asserts that exactly the device pixels with x in [x0,x1) and
// y in [y0,y1) are col, and every other pixel is white. This is the
// exact-pixel oracle: the fill rule is pixel-center inclusion against
// half-open device intervals, so an axis-aligned rectangle's coverage is
// exactly predictable.
func assertRect(t *testing.T, img *image.RGBA, x0, x1, y0, y1 int, col color.RGBA) {
	t.Helper()
	bad := 0
	for y := 0; y < 100; y++ {
		for x := 0; x < 100; x++ {
			want := white
			if x >= x0 && x < x1 && y >= y0 && y < y1 {
				want = col
			}
			if got := pixelAt(img, x, y); got != want {
				bad++
				if bad <= 5 {
					t.Errorf("pixel (%d,%d) = %v; want %v", x, y, got, want)
				}
			}
		}
	}
	if bad > 5 {
		t.Errorf("... and %d more wrong pixels", bad-5)
	}
}

// TestFillRectExactPixels is the acceptance's null check in miniature: a
// blank raster fails it, because the red rectangle must land on exactly the
// predicted pixels. User rect (10,20)-(40,60) maps to device x [10,40),
// rows [40,80) after the y-flip.
func TestFillRectExactPixels(t *testing.T) {
	img := render100(t, "1 0 0 rg 10 20 30 40 re f")
	assertRect(t, img, 10, 40, 40, 80, red)
}

// TestWindingRules draws two nested rectangles with the SAME ring direction
// (re always emits its corners counter-clockwise) and paints once with f and
// once with f*. Nonzero winding fills the inner rectangle (winding 2);
// even-odd leaves it a hole (parity 2). This is byblos settling the winding
// rule x/image/vector leaves as a TODO.
func TestWindingRules(t *testing.T) {
	src := "0 0 1 rg 20 20 60 60 re 35 35 30 30 re "
	nonzero := render100(t, src+"f")
	evenodd := render100(t, src+"f*")

	// (50,50) user = (50,50) device: inside both rectangles.
	if got := pixelAt(nonzero, 50, 50); got != blue {
		t.Errorf("nonzero: inner pixel = %v; want %v", got, blue)
	}
	if got := pixelAt(evenodd, 50, 50); got != white {
		t.Errorf("even-odd: inner pixel = %v; want %v (a hole)", got, white)
	}
	// (25,50): in the ring between the rectangles, filled under both rules.
	if got := pixelAt(nonzero, 25, 50); got != blue {
		t.Errorf("nonzero: ring pixel = %v; want %v", got, blue)
	}
	if got := pixelAt(evenodd, 25, 50); got != blue {
		t.Errorf("even-odd: ring pixel = %v; want %v", got, blue)
	}
	// Outside both.
	if got := pixelAt(evenodd, 10, 50); got != white {
		t.Errorf("even-odd: outside pixel = %v; want white", got)
	}
}

// TestStrokeLineExactPixels: a 4pt horizontal line at user y=50 from x=10 to
// x=90. Half the width spreads either side: device band rows [48,52), butt
// caps end the ink exactly at x=10 and x=90.
func TestStrokeLineExactPixels(t *testing.T) {
	img := render100(t, "0 g 4 w 10 50 m 90 50 l S")
	assertRect(t, img, 10, 90, 48, 52, black)
}

// TestStrokeJoinFillsTheCorner: an L of two 10pt-wide strokes. Butt-capped
// segment rectangles alone leave a notch outside the corner point; the join
// must fill it. (17,83) user sits in that notch, 4.24pt from the corner
// (20,80) -- inside a round-ish join of radius 5. (14,86) is 7.2pt out,
// beyond any join of radius 5, and must stay white.
func TestStrokeJoinFillsTheCorner(t *testing.T) {
	img := render100(t, "0 g 10 w 20 20 m 20 80 l 80 80 l S")
	if got := pixelAt(img, 17, 100-83); got != black {
		t.Errorf("join notch pixel = %v; want black", got)
	}
	if got := pixelAt(img, 14, 100-86); got != white {
		t.Errorf("beyond-join pixel = %v; want white", got)
	}
	// Sanity: both arms of the L are inked.
	if got := pixelAt(img, 20, 100-50); got != black {
		t.Errorf("vertical arm = %v; want black", got)
	}
	if got := pixelAt(img, 50, 100-80); got != black {
		t.Errorf("horizontal arm = %v; want black", got)
	}
}

// TestZeroWidthStrokeIsAHairline: ISO 32000-1 8.4.3.2 makes width 0 the
// thinnest renderable line, not nothing. It must mark exactly one row.
func TestZeroWidthStrokeIsAHairline(t *testing.T) {
	img := render100(t, "0 g 0 w 10 50.5 m 90 50.5 l S")
	assertRect(t, img, 10, 90, 49, 50, black)
}

// TestCTMScalesAndPlaces: cm doubles the rect before it is filled.
func TestCTMScalesAndPlaces(t *testing.T) {
	img := render100(t, "q 2 0 0 2 0 0 cm 1 0 0 rg 5 5 10 10 re f Q")
	// User rect after CTM: (10,10)-(30,30); device rows [70,90).
	assertRect(t, img, 10, 30, 70, 90, red)
}

// TestBezierFlattening fills the region between a chord and a cubic that
// dips from y=50 to y=20 at its midpoint. Probes sit more than the
// flattening tolerance away from the true curve.
func TestBezierFlattening(t *testing.T) {
	img := render100(t, "0 g 20 50 m 80 50 l 80 10 20 10 20 50 c h f")
	for _, p := range []struct {
		ux, uy int
		want   color.RGBA
	}{
		{50, 30, black}, // between curve (y=20 at x=50) and chord (y=50)
		{50, 45, black},
		{50, 15, white}, // below the curve
		{15, 30, white}, // left of the whole shape
		{50, 55, white}, // above the chord
	} {
		if got := pixelAt(img, p.ux, 100-p.uy); got != p.want {
			t.Errorf("user (%d,%d) = %v; want %v", p.ux, p.uy, got, p.want)
		}
	}
}

// TestDeviceGray: 0.5 g fills with mid gray, rounded to 128.
func TestDeviceGray(t *testing.T) {
	img := render100(t, "0.5 g 10 10 20 20 re f")
	gray := color.RGBA{128, 128, 128, 255}
	if got := pixelAt(img, 15, 100-15); got != gray {
		t.Errorf("gray fill = %v; want %v", got, gray)
	}
}

// TestColorSpaceOperators: cs + sc select DeviceRGB colour the long way.
func TestColorSpaceOperators(t *testing.T) {
	img := render100(t, "/DeviceRGB cs 0 0 1 sc 10 10 20 20 re f")
	if got := pixelAt(img, 15, 100-15); got != blue {
		t.Errorf("cs/sc fill = %v; want %v", got, blue)
	}
}

// TestClipRestrictsFill: a W n clip narrows a full-page fill to the clip
// rectangle (recorded as a device-space box in this stage).
func TestClipRestrictsFill(t *testing.T) {
	img := render100(t, "20 20 40 40 re W n 0 g 0 0 100 100 re f")
	assertRect(t, img, 20, 60, 40, 80, black)
}

// TestClipRestoredByQ: the clip is graphics state, so Q discards it.
func TestClipRestoredByQ(t *testing.T) {
	img := render100(t, "q 20 20 40 40 re W n Q 1 0 0 rg 10 20 30 40 re f")
	assertRect(t, img, 10, 40, 40, 80, red)
}

// TestRasterDimensionsClamped: page boxes and scales come out of the file,
// so absurd raster sizes are refused rather than allocated.
func TestRasterDimensionsClamped(t *testing.T) {
	for _, tc := range []struct {
		name  string
		box   content.Box
		scale float64
	}{
		{"huge box", content.Box{URX: 1e9, URY: 1e9}, 1},
		{"huge scale", box100, 1e7},
		{"zero scale", box100, 0},
		{"negative scale", box100, -1},
		{"empty box", content.Box{}, 1},
	} {
		if _, err := Page(context.Background(), nil, tc.box, 0, tc.scale, nil, nil); err == nil {
			t.Errorf("%s: Page returned nil error; want a refusal", tc.name)
		}
	}
}

// TestPathPointBudget: a curve storm cannot allocate unbounded flattened
// points. The budget is lowered so the test does not need a 30 MB stream.
func TestPathPointBudget(t *testing.T) {
	defer func(v int64) { maxPathPoints = v }(maxPathPoints)
	maxPathPoints = 64
	src := "0 g 10 50 m " + strings.Repeat("80 10 20 10 20 50 c 80 10 20 10 20 50 c ", 8) + "f"
	if _, err := Page(context.Background(), []byte(src), box100, 0, 1, nil, nil); err == nil {
		t.Fatal("Page accepted a path beyond the point budget")
	}
}

// TestFillWorkBudget: scanline work is charged as it accrues, so a hostile
// edge pile is cut off rather than looped over.
func TestFillWorkBudget(t *testing.T) {
	defer func(v int64) { maxFillWork = v }(maxFillWork)
	maxFillWork = 16
	var b strings.Builder
	b.WriteString("0 g 0 0 m ")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&b, "%d 100 l %d 0 l ", i, i+1)
	}
	b.WriteString("f")
	if _, err := Page(context.Background(), []byte(b.String()), box100, 0, 1, nil, nil); err == nil {
		t.Fatal("Page accepted fill work beyond the budget")
	}
}

// TestContextCancellation: the operator loop is interruptible, like walk's.
func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	src := strings.Repeat("10 20 30 40 re f ", 100)
	if _, err := Page(ctx, []byte(src), box100, 0, 1, nil, nil); err == nil {
		t.Fatal("Page ignored a cancelled context")
	}
}

// TestStrokeColorOperators: G/RG/CS/SCN drive the STROKE colour, not the
// fill colour -- the other stroke tests use the default black, which routing
// these operators to gs.fill would leave untouched.
func TestStrokeColorOperators(t *testing.T) {
	img := render100(t, "1 0 0 RG 4 w 10 50 m 90 50 l S")
	assertRect(t, img, 10, 90, 48, 52, red)
	img = render100(t, "/DeviceRGB CS 0 0 1 SCN 4 w 10 50 m 90 50 l S")
	assertRect(t, img, 10, 90, 48, 52, blue)
	img = render100(t, "0.5 G 4 w 10 50 m 90 50 l S")
	assertRect(t, img, 10, 90, 48, 52, color.RGBA{128, 128, 128, 255})
}

// TestFillStrokeOperatorB: B both fills (with the fill colour) and strokes
// (with the stroke colour) in one operator.
func TestFillStrokeOperatorB(t *testing.T) {
	img := render100(t, "1 0 0 rg 0 0 1 RG 8 w 20 20 60 60 re B")
	for _, p := range []struct {
		ux, uy int
		want   color.RGBA
		what   string
	}{
		{50, 50, red, "interior fill"},
		{20, 50, blue, "stroke centred on the left edge"},
		{17, 50, blue, "stroke spread outside the edge"},
		{10, 50, white, "beyond the stroke"},
	} {
		if got := pixelAt(img, p.ux, 100-p.uy); got != p.want {
			t.Errorf("%s at user (%d,%d) = %v; want %v", p.what, p.ux, p.uy, got, p.want)
		}
	}
}

// TestEvenOddFillStrokeOperator: b* fills even-odd (the inner rectangle is a
// hole) and strokes the path.
func TestEvenOddFillStrokeOperator(t *testing.T) {
	img := render100(t, "1 0 0 rg 4 w 20 20 60 60 re 35 35 30 30 re b*")
	for _, p := range []struct {
		ux, uy int
		want   color.RGBA
		what   string
	}{
		{50, 50, white, "even-odd hole"},
		{27, 50, red, "ring fill"},
		{20, 50, black, "stroked outer edge"},
		{35, 50, black, "stroked inner edge"},
	} {
		if got := pixelAt(img, p.ux, 100-p.uy); got != p.want {
			t.Errorf("%s at user (%d,%d) = %v; want %v", p.what, p.ux, p.uy, got, p.want)
		}
	}
}

// TestCloseStrokeOperator: s closes the subpath before stroking, so the
// closing diagonal of the L is inked; the same path under S leaves it bare.
// This is the only operator sequence that reaches the closed-subpath stroke
// block via an explicit close.
func TestCloseStrokeOperator(t *testing.T) {
	img := render100(t, "0 g 4 w 20 20 m 20 80 l 80 80 l s")
	if got := pixelAt(img, 50, 50); got != black {
		t.Errorf("closing segment midpoint = %v; want black", got)
	}
	img = render100(t, "0 g 4 w 20 20 m 20 80 l 80 80 l S")
	if got := pixelAt(img, 50, 50); got != white {
		t.Errorf("S must not close: midpoint = %v; want white", got)
	}
}

// TestVYCurveOperators pins the operand-to-control-point mapping of v and y
// (ISO 32000-1 table 59) by requiring pixel identity with the equivalent c
// operator. Swapping the two mappings moves ~90 boundary pixels, so the two
// rasters must also differ from each other.
func TestVYCurveOperators(t *testing.T) {
	yImg := render100(t, "0 g 10 10 m 10 90 90 10 y h f")
	yRef := render100(t, "0 g 10 10 m 10 90 90 10 90 10 c h f")
	vImg := render100(t, "0 g 10 10 m 10 90 90 10 v h f")
	vRef := render100(t, "0 g 10 10 m 10 10 10 90 90 10 c h f")
	if !bytes.Equal(yImg.Pix, yRef.Pix) {
		t.Error("y does not match its c equivalent (c1=(x1,y1), c2=endpoint)")
	}
	if !bytes.Equal(vImg.Pix, vRef.Pix) {
		t.Error("v does not match its c equivalent (c1=current point)")
	}
	if bytes.Equal(yImg.Pix, vImg.Pix) {
		t.Error("y and v produced identical rasters; the two mappings have collapsed")
	}
	if got := pixelAt(yImg, 30, 100-30); got != black {
		t.Errorf("y curve fill interior = %v; want black", got)
	}
}

// TestFillPixelWorkBudget: painted pixels are charged against maxFillWork,
// not just active edges. A full-canvas rectangle charges 3 edge units per
// row (300 total, under this budget) but 100 pixels per row, which must
// trip it.
func TestFillPixelWorkBudget(t *testing.T) {
	defer func(v int64) { maxFillWork = v }(maxFillWork)
	maxFillWork = 350
	if _, err := Page(context.Background(), []byte("0 g 0 0 100 100 re f"), box100, 0, 1, nil, nil); err == nil {
		t.Fatal("Page accepted pixel-fill work beyond the budget")
	}
}

// TestStrokeWorkBudget: stroke rasterisation goes through the same budgeted
// scanline filler as fills, so a stroke is cut off too.
func TestStrokeWorkBudget(t *testing.T) {
	defer func(v int64) { maxFillWork = v }(maxFillWork)
	maxFillWork = 16
	if _, err := Page(context.Background(), []byte("0 g 4 w 10 10 m 90 90 l S"), box100, 0, 1, nil, nil); err == nil {
		t.Fatal("Page accepted stroke work beyond the budget")
	}
}

// TestMalformedStreamStillReturnsCanvas: like Walk, a stream that lexes
// partway still paints the part it reached; the canvas comes back with the
// error unset for plain garbage operators, which are simply ignored.
func TestMalformedStreamStillReturnsCanvas(t *testing.T) {
	img, err := Page(context.Background(), []byte("nonsense ops here 1 0 0 rg 10 20 30 40 re f"), box100, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	assertRect(t, img, 10, 40, 40, 80, red)
}

// TestFillHugeSpanStillPaints is the fill-side twin of
// TestImageHugeTranslationSkips, and it exists because fillEdges had the
// float-space clamp on ONE axis. The y bounds intersect the clip in floats
// before the int conversion; the scanline crossings on x did not. A rect
// 10^31 points wide -- finite, and PDF numbers have no exponent form, so the
// operand is written out -- puts the right-hand crossing past 2^63, where
// amd64's CVTTSD2SI wraps to minInt64, min() takes it, and every span is
// dropped: an all-black page renders fully WHITE. arm64's FCVTZS saturates
// instead, so this passes there either way; the regression it pins is
// amd64's (linux CI and prod), the same convention imagedraw_test.go uses.
//
// Note the failure is one-way -- it drops ink, it never paints ink that
// should not exist -- which is why no visual test caught it.
func TestFillHugeSpanStillPaints(t *testing.T) {
	huge := "1" + strings.Repeat("0", 31)
	img, err := Page(context.Background(), []byte("0 g 0 0 "+huge+" 100 re f"), box100, 0, 1, nil, nil)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	b := img.Bounds()
	inked := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if r, _, _, _ := img.At(x, y).RGBA(); r>>8 < 128 {
				inked++
			}
		}
	}
	if want := b.Dx() * b.Dy(); inked != want {
		t.Errorf("a rect %s points wide inked %d of %d pixels; the whole canvas must be black",
			huge, inked, want)
	}
}
