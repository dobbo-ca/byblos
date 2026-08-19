package skew

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// THESE TESTS ARE THE INSTRUMENT'S CALIBRATION, and they run on every `go test
// ./...` rather than behind a corpus gate. byb-16j.1 spends hours of decoding to
// produce one distribution, and a distribution is only as good as the ruler that
// produced it; an estimator nothing falsifies reports its own bugs as findings
// about the corpus.
//
// GROUND TRUTH IS SYNTHESISED ANALYTICALLY, not by rotating a bitmap. Rotating a
// raster resamples it, so the "known" angle would arrive carrying the very
// interpolation blur the estimator has to see through, and a test that passes
// would not say which of the two was being measured. Instead every pixel is
// evaluated against an ideal page of text whose coordinates are rotated first:
// the pattern is exact at any angle, and the only error left is the pixel grid.

// textPage draws an ideal page of text rotated by deg, in IMAGE coordinates
// where y runs DOWN and a positive deg carries the right-hand end of each line
// downward.
//
// The pattern is lines of words rather than solid bars because the two are not
// the same test: a page of solid bars has a projection comb whatever the word
// spacing does, while real text has gaps along the line as well as between the
// lines, and it is the second that a projection profile has to survive.
func textPage(w, h int, deg float64, bitonal bool) *image.Gray {
	return drawPage(w, h, deg, bitonal, spec{lineH: 11, lineGap: 9, wordW: 47, wordGap: 13, margin: 60})
}

// spec is the ideal page's geometry, in SOURCE pixels. It is a parameter rather
// than a constant because the reduction to working resolution is only tested by
// a page whose strokes are thin RELATIVE TO THE STRIDE, and the default page's
// are eleven pixels thick -- fat enough that any reduction at all keeps them.
type spec struct{ lineH, lineGap, wordW, wordGap, margin int }

func drawPage(w, h int, deg float64, bitonal bool, sp spec) *image.Gray {
	lineH, lineGap := sp.lineH, sp.lineGap
	wordW, wordGap, margin := sp.wordW, sp.wordGap, sp.margin
	rad := deg * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	cx, cy := float64(w)/2, float64(h)/2
	img := image.NewGray(image.Rect(0, 0, w, h))
	ink, paper := uint8(30), uint8(235)
	if bitonal {
		ink, paper = 0, 255
	}
	for y := range h {
		for x := range w {
			// Rotate BACKWARD into the pattern's own frame, so the pattern's
			// horizontal lines come out at +deg in the image.
			dx, dy := float64(x)-cx, float64(y)-cy
			px := dx*cos + dy*sin + cx
			py := -dx*sin + dy*cos + cy
			v := paper
			if inWord(px, py, w, h, margin, lineH, lineGap, wordW, wordGap) {
				v = ink
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// inWord is the ideal page: a block of text with a margin, lines of a fixed
// pitch, and words of a fixed pitch along each line.
func inWord(px, py float64, w, h, margin, lineH, lineGap, wordW, wordGap int) bool {
	if px < float64(margin) || px > float64(w-margin) ||
		py < float64(margin) || py > float64(h-margin) {
		return false
	}
	pitch := float64(lineH + lineGap)
	if math.Mod(py-float64(margin), pitch) > float64(lineH) {
		return false
	}
	wpitch := float64(wordW + wordGap)
	return math.Mod(px-float64(margin), wpitch) <= float64(wordW)
}

// TestRecoversKnownAngle is the headline claim: the number this package reports
// is the angle that was drawn.
//
// THE TOLERANCE IS THE POINT OF THE TABLE. fineStep is 0.025 degrees, so the
// grid alone cannot do better than half of that; everything above it is the
// pixel grid quantising an ideal pattern. A tolerance loose enough to pass
// whatever the estimator does would make this test decoration, so it is set at
// four fine steps and the failure message prints the error, which is what says
// whether a change made the instrument better or worse.
func TestRecoversKnownAngle(t *testing.T) {
	const tol = 4 * fineStep
	// 0.13 is the median placement deskew byb-divert measured over govdocs1, and
	// 1.09 is the widest; both are in the table because MaxSkewDeg was set at 2.0
	// to clear them and the census turns on whether content skew behaves the same
	// way. 12 is past anything Chris asked for and inside the search rail.
	for _, want := range []float64{-12, -7, -2.5, -1.09, -0.5, -0.13, 0, 0.13, 0.5, 1.09, 2.5, 7, 12} {
		img := textPage(700, 900, want, false)
		est := Measure(img, Options{})
		if !est.OK {
			t.Errorf("%+.2f deg: no estimate (conf %.3f, ink %.4f)", want, est.Confidence, est.InkFrac)
			continue
		}
		// The image was drawn at +want in row space, so user space is -want.
		got := -est.Deg
		if math.Abs(got-want) > tol {
			t.Errorf("%+.2f deg: got %+.3f, error %+.3f > %.3f (conf %.2f)",
				want, got, got-want, tol, est.Confidence)
		}
	}
}

// TestSignIsUserSpace pins the axis flip that byb-16j.4 says will be got wrong at
// least once. It is a separate test from the recovery table because the table
// would pass with the sign inverted everywhere and the census would then report
// every correction backwards.
func TestSignIsUserSpace(t *testing.T) {
	// Drawn with each line's right-hand end LOWER on the page. Read as a PDF,
	// whose y runs up, that line descends to the right, so its angle is negative
	// and straightening it is a counter-clockwise turn.
	est := Measure(textPage(700, 900, 3, false), Options{})
	if !est.OK {
		t.Fatalf("no estimate: conf %.3f", est.Confidence)
	}
	if est.Deg >= 0 {
		t.Fatalf("lines descending to the right reported %+.3f; want negative", est.Deg)
	}
}

// TestConfidenceSeparatesTextFromNoise is where minConfidence comes from.
//
// It asserts a MARGIN either side rather than just "text passes": a threshold
// that only one population clears is a threshold that has not been placed, and
// the number this test prints is what a later change to the estimator is judged
// against.
func TestConfidenceSeparatesTextFromNoise(t *testing.T) {
	// The dense default page is the HARD case -- 35% ink, whose comb has the
	// least gap in it -- and a fanned page is harder still, because it smears its
	// own comb. Both are here so the threshold is placed under the worst text in
	// the file rather than under the best.
	structured := map[string]image.Image{
		"text at 0":    textPage(700, 900, 0, false),
		"text at 0.13": textPage(700, 900, 0.13, false),
		"text at 6":    textPage(700, 900, 6, false),
		"fanned 4 deg": warpPage(700, 900, 1, 4),
	}
	flat := map[string]image.Image{
		"uniform noise": noisePage(700, 900),
		"gradient":      gradientPage(700, 900),
		"blob":          blobPage(700, 900),
	}
	for name, img := range structured {
		est := Measure(img, Options{})
		if est.Confidence < 2*minConfidence {
			t.Errorf("%s: confidence %.3f, want >= %.3f (2x the threshold)",
				name, est.Confidence, 2*minConfidence)
		}
	}
	for name, img := range flat {
		est := Measure(img, Options{})
		if est.OK && est.Confidence > minConfidence/2 {
			t.Errorf("%s: confidence %.3f, want <= %.3f (half the threshold); "+
				"reported %+.3f deg", name, est.Confidence, minConfidence/2, est.Deg)
		}
	}
}

// TestBitonalAndGreyAgree checks that the Otsu path and the fixed-cut path find
// the same angle on the same page. They are different code (Options.Bitonal
// skips Otsu entirely), and a census that mixes 1-bit JBIG2 scans with 8-bit
// JPEG ones divides pages by an angle both paths must produce alike.
func TestBitonalAndGreyAgree(t *testing.T) {
	const want = 1.7
	bi := Measure(textPage(700, 900, want, true), Options{Bitonal: true})
	gr := Measure(textPage(700, 900, want, false), Options{})
	if !bi.OK || !gr.OK {
		t.Fatalf("no estimate: bitonal ok=%v conf=%.3f, grey ok=%v conf=%.3f",
			bi.OK, bi.Confidence, gr.OK, gr.Confidence)
	}
	if math.Abs(bi.Deg-gr.Deg) > 4*fineStep {
		t.Errorf("bitonal %+.3f and grey %+.3f disagree by %+.3f",
			bi.Deg, gr.Deg, bi.Deg-gr.Deg)
	}
}

// TestRefusesDegenerateRasters covers the pages a real corpus is full of and
// that have no angle at all. Each must come back !OK; reporting 0 degrees for a
// blank page would put it in the "needs no correction" bucket, which is a
// different claim from "cannot be measured".
func TestRefusesDegenerateRasters(t *testing.T) {
	for name, img := range map[string]image.Image{
		"blank":       fill(700, 900, 255),
		"solid black": fill(700, 900, 0),
		"one pixel":   fill(1, 1, 0),
		"empty":       image.NewGray(image.Rect(0, 0, 0, 0)),
	} {
		if est := Measure(img, Options{}); est.OK {
			t.Errorf("%s: reported %+.3f deg at confidence %.3f; want no estimate",
				name, est.Deg, est.Confidence)
		}
	}
}

// TestDownscalingKeepsTheAngle runs the same drawn angle at two source
// resolutions, one below maxWorkingDim and one well above it, so the block
// reduction is exercised. A reduction that lost thin strokes would show up here
// as a lost or moved peak, and the census decodes 600 DPI base layers.
func TestDownscalingKeepsTheAngle(t *testing.T) {
	const want = 2.5
	small := Measure(textPage(700, 900, want, false), Options{})
	large := Measure(textPage(2100, 2700, want, false), Options{})
	if !small.OK || !large.OK {
		t.Fatalf("no estimate: small ok=%v, large ok=%v", small.OK, large.OK)
	}
	if math.Abs(small.Deg-large.Deg) > 8*fineStep {
		t.Errorf("700x900 %+.3f and 2100x2700 %+.3f disagree by %+.3f",
			small.Deg, large.Deg, small.Deg-large.Deg)
	}
}

// TestThinStrokesSurviveReduction is the test that makes the block MINIMUM
// load-bearing, and it exists because the first mutation run of this package
// found that nothing did.
//
// The case is the sample's 600 DPI bitonal base layers. At that resolution a
// hairline rule or the stem of small type is ONE source pixel, the stride is
// four, and the threshold on a bitonal raster is a fixed cut rather than Otsu's
// -- so a reduction that AVERAGES its block turns one ink pixel among sixteen
// into a value far on the paper side of the cut and the stroke is gone before
// any angle is computed. Taking the darkest pixel keeps it. Averaging survived
// every other test in this file, because their strokes are eleven pixels thick.
func TestThinStrokesSurviveReduction(t *testing.T) {
	const want = 1.7
	// 1-pixel strokes on a 60-pixel line pitch: 10 pt type at 600 DPI.
	hairline := spec{lineH: 1, lineGap: 59, wordW: 200, wordGap: 60, margin: 200}
	est := Measure(drawPage(3600, 4800, want, true, hairline), Options{Bitonal: true})
	if !est.OK {
		t.Fatalf("hairline page lost: no estimate (conf %.3f, ink %.5f, points %d)",
			est.Confidence, est.InkFrac, est.InkPoints)
	}
	if got := -est.Deg; math.Abs(got-want) > 8*fineStep {
		t.Errorf("hairline page: got %+.3f, want %+.3f (conf %.2f, ink %.5f)",
			got, want, est.Confidence, est.InkFrac)
	}
}

// warpPage draws the ideal page through a keystone, so the two things a
// keystone does can be tested apart from each other.
//
// widen scales the page's WIDTH linearly with height: widen > 1 makes the top
// edge wider than the bottom, which is the page tilted about its horizontal
// axis. Text lines stay horizontal and only the sides converge, so this is the
// case Converge must catch and BandSpread cannot.
//
// fan turns each text line by an amount that grows with its height, which is the
// page tilted about its vertical axis. The sides stay parallel and the lines
// stop being so, which is the case BandSpread must catch and Converge cannot.
//
// The two are drawn separately, and NOT as one homography, on purpose: a single
// warp doing both at once could pass the pair of tests with each measurement
// answering the other one's question.
func warpPage(w, h int, widen, fanDeg float64) *image.Gray {
	sp := spec{lineH: 11, lineGap: 9, wordW: 47, wordGap: 13, margin: 60}
	img := image.NewGray(image.Rect(0, 0, w, h))
	cx, cy := float64(w)/2, float64(h)/2
	for y := range h {
		// v runs 0 at the top of the page to 1 at the bottom.
		v := float64(y) / float64(h-1)
		scale := widen + (1-widen)*v
		rad := fanDeg * (v - 0.5) * math.Pi / 180
		sin, cos := math.Sin(rad), math.Cos(rad)
		for x := range w {
			dx, dy := float64(x)-cx, float64(y)-cy
			// Undo the fan first, then the widening, to land in pattern space.
			px := (dx*cos+dy*sin)/scale + cx
			py := -dx*sin + dy*cos + cy
			v := uint8(235)
			if inWord(px, py, w, h, sp.margin, sp.lineH, sp.lineGap, sp.wordW, sp.wordGap) {
				v = 30
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

// TestConvergeSeesATrapezoidAndRotationDoesNot is the keystone half's ground
// truth for the axis that leaves the text lines alone.
//
// The assertion is MONOTONE rather than arithmetic. Converge's exact value
// depends on where the text block's fifth percentile falls, which is a property
// of the drawn page and not of the geometry; what a census needs is that a
// square page reads near zero, that a trapezoid does not, and that a wider
// trapezoid reads wider. An exact expected angle here would pin the fixture, not
// the instrument.
func TestConvergeSeesATrapezoidAndRotationDoesNot(t *testing.T) {
	square := Measure(textPage(700, 900, 0, false), Options{})
	turned := Measure(textPage(700, 900, 3.5, false), Options{})
	mild := Measure(warpPage(700, 900, 1.06, 0), Options{})
	steep := Measure(warpPage(700, 900, 1.15, 0), Options{})
	for name, est := range map[string]Estimate{
		"square": square, "turned": turned, "mild": mild, "steep": steep,
	} {
		if !est.EdgesOK {
			t.Fatalf("%s: no edge fit (ok=%v conf=%.2f resid=%.2f)",
				name, est.OK, est.Confidence, est.EdgeResid)
		}
	}
	// A rotation is not a keystone, and this is the assertion that says so: both
	// sides move together, so what is left after the turn is nothing.
	if math.Abs(square.Converge) > 0.30 {
		t.Errorf("square page converges %+.3f deg; want ~0", square.Converge)
	}
	if math.Abs(turned.Converge) > 0.30 {
		t.Errorf("page turned 3.5 deg converges %+.3f deg; want ~0 "+
			"(a rotation keeps a parallelogram one)", turned.Converge)
	}
	if math.Abs(mild.Converge) < 1.0 {
		t.Errorf("6%% trapezoid converges only %+.3f deg; want a clear signal",
			mild.Converge)
	}
	if math.Abs(steep.Converge) <= math.Abs(mild.Converge) {
		t.Errorf("15%% trapezoid (%+.3f) does not converge harder than 6%% (%+.3f)",
			steep.Converge, mild.Converge)
	}
	// The sides converge; the LINES are untouched, so the other half of the
	// parallelogram test must stay quiet. This is the pairing that stops one
	// measurement standing in for the other.
	if mild.BandSpread > 0.5 {
		t.Errorf("trapezoid moved BandSpread to %.3f; its lines are parallel",
			mild.BandSpread)
	}
	// LeftDeg and RightDeg are RESIDUAL angles -- what is left after the page is
	// turned back by its own line angle -- and this is the only assertion that
	// says so. Converge cannot say it: a rotation moves both sides equally and
	// cancels out of their difference, so Converge is right even when the turn is
	// skipped entirely. The mutation that skips it is invisible here without
	// these two lines.
	if math.Abs(turned.LeftDeg) > 0.30 || math.Abs(turned.RightDeg) > 0.30 {
		t.Errorf("page turned 3.5 deg reports sides at %+.3f / %+.3f; want both "+
			"near 0 (the page is turned back before the sides are fitted)",
			turned.LeftDeg, turned.RightDeg)
	}
}

// TestSpeckleDoesNotMoveTheSides is why the sides are a percentile and not the
// extreme.
//
// A real archive scan carries dust, hole punches, a marginal pen mark and the
// dark band where the platen ends. Every one of them is further out than the
// text, so a side fitted to the LITERAL leftmost ink pixel of each slice is
// fitted to the debris. The page here is square, so any convergence reported is
// the debris being measured.
func TestSpeckleDoesNotMoveTheSides(t *testing.T) {
	clean := textPage(700, 900, 0, false)
	dirty := speckle(textPage(700, 900, 0, false))
	c, d := Measure(clean, Options{}), Measure(dirty, Options{})
	if !c.EdgesOK || !d.EdgesOK {
		t.Fatalf("no edge fit: clean %v, speckled %v", c.EdgesOK, d.EdgesOK)
	}
	if math.Abs(d.Converge) > 0.40 {
		t.Errorf("speckle in the margins converged the sides by %+.3f deg "+
			"(clean page: %+.3f); the side is meant to be a percentile, not the "+
			"outermost pixel", d.Converge, c.Converge)
	}
	if math.Abs(d.Deg-c.Deg) > 4*fineStep {
		t.Errorf("speckle moved the angle from %+.3f to %+.3f", c.Deg, d.Deg)
	}
}

// speckle scatters isolated dark pixels across the whole page, most of them in
// the margins where there is room. Deterministic, and drawn as single pixels so
// it cannot form line structure of its own.
func speckle(img *image.Gray) *image.Gray {
	b := img.Bounds()
	s := uint32(0x9E3779B9)
	for range 900 {
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		x := int(s%uint32(b.Dx())) + b.Min.X
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		y := int(s%uint32(b.Dy())) + b.Min.Y
		img.SetGray(x, y, color.Gray{Y: 0})
	}
	return img
}

// TestBandSpreadSeesAFanAndRotationDoesNot is the same ground truth for the
// other axis: the lines stop being parallel and the sides do not move.
func TestBandSpreadSeesAFanAndRotationDoesNot(t *testing.T) {
	flat := Measure(textPage(700, 900, 2, false), Options{})
	fanned := Measure(warpPage(700, 900, 1, 4), Options{})
	if flat.BandSpread < 0 || fanned.BandSpread < 0 {
		t.Fatalf("bands not measured: flat %.3f, fanned %.3f",
			flat.BandSpread, fanned.BandSpread)
	}
	if flat.BandSpread > 0.25 {
		t.Errorf("page turned 2 deg has band spread %.3f; a rotation turns every "+
			"line by the same angle", flat.BandSpread)
	}
	if fanned.BandSpread < 1.5 {
		t.Errorf("4 deg fan gives band spread %.3f; want it clear of the noise floor",
			fanned.BandSpread)
	}
	// A fan leaves the sides parallel, so the other half must stay quiet.
	if math.Abs(fanned.Converge) > 1.0 {
		t.Errorf("fan moved Converge to %+.3f; its sides are parallel", fanned.Converge)
	}
}

// TestGutterBandDoesNotMoveTheAngle is the case that argues against a LOCAL
// threshold, and it is here because the argument is otherwise only prose.
//
// A bound volume scanned open leaves a dark band down one side where the page
// curves into the spine, and a flatbed leaves one where the platen ends. Both
// are large, dark and have no line structure. A global cut puts the whole band
// on the ink side of the threshold, where it is a solid slab that the profile
// counts at EVERY angle and so cannot prefer one -- the angle survives. Sauvola
// (sauvola.go) would instead follow the band's own illumination and fill it with
// speckle, and speckle in a slab has structure a comb can lock onto. The global
// cut's failure mode is a lost estimate; the local cut's is a confident wrong
// one, and only the first is safe in a census.
func TestGutterBandDoesNotMoveTheAngle(t *testing.T) {
	const want = 1.5
	clean := textPage(700, 900, want, false)
	dirty := textPage(700, 900, want, false)
	// A 70-pixel band down the left tenth, as dark as the ink.
	for y := range 900 {
		for x := range 70 {
			dirty.SetGray(x, y, color.Gray{Y: 25})
		}
	}
	c, d := Measure(clean, Options{}), Measure(dirty, Options{})
	if !d.OK {
		t.Fatalf("gutter band lost the estimate (conf %.3f, ink %.4f)",
			d.Confidence, d.InkFrac)
	}
	if got := -d.Deg; math.Abs(got-want) > 8*fineStep {
		t.Errorf("with a gutter band: got %+.3f, want %+.3f (clean page got %+.3f)",
			got, want, -c.Deg)
	}
}

// TestDecimationKeepsTheAngle drives the ink list past maxInkPoints, which no
// other fixture in this file does.
//
// It is the one place the ORDER of the decimation matters. The points are
// gathered row by row, so a fixed stride through them walks across rows and
// takes a different phase within each; a decimation that instead dropped whole
// ROWS would alias against the line pitch, and at the wrong stride it would
// delete every text line and leave a page of margins.
func TestDecimationKeepsTheAngle(t *testing.T) {
	const want = 2.5
	// 1150x1450 is under maxWorkingDim, so stride is 1 and the working image is
	// the source: about 1.6 megapixels at a third ink, which is well past
	// maxInkPoints.
	big := Measure(textPage(1150, 1450, want, false), Options{})
	if big.InkPoints <= maxInkPoints {
		t.Fatalf("fixture never reached the decimation path: %d points, cap %d",
			big.InkPoints, maxInkPoints)
	}
	if !big.OK {
		t.Fatalf("no estimate (conf %.3f)", big.Confidence)
	}
	if got := -big.Deg; math.Abs(got-want) > 4*fineStep {
		t.Errorf("decimated page: got %+.3f, want %+.3f", got, want)
	}
}

// TestTheTwoScoreFunctionsAgree checks the cross-check itself. AltDeg is only
// worth recording on a census row if the two scores agree on pages that are
// known-good; if they disagreed here, a disagreement over the corpus would say
// nothing about the corpus.
func TestTheTwoScoreFunctionsAgree(t *testing.T) {
	for _, want := range []float64{-7, -1.09, 0, 0.13, 3.5, 9} {
		est := Measure(textPage(700, 900, want, false), Options{})
		if !est.OK {
			t.Errorf("%+.2f: no estimate", want)
			continue
		}
		if math.Abs(est.Deg-est.AltDeg) > 8*fineStep {
			t.Errorf("%+.2f deg: sum-of-squares says %+.3f, first-differences "+
				"says %+.3f", want, est.Deg, est.AltDeg)
		}
	}
}

// TestRaggedRightIsNotAKeystone is the case that the first census run over real
// pages found and that no drawn page in this file had.
//
// Ragged-right typescript ends every paragraph with a short line. The right-hand
// extreme of the slice holding one sits far inside the margin, so the right side
// is not a straight line at all -- and a page that is perfectly flat then reports
// a large convergence. Over 176 real extracted pages the median was 9.6 degrees
// before the fit was made robust and the residual gated.
//
// The assertion is on the GATE and not on the value: a page with no straight
// sides has no measurable convergence, and the honest answer is EdgesOK false
// rather than a number.
func TestRaggedRightIsNotAKeystone(t *testing.T) {
	square := Measure(textPage(700, 900, 0, false), Options{})
	if !square.EdgesOK {
		t.Fatalf("the square page lost its edge fit; the gate is too tight "+
			"(resid %.1f)", square.EdgeResid)
	}
	ragged := Measure(raggedPage(700, 900, 0), Options{})
	if ragged.EdgesOK && math.Abs(ragged.Converge) > 1.0 {
		t.Errorf("ragged-right page reported a convergence of %+.3f deg at "+
			"residual %.1f; a page with no straight right side must not report "+
			"a keystone", ragged.Converge, ragged.EdgeResid)
	}
	// The LINES are still perfectly good, so the angle must survive even though
	// the sides do not. The two halves of the instrument fail independently.
	if !ragged.OK {
		t.Errorf("ragged-right page lost its ANGLE too (conf %.3f); only the "+
			"sides are ragged", ragged.Confidence)
	}
}

// raggedPage is textPage with each line's last word cut back by a deterministic
// amount, which is what a paragraph looks like.
func raggedPage(w, h int, deg float64) *image.Gray {
	img := textPage(w, h, deg, false)
	s := uint32(0x85EBCA6B)
	const margin, lineH, lineGap = 60, 11, 9
	for line := 0; ; line++ {
		y0 := margin + line*(lineH+lineGap)
		if y0 >= h-margin {
			break
		}
		s ^= s << 13
		s ^= s >> 17
		s ^= s << 5
		// Blank the last 0 to 55% of the line, so the right side wanders by
		// hundreds of pixels while the left stays put.
		cut := margin + int(float64(w-2*margin)*(0.45+0.55*float64(s%1000)/1000))
		for y := y0; y < min(y0+lineH+1, h); y++ {
			for x := cut; x < w; x++ {
				img.SetGray(x, y, color.Gray{Y: 235})
			}
		}
	}
	return img
}

// TestPastTheRailIsRefusedNotRounded is the bug the first census run found, and
// it is the one that a synthetic table of angles INSIDE the range cannot catch.
//
// A page turned further than searchDeg has its true peak outside the grid, so
// the score curve is still climbing when the sweep ends and the argmax is the
// last sample: the boundary constant, reported to four decimal places like any
// other angle. Over the pinned sample that put 11 pages at exactly 15.0000 in
// the whole-page column -- 11 of the 18 pages that looked like more than 10
// degrees of skew -- and 45 bands at -15.0000 beside neighbours near zero, which
// reads as a fifteen degree fan on a page that is flat.
func TestPastTheRailIsRefusedNotRounded(t *testing.T) {
	// The search is driven DIRECTLY, on ink laid along lines AT the boundary.
	// Going through Measure does not reach the case: a whole page turned past the
	// range has a flat curve across the entire window and is refused for low
	// confidence instead, which is a different refusal and would let the rail
	// through unnoticed. The corpus reached it on BANDS, whose sparser ink leaves
	// a curve still climbing where the sweep stops.
	//
	// THE PREDICATE IS ABOUT THE BOUNDARY, NOT ABOUT THE TRUE ANGLE, and that is
	// deliberate: an argmax sitting on the last sample means the search never saw
	// the far side of the peak, so it cannot tell 15 degrees from 40. Both are
	// refused and only one of them is wrong, which is the safe direction for a
	// census.
	for _, deg := range []float64{searchDeg, -searchDeg} {
		pts := linePoints(600, 800, deg)
		got, conf, _, railed := search(pts, 600, 800)
		if !railed {
			t.Errorf("ink at the +-%.0f boundary: search returned %+.4f "+
				"(conf %.2f) without setting railed", deg, -got, conf)
		}
	}
	// Ink well inside the range must NOT rail, or the flag would refuse
	// everything and the test above would pass for the wrong reason.
	if _, _, _, railed := search(linePoints(600, 800, 6), 600, 800); railed {
		t.Error("ink at 6 deg, well inside the search, was reported as railed")
	}
	// The range is not narrowed by this: an angle just inside it must still be a
	// measurement. Without this half, railing everything would pass.
	inside := Measure(textPage(700, 900, 12, false), Options{})
	if inside.Railed || !inside.OK {
		t.Errorf("12 deg is inside the +-%.0f search but was refused "+
			"(railed=%v ok=%v deg=%+.4f)", searchDeg, inside.Railed, inside.OK,
			inside.Deg)
	}
	// And a railed whole-page estimate is not OK, whatever its confidence.
	var est Estimate
	est.Deg, est.Railed = searchDeg, true
	if est.Railed && est.OK {
		t.Error("a railed estimate must never be OK")
	}
}

// linePoints lays ink along evenly spaced parallel lines at deg, in row space.
// It is the minimum a projection profile needs to have a peak, and unlike a
// drawn page it puts that peak wherever it is asked to.
func linePoints(w, h int, deg float64) []point {
	rad := deg * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	cx, cy := float64(w)/2, float64(h)/2
	span := float64(w + h)
	var pts []point
	// Each line is the centre plus a step along the line direction (cos, sin)
	// and a fixed offset along its normal (-sin, cos).
	for off := -span; off <= span; off += 20 {
		for s := -span; s <= span; s++ {
			x := cx + s*cos - off*sin
			y := cy + s*sin + off*cos
			if x >= 0 && x < float64(w) && y >= 0 && y < float64(h) {
				pts = append(pts, point{int32(x), int32(y)})
			}
		}
	}
	return pts
}

// noisePage is uncorrelated pixel noise: it has ink everywhere and structure
// nowhere. Deterministic, because a test that draws a different page each run
// cannot be bisected.
func noisePage(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	s := uint32(0x2545F491)
	for y := range h {
		for x := range w {
			s ^= s << 13
			s ^= s >> 17
			s ^= s << 5
			img.SetGray(x, y, color.Gray{Y: uint8(s)})
		}
	}
	return img
}

// gradientPage is the shape of a photograph as far as a global threshold is
// concerned: a smooth ramp that Otsu cuts into two large regions with one soft
// boundary and no periodic structure at all.
func gradientPage(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := (x*180/w + y*60/h)
			img.SetGray(x, y, color.Gray{Y: uint8(v)})
		}
	}
	return img
}

// blobPage is one large dark rectangle on a light field -- a photograph placed
// on a page, or a solid mark. It has sharp edges, which is exactly the thing
// that could fake a peak, and no repetition, which is what a comb needs.
func blobPage(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := uint8(240)
			if x > w/5 && x < 4*w/5 && y > h/4 && y < 3*h/4 {
				v = 40
			}
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
	return img
}

func fill(w, h int, v uint8) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return img
}
